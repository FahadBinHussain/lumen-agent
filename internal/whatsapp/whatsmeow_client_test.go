package whatsapp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTextUnicodeSafeAndLabeled(t *testing.T) {
	parts := splitText(strings.Repeat("বাংলা release line with a link https://example.com\n", 120), whatsappMaxTextRunes)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(parts))
	}
	for i, part := range parts {
		if utf8.RuneCountInString(part) > whatsappMaxTextRunes {
			t.Fatalf("part %d exceeds limit: %d runes", i+1, utf8.RuneCountInString(part))
		}
		if !strings.HasPrefix(part, "[part ") {
			t.Fatalf("part %d is missing continuation label: %q", i+1, part[:min(len(part), 20)])
		}
		if !utf8.ValidString(part) {
			t.Fatalf("part %d is invalid UTF-8", i+1)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestCleanMentionsFromNames(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		names []string
		want  string
	}{
		{"no names keeps text", "hello @Kite how are you", nil, "hello @Kite how are you"},
		{"strips own mention token", "hello @Kite how are you", []string{"Kite"}, "hello how are you"},
		{"strips mention at start", "@Kite check this", []string{"Kite"}, "check this"},
		{"mention only becomes empty", "@Kite", []string{"Kite"}, ""},
		{"multiple names", "hi @Kite and @Ratul", []string{"Kite", "Ratul"}, "hi and"},
		{"name mismatch leaves text", "hello @Kite", []string{"Someone Else"}, "hello @Kite"},
		{"empty text stays empty", "", []string{"Kite"}, ""},
	}
	for _, tc := range cases {
		if got := cleanMentionsFromNames(tc.text, tc.names); got != tc.want {
			t.Errorf("%s: cleanMentionsFromNames(%q, %v) = %q, want %q", tc.name, tc.text, tc.names, got, tc.want)
		}
	}
}
