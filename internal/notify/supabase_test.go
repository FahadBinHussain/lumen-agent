package notify

import "testing"

func TestExtractRef(t *testing.T) {
	tests := []struct{ key, want string }{
		{"supabase.abc123def.refresh_token", "abc123def"},
		{"supabase.x.y.z", "x"},
		{"neon.foo.bar", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractRef(tt.key)
		if got != tt.want {
			t.Errorf("extractRef(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestExtractRefEdgeCases(t *testing.T) {
	if got := extractRef("supabase"); got != "" {
		t.Errorf("bare prefix: %q", got)
	}
	if got := extractRef("supabase.one.two.three"); got != "one" {
		t.Errorf("three dots: %q", got)
	}
}