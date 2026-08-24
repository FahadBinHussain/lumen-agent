package notify

import (
	"testing"
)

func TestParseFeedEntries(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>abc-1</id>
    <title>Amazing free game</title>
    <link href="https://store.example.com/game/1"/>
    <content type="html">&lt;p&gt;Free this week!&lt;/p&gt;</content>
    <category term="source:steam"/>
    <category term="genre:action"/>
  </entry>
  <entry>
    <id>abc-2</id>
    <title>Amazon Prime loot</title>
    <link href="https://luna.amazon.com/game"/>
    <content>Prime special</content>
    <category term="source:AMAZON"/>
  </entry>
  <entry>
    <id>abc-3</id>
    <title>Fab giveaway</title>
    <link href="https://www.fab.com/item/1"/>
    <content>Fab stuff</content>
    <category term="source:fab"/>
  </entry>
  <entry>
    <title>No id entry</title>
    <link href=""/>
    <content>should skip</content>
  </entry>
</feed>`

	items := parseFeedEntries(xml)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d: %+v", len(items), items)
	}
	got := items[0]
	if got.GUID != "abc-1" || got.Title != "Amazing free game" || got.Source != "steam" {
		t.Fatalf("unexpected item: %+v", got)
	}
	if got.Content != "Free this week!" {
		t.Fatalf("content not stripped: %q", got.Content)
	}
}

func TestStripHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<p>Hello <b>world</b></p>", "Hello world"},
		{"No tags", "No tags"},
		{"<a href='x'>link</a> text", "link text"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripHTML(c.in); got != c.want {
			t.Errorf("stripHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitList(t *testing.T) {
	if got := splitList(" a , b ,,c ", "fallback"); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unexpected: %v", got)
	}
	if got := splitList("", "x,y"); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("fallback failed: %v", got)
	}
	if got := splitList(" ", ""); len(got) != 0 {
		t.Fatalf("empty expected: %v", got)
	}
}

func TestCanonicalResetDate(t *testing.T) {
	if got := canonicalResetDate("2026-09-01T00:00:00Z"); got != "2026-09-01" {
		t.Fatalf("got %q", got)
	}
	if got := canonicalResetDate(""); got != "unknown" {
		t.Fatalf("got %q", got)
	}
	if got := canonicalResetDate("-"); got != "unknown" {
		t.Fatalf("got %q", got)
	}
}

func TestReleaseTitleFilter(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"Maneater-RUNE", true},
		{"Warhounds-RUNE", true},
		{"Grand.Theft.Auto.V.Legacy.v1.0.3889.0-RUNE", true},
		{"Atomic.Heart-CODEX", true},
		{"Daily Releases (August 13, 2026)", false},
		{"[Crack Watch] Weekly question thread", false},
		{"[Crack Watch] Games", false},
		{"Something with no group", false},
		{"Game-lowercasegroup", false},
	}
	for _, c := range cases {
		if got := releaseTitleRe.MatchString(c.title); got != c.want {
			t.Errorf("releaseTitleRe(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}