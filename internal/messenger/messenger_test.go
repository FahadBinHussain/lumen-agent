package messenger

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitMessageUnicodeSafeAndLabeled(t *testing.T) {
	text := strings.Repeat("রোহিঙ্গাদের প্রত্যাবর্তন নিশ্চিতে আন্তর্জাতিক সম্প্রদায়ের সহযোগিতা চাইলেন প্রধানমন্ত্রী\n", 80)
	parts := splitMessage(text, maxMsgLen)
	if len(parts) < 2 {
		t.Fatal("expected a long message to split")
	}
	for i, part := range parts {
		if len(part) > maxMsgLen {
			t.Fatalf("part %d exceeds Messenger limit: %d bytes", i+1, len(part))
		}
		if !strings.HasPrefix(part, "[part ") {
			t.Fatalf("part %d is missing continuation label: %q", i+1, part[:min(len(part), 30)])
		}
		if !utf8.ValidString(part) {
			t.Fatalf("part %d is not valid UTF-8", i+1)
		}
	}
}


func TestReactionHandler(t *testing.T) {
	cl := &Client{}
	var received ReactionEvent
	cl.SetReactionHandler(func(ctx context.Context, react ReactionEvent) {
		received = react
	})

	if cl.reactionHandler == nil {
		t.Fatal("expected reactionHandler to be set")
	}

	cl.reactionHandler(context.Background(), ReactionEvent{
		ThreadID:  12345,
		MessageID: "mid-1",
		ActorID:   67890,
		Reaction:  "👍",
		Timestamp: 1000,
	})

	if received.ThreadID != 12345 || received.MessageID != "mid-1" || received.ActorID != 67890 || received.Reaction != "👍" {
		t.Fatalf("unexpected reaction event: %+v", received)
	}
}
