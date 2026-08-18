package discordbot

import (
	"strings"
	"testing"
)

func TestThinkingAnimNilSafe(t *testing.T) {
	var a *thinkingAnim
	if a.finish(&Service{}, "hello") {
		t.Fatal("nil anim must never claim delivery")
	}
	a.discard(&Service{})
	if a != nil {
		t.Fatal("discard must not panic on nil anim")
	}
}

func TestThinkingReplaceableSinglePart(t *testing.T) {
	if !thinkingReplaceable("short reply") {
		t.Fatal("single-part short content must be replaceable")
	}
}

func TestThinkingReplaceableMultiPart(t *testing.T) {
	long := strings.Repeat("x", discordMessageLimit*2)
	if thinkingReplaceable(long) {
		t.Fatal("over-long content must fall back to the multi-message path")
	}
	if thinkingReplaceable("first part <chunk> second part") {
		t.Fatal("explicit <chunk> splits must fall back")
	}
}

func TestThinkingFramesCycle(t *testing.T) {
	if len(thinkingFrames) < 4 {
		t.Fatalf("expected a smooth frame cycle, got %d frames", len(thinkingFrames))
	}
	if thinkingFrames[0] != "thinking" {
		t.Fatalf("animation must start on the bare word, got %q", thinkingFrames[0])
	}
}
