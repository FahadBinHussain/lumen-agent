package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"element-orion/internal/config"
	"element-orion/internal/llm"
)

func testBridgeService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{}
	cfg.LLM.BaseURL = "http://x"
	cfg.LLM.Model = "test-model"
	cfg.App.WorkspaceRoot = dir
	cfg.App.SessionDir = dir
	cfg.App.MemoryDir = filepath.Join(dir, "memory")
	return &Service{
		cfg:        cfg,
		sessions:   map[string][]llm.Message{},
		runCancels: map[string]runCancel{},
		historyPath: filepath.Join(dir, "bridge-sessions.json"),
	}
}

func TestRunCommandUnknownFallsThrough(t *testing.T) {
	s := testBridgeService(t)
	reply, ok := s.runCommand("whatsapp", "123@g.us", "/whatever blah")
	if ok {
		t.Fatalf("unknown command must fall through to the agent, got reply %q", reply)
	}
}

func TestRunCommandNewClearsHistory(t *testing.T) {
	s := testBridgeService(t)
	key := "whatsapp:123@g.us"
	s.sessions[key] = []llm.Message{{Role: "user", Content: "hi"}}

	reply, ok := s.runCommand("whatsapp", "123@g.us", "/new")
	if !ok || !strings.Contains(reply, "Previous context cleared") {
		t.Fatalf("expected new-session reply, got %q (ok=%v)", reply, ok)
	}
	if len(s.sessions[key]) != 0 {
		t.Fatal("history must be cleared after /new")
	}
	if _, err := os.Stat(s.historyPath); err != nil {
		t.Fatalf("history file must be persisted after /new: %v", err)
	}
}

func TestRunCommandStopIdleAndActive(t *testing.T) {
	s := testBridgeService(t)
	key := "messenger:984803114200952"

	reply, ok := s.runCommand("messenger", "984803114200952", "/stop")
	if !ok || !strings.Contains(reply, "No active session") {
		t.Fatalf("expected idle reply, got %q (ok=%v)", reply, ok)
	}

	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	s.runCancels[key] = runCancel{cancel: cancel, seq: 1}
	reply, _ = s.runCommand("messenger", "984803114200952", "/stop")
	if !strings.Contains(reply, "Stopped the active session") {
		t.Fatalf("expected stop reply, got %q", reply)
	}
	if _, exists := s.runCancels[key]; exists {
		t.Fatal("run cancel must be removed after /stop")
	}
}

func TestRunCommandCompact(t *testing.T) {
	s := testBridgeService(t)
	s.cfg.App.HistoryCompaction.Enabled = true
	s.cfg.App.HistoryCompaction.TriggerTokens = 10
	s.cfg.App.HistoryCompaction.TargetTokens = 5
	s.cfg.App.HistoryCompaction.PreserveRecentMessages = 1

	key := "messenger:1"
	s.sessions[key] = []llm.Message{
		{Role: "user", Content: strings.Repeat("a", 400)},
		{Role: "assistant", Content: strings.Repeat("b", 400)},
		{Role: "user", Content: strings.Repeat("c", 400)},
		{Role: "assistant", Content: strings.Repeat("d", 400)},
	}

	reply, ok := s.runCommand("messenger", "1", "/compact")
	if !ok || !strings.Contains(reply, "History compacted") {
		t.Fatalf("expected compact reply, got %q (ok=%v)", reply, ok)
	}
	compacted := len(s.sessions[key])
	if compacted == 0 || compacted == 4 {
		t.Fatalf("expected compaction to reduce history, got %d messages", compacted)
	}

	s.sessions[key] = []llm.Message{{Role: "user", Content: "x"}}
	reply, ok = s.runCommand("messenger", "1", "/compact")
	if !ok || !strings.Contains(reply, "already compact enough") {
		t.Fatalf("expected already-compact reply, got %q (ok=%v)", reply, ok)
	}

	reply, ok = s.runCommand("messenger", "2", "/compact")
	if !ok || !strings.Contains(reply, "Nothing to compact") {
		t.Fatalf("expected empty-compact reply, got %q (ok=%v)", reply, ok)
	}
}

func TestRunCommandStatus(t *testing.T) {
	s := testBridgeService(t)
	s.sessions["whatsapp:123@g.us"] = []llm.Message{{Role: "user", Content: "x"}}

	reply, ok := s.runCommand("whatsapp", "123@g.us", "/status")
	if !ok {
		t.Fatal("expected /status handled")
	}
	for _, want := range []string{"test-model", "history:", "1 messages", "whatsapp:", "n/a"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("status reply missing %q:\n%s", want, reply)
		}
	}
}

func TestRunCommandMemory(t *testing.T) {
	s := testBridgeService(t)
	root := filepath.Join(s.cfg.App.SessionDir, "guild-memory", "whatsapp", "123@g.us")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shard-2026-08-18.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	reply, ok := s.runCommand("whatsapp", "123@g.us", "/memory")
	if !ok || !strings.Contains(reply, "shard-2026-08-18.md") {
		t.Fatalf("expected shard listing, got %q (ok=%v)", reply, ok)
	}

	reply, ok = s.runCommand("whatsapp", "999@g.us", "/memory")
	if !ok || !strings.Contains(reply, "no memory shards") {
		t.Fatalf("expected empty memory reply, got %q (ok=%v)", reply, ok)
	}
}