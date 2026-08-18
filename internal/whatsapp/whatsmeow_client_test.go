package whatsapp

import "testing"

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