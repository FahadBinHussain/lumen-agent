package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"element-orion/internal/config"
	"element-orion/internal/discordbot"
)

// Regression tests for the BNP route mirror-edit contract: a mirrored
// whatsapp/discord message must be edited IN PLACE on every later edit round
// (the translation refresh re-edits the same logical message), never
// re-mirrored as a fresh message because an earlier edit consumed the
// mapping. Live bug 2026-10-19: the detected->published edit consumed the
// whatsapp mapping, so the translate-refresh edit fell back to a fresh
// message and the group got a duplicate bubble.

func TestMirrorEditMappingKeptAndRepointed(t *testing.T) {
	s := &Service{waEdits: make(map[string]mirrorEdit), dcEdits: make(map[string]mirrorEdit)}

	s.recordMirrorEdit(s.waEdits, "mid-1|123@g.us", "wa-msg-1")

	// Lookups must never consume the entry: the second edit round has to
	// reach the same whatsapp message.
	for i := 0; i < 2; i++ {
		rec, ok := s.mirrorEditTarget(s.waEdits, "mid-1|123@g.us")
		if !ok || rec.id != "wa-msg-1" {
			t.Fatalf("lookup %d: got (%q, %v), want wa-msg-1", i+1, rec.id, ok)
		}
	}

	// A fresh mirror re-points the mapping so later edits follow the newest
	// bubble instead of retrying one that can no longer be edited.
	s.recordMirrorEdit(s.waEdits, "mid-1|123@g.us", "wa-msg-2")
	if rec, ok := s.mirrorEditTarget(s.waEdits, "mid-1|123@g.us"); !ok || rec.id != "wa-msg-2" {
		t.Fatalf("after re-point: got (%q, %v), want wa-msg-2", rec.id, ok)
	}
	if len(s.waEdits) != 1 {
		t.Fatalf("re-point must replace, not add: map has %d entries", len(s.waEdits))
	}
}

func TestMirrorEditPruneDropsExpiredEntries(t *testing.T) {
	s := &Service{waEdits: make(map[string]mirrorEdit), dcEdits: make(map[string]mirrorEdit)}

	s.mu.Lock()
	s.waEdits["old"] = mirrorEdit{id: "wa-old", at: time.Now().Add(-mirrorEditTTL - time.Hour)}
	s.mu.Unlock()

	s.recordMirrorEdit(s.waEdits, "fresh", "wa-fresh")

	if _, ok := s.mirrorEditTarget(s.waEdits, "old"); ok {
		t.Fatalf("expired entry was not pruned")
	}
	if rec, ok := s.mirrorEditTarget(s.waEdits, "fresh"); !ok || rec.id != "wa-fresh" {
		t.Fatalf("fresh entry lost: got (%q, %v)", rec.id, ok)
	}
}

type fakeRouteDiscord struct {
	patches    []string // message IDs EditPlainText was called with
	sends      []string // texts sent as fresh messages
	editErr    error
	sendNextID string
}

func (f *fakeRouteDiscord) IsConnected() bool { return true }

func (f *fakeRouteDiscord) SendPlainText(channelID string, text string) (string, error) {
	f.sends = append(f.sends, text)
	if f.sendNextID == "" {
		return "dc-fresh", nil
	}
	return f.sendNextID, nil
}

func (f *fakeRouteDiscord) EditPlainText(channelID string, messageID string, text string) error {
	if f.editErr != nil {
		return f.editErr
	}
	f.patches = append(f.patches, messageID)
	return nil
}

func (f *fakeRouteDiscord) ListChannels() ([]discordbot.ChannelInfo, error) { return nil, nil }

func discordRouteConfig(t *testing.T) config.Config {
	t.Helper()
	// Discord-only route: the discord leg runs without needing a messenger
	// client; EditRoute's trailing "no messenger channel for edits" error is
	// expected and irrelevant to these assertions.
	return writeTestConfig(t, "  routes:\n    bnp-test:\n      - platform: discord\n        channel_id: DC-CH-1\n")
}

func TestEditRouteDiscordEditsSameMessageOnEveryRound(t *testing.T) {
	cfg := discordRouteConfig(t)
	fake := &fakeRouteDiscord{}
	s := &Service{cfg: cfg, dcEdits: make(map[string]mirrorEdit), discord: fake}
	s.recordMirrorEdit(s.dcEdits, "mid-1|DC-CH-1", "dc-msg-9")

	for _, text := range []string{"round one", "round two"} {
		_ = s.EditRoute(context.Background(), "bnp-test", "mid-1", text)
	}

	if len(fake.sends) != 0 {
		t.Fatalf("expected no fresh mirrors, got %d: %v", len(fake.sends), fake.sends)
	}
	if len(fake.patches) != 2 || fake.patches[0] != "dc-msg-9" || fake.patches[1] != "dc-msg-9" {
		t.Fatalf("expected both rounds to patch dc-msg-9, got %v", fake.patches)
	}
	if _, ok := s.mirrorEditTarget(s.dcEdits, "mid-1|DC-CH-1"); !ok {
		t.Fatalf("mapping was consumed; a later edit round would mirror fresh again")
	}
}

func TestEditRouteDiscordFailureRepointsMapping(t *testing.T) {
	cfg := discordRouteConfig(t)
	fake := &fakeRouteDiscord{editErr: errors.New("discord patch failed"), sendNextID: "dc-msg-10"}
	s := &Service{cfg: cfg, dcEdits: make(map[string]mirrorEdit), discord: fake}
	s.recordMirrorEdit(s.dcEdits, "mid-1|DC-CH-1", "dc-msg-9")

	_ = s.EditRoute(context.Background(), "bnp-test", "mid-1", "round one")

	if len(fake.sends) != 1 {
		t.Fatalf("failed patch must fall back to exactly one fresh mirror, got %d", len(fake.sends))
	}
	rec, ok := s.mirrorEditTarget(s.dcEdits, "mid-1|DC-CH-1")
	if !ok || rec.id != "dc-msg-10" {
		t.Fatalf("mapping not re-pointed to the fresh mirror: got (%q, %v)", rec.id, ok)
	}

	fake.editErr = nil
	_ = s.EditRoute(context.Background(), "bnp-test", "mid-1", "round two")
	if len(fake.patches) != 1 || fake.patches[0] != "dc-msg-10" {
		t.Fatalf("later edit must patch the fresh mirror, got %v", fake.patches)
	}
}
