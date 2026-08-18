package bridge

import (
	"context"
	"strings"
	"time"

	"element-orion/internal/agent"
	"element-orion/internal/whatsapp"
)

// streamSignal is one renderer input: a token delta to append to the live
// preview, or a reset marker (a new model call starting after a tool call).
type streamSignal struct {
	delta string
	reset bool
	tool  string
}

// feedStreamSignals forwards agent events into the renderer channel without
// ever blocking the runner (a full buffer drops deltas, never stalls a turn).
func feedStreamSignals(stream chan<- streamSignal, ev agent.Event) {
	if stream == nil {
		return
	}
	var sig streamSignal
	switch ev.Kind {
	case agent.EventStreamDelta:
		sig.delta = ev.Message
	case agent.EventToolStarted:
		sig.reset = true
		sig.tool = ev.ToolName
	default:
		return
	}
	select {
	case stream <- sig:
	default:
	}
}

// renderThinkingEdits is the real TUI-style animation: instead of cycling
// fake dots, it edits the placeholder with the ACTUAL token stream as it
// arrives from the model (whatsapp protocol edits have no practical cap).
// Edits are throttled to ~2.5/s to stay polite to the server, the preview is
// truncated, and the loop dies on the stop signal or the first failed edit
// (the final sendReply then takes over with the complete reply).
func renderThinkingEdits(ctx context.Context, w *whatsapp.WhatsmeowClient, jid string, msgID string, signals <-chan streamSignal, stop <-chan struct{}) {
	if w == nil {
		return
	}
	const previewMax = 1500
	const throttle = 400 * time.Millisecond

	var preview strings.Builder
	var lastEdit time.Time
	justReset := false

	flush := func() bool {
		if preview.Len() == 0 {
			return true
		}
		text := preview.String()
		if len(text) > previewMax {
			text = text[:previewMax] + "..."
		}
		if err := w.EditText(ctx, jid, msgID, text); err != nil {
			return false
		}
		lastEdit = time.Now()
		return true
	}

	ticker := time.NewTicker(throttle)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			if sig.reset {
				preview.Reset()
				justReset = true
				if sig.tool != "" {
					preview.WriteString("using " + sig.tool + "...")
				}
				if !flush() {
					return
				}
				continue
			}
			if justReset {
				preview.Reset()
				justReset = false
			}
			preview.WriteString(sig.delta)
			if time.Since(lastEdit) >= throttle && !flush() {
				return
			}
		case <-ticker.C:
			if !flush() {
				return
			}
		}
	}
}
