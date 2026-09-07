package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		header string
		want  time.Duration
	}{
		{"delta seconds", "120", 2 * time.Minute},
		{"http date", now.Add(90 * time.Second).UTC().Format(http.TimeFormat), 90 * time.Second},
		{"missing", "", defaultRateLimitWait},
		{"garbage", "whenever", defaultRateLimitWait},
		{"zero", "0", time.Second},
		{"past date", now.Add(-time.Minute).UTC().Format(http.TimeFormat), time.Second},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.header != "" {
			h.Set("Retry-After", c.header)
		}
		got := parseRetryAfter(h, now)
		// HTTP-date rounding to the second: allow 2s slack.
		if got < c.want-2*time.Second || got > c.want+2*time.Second {
			t.Errorf("%s: parseRetryAfter(%q) = %v, want ~%v", c.name, c.header, got, c.want)
		}
	}
}

const crackwatchTestFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>t3-sticky1</id>
    <title>[Crack Watch] Weekly question thread</title>
    <link href="https://www.reddit.com/r/CrackWatch/comments/x/"/>
  </entry>
  <entry>
    <id>t3-digest1</id>
    <title>Daily Releases (September 7, 2026)</title>
    <link href="https://www.reddit.com/r/CrackWatch/comments/y/"/>
  </entry>
</feed>`

// A 429 must back off for Retry-After and retry exactly once instead of
// hammering: 2 requests total, ~1s apart, then success (no releases → nil).
func TestPollCrackWatch429BackoffThenSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Write([]byte(crackwatchTestFeed))
	}))
	defer srv.Close()

	s := &Service{
		cfg:    Config{CrackWatch: CrackWatchCfg{FeedURL: srv.URL, ThreadIDs: "123"}},
		client: srv.Client(),
	}
	start := time.Now()
	if err := s.pollCrackWatch(context.Background()); err != nil {
		t.Fatalf("poll returned error: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("expected exactly 2 requests (429 + 1 retry), got %d", n)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("backoff not honored: poll took %v", elapsed)
	}
}

// A non-429 4xx must fail fast with a single request — no blind retries.
func TestPollCrackWatch404FailsFast(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := &Service{
		cfg:    Config{CrackWatch: CrackWatchCfg{FeedURL: srv.URL, ThreadIDs: "123"}},
		client: srv.Client(),
	}
	err := s.pollCrackWatch(context.Background())
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	var hs *httpStatusError
	if !errors.As(err, &hs) || hs.status != 404 {
		t.Fatalf("expected *httpStatusError{404}, got %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("expected exactly 1 request (fail fast), got %d", n)
	}
}
