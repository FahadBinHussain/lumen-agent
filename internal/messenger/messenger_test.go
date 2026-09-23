package messenger

import (
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
		if utf8.RuneCountInString(part) > maxMsgLen {
			t.Fatalf("part %d exceeds Messenger limit: %d", i+1, utf8.RuneCountInString(part))
		}
		if !strings.HasPrefix(part, "[part ") {
			t.Fatalf("part %d is missing continuation label: %q", i+1, part[:min(len(part), 30)])
		}
		if !utf8.ValidString(part) {
			t.Fatalf("part %d is not valid UTF-8", i+1)
		}
	}
}
