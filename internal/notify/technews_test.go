package notify

import (
	"strings"
	"testing"
)

func TestCleanFeedText(t *testing.T) {
	input := `<p>This is a <b>bold</b> test with <a href="http://example.com">a link</a> &amp; special chars.</p>`
	expected := "This is a bold test with a link & special chars."
	got := cleanFeedText(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestParseFeedDate(t *testing.T) {
	cases := []struct {
		raw      string
		expected int // year
	}{
		{"Sun, 25 Oct 2026 12:37:54 +0000", 2026},
		{"25 Oct 2026 12:37:54 GMT", 2026},
		{"2026-10-25T12:37:54Z", 2026},
		{"Sun, 25 Oct 2026 08:37:54 -0400", 2026},
	}
	for _, tc := range cases {
		d := parseFeedDate(tc.raw)
		if d.Year() != tc.expected {
			t.Errorf("parseFeedDate(%q) expected year %d, got %d (zero=%v)", tc.raw, tc.expected, d.Year(), d.IsZero())
		}
	}
}

func TestParseTechFeedDataRSS(t *testing.T) {
	rssXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Sample Tech Feed</title>
    <link>https://example.com</link>
    <item>
      <title>Breaking: Superchip Announced</title>
      <link>https://example.com/superchip</link>
      <guid>https://example.com/superchip?id=123</guid>
      <comments>https://example.com/comments/123</comments>
      <description><![CDATA[<p>A new superchip with <b>100B transistors</b> has been unveiled.</p>]]></description>
      <pubDate>Sun, 25 Oct 2026 12:00:00 GMT</pubDate>
    </item>
    <item>
      <title>Second Story</title>
      <link>https://example.com/second</link>
      <description>Simple description</description>
      <pubDate>Sun, 25 Oct 2026 11:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`

	items := parseTechFeedData("Sample Tech", []byte(rssXML))
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	it1 := items[0]
	if it1.FeedName != "Sample Tech" {
		t.Errorf("expected feed name 'Sample Tech', got %q", it1.FeedName)
	}
	if it1.Title != "Breaking: Superchip Announced" {
		t.Errorf("expected title 'Breaking: Superchip Announced', got %q", it1.Title)
	}
	if it1.GUID != "https://example.com/superchip?id=123" {
		t.Errorf("expected GUID 'https://example.com/superchip?id=123', got %q", it1.GUID)
	}
	if it1.Link != "https://example.com/superchip" {
		t.Errorf("expected link 'https://example.com/superchip', got %q", it1.Link)
	}
	if it1.Comments != "https://example.com/comments/123" {
		t.Errorf("expected comments 'https://example.com/comments/123', got %q", it1.Comments)
	}
	if !strings.Contains(it1.Summary, "100B transistors") || strings.Contains(it1.Summary, "<p>") {
		t.Errorf("expected clean summary without HTML tags, got %q", it1.Summary)
	}
	if it1.PubDate.IsZero() {
		t.Errorf("expected non-zero pubDate")
	}

	// Second item has no explicit GUID, should fall back to Link
	it2 := items[1]
	if it2.GUID != "https://example.com/second" {
		t.Errorf("expected fallback GUID from link, got %q", it2.GUID)
	}
}

func TestParseTechFeedDataAtom(t *testing.T) {
	atomXML := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Tech Feed</title>
  <entry>
    <id>urn:uuid:1234-5678</id>
    <title>Quantum Computing Milestone</title>
    <link rel="alternate" href="https://example.com/quantum"/>
    <summary><![CDATA[Scientists achieved <em>fault-tolerant qubits</em>.]]></summary>
    <published>2026-10-25T14:30:00Z</published>
  </entry>
</feed>`

	items := parseTechFeedData("Atom News", []byte(atomXML))
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	it := items[0]
	if it.FeedName != "Atom News" {
		t.Errorf("expected feed name 'Atom News', got %q", it.FeedName)
	}
	if it.GUID != "urn:uuid:1234-5678" {
		t.Errorf("expected GUID 'urn:uuid:1234-5678', got %q", it.GUID)
	}
	if it.Link != "https://example.com/quantum" {
		t.Errorf("expected link 'https://example.com/quantum', got %q", it.Link)
	}
	if it.Title != "Quantum Computing Milestone" {
		t.Errorf("expected title 'Quantum Computing Milestone', got %q", it.Title)
	}
	if !strings.Contains(it.Summary, "fault-tolerant qubits") || strings.Contains(it.Summary, "<em>") {
		t.Errorf("expected clean summary, got %q", it.Summary)
	}
	if it.PubDate.Year() != 2026 {
		t.Errorf("expected pubDate year 2026, got %v", it.PubDate)
	}
}
