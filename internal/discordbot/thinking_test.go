package discordbot

import (
	"strings"
	"testing"

	"element-orion/internal/agent"
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

func TestThinkingFeedMapsEventsToSignals(t *testing.T) {
	a := &thinkingAnim{signals: make(chan streamSignal, 16)}
	a.feed(agent.Event{Kind: agent.EventStreamDelta, Message: "tok"})
	a.feed(agent.Event{Kind: agent.EventToolStarted, ToolName: "exec_command"})
	a.feed(agent.Event{Kind: agent.EventStatus, Message: "ignored"})

	sig := <-a.signals
	if sig.delta != "tok" || sig.reset {
		t.Fatalf("expected token delta, got %+v", sig)
	}
	sig = <-a.signals
	if !sig.reset || sig.tool != "exec_command" {
		t.Fatalf("expected tool reset marker, got %+v", sig)
	}
	select {
	case extra := <-a.signals:
		t.Fatalf("non-stream events must not reach the renderer, got %+v", extra)
	default:
	}
}
