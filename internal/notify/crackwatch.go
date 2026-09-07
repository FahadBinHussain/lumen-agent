package notify

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// pollCrackWatch polls the r/CrackWatch RSS feed for scene releases. Faithful
// sibling of pollFreeGames: fetches the Atom feed (with retries - reddit's .rss
// intermittently resets connections from datacenter IPs), keeps only real
// release posts (Game-GROUP title pattern), skips sticky/digest posts, dedupes
// against Neon crack_seen, POSTs new ones as source "crackwatch" with
// dedupeKey = the reddit post URL (guid).
func (s *Service) pollCrackWatch(ctx context.Context) error {
	threads := splitList(s.cfg.CrackWatch.ThreadIDs, "")
	if len(threads) == 0 {
		return fmt.Errorf("no threads configured")
	}

	feedURL := s.cfg.CrackWatch.FeedURL
	if feedURL == "" {
		feedURL = "https://www.reddit.com/r/CrackWatch/.rss"
	}

	// reddit's .rss throttles shared datacenter IPs (Render egress is shared,
	// so our bucket is drained by neighbors). Transport errors and 5xx get 3x
	// backoff retries; a 429 honors the server's Retry-After with ONE delayed
	// retry instead of hammering — rapid retries only deepen the hole.
	// Anything else 4xx fails fast (retrying a 403/404 is pointless).
	var items []feedItem
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		items, lastErr = s.fetchCrackWatchFeed(ctx, feedURL)
		if lastErr == nil {
			break
		}
		var rl *rateLimitedError
		if errors.As(lastErr, &rl) {
			wait := rl.retryAfter
			if wait > maxRateLimitWait {
				wait = maxRateLimitWait
			}
			log.Printf("crackwatch: rate-limited by reddit (retry-after %s) — backing off %s for one retry", rl.retryAfter, wait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			items, lastErr = s.fetchCrackWatchFeed(ctx, feedURL)
			break
		}
		var hs *httpStatusError
		if errors.As(lastErr, &hs) {
			break
		}
		time.Sleep(time.Duration(attempt*5) * time.Second)
	}
	if lastErr != nil {
		return fmt.Errorf("crackwatch feed: %w", lastErr)
	}
	if len(items) == 0 {
		return nil
	}

	dbr, err := s.dbQuery(ctx, "public.crack_seen", "guid", func() []string {
		guids := make([]string, 0, len(items))
		for _, it := range items {
			guids = append(guids, it.GUID)
		}
		return guids
	})
	if err != nil {
		return fmt.Errorf("crack_seen query: %w", err)
	}

	for _, item := range items {
		if dbr.seen[item.GUID] {
			continue
		}
		linkLine := ""
		if item.Link != "" {
			linkLine = "\n\n" + item.Link
		}

		ok := true
		for _, tid := range threads {
			if err := s.postWebhook(ctx, s.cfg.CrackWatch.WebhookURL, "crackwatch", tid,
				"🔓 CRACK: "+item.Title,
				"New release on r/CrackWatch"+linkLine, item.Link, item.GUID); err != nil {
				ok = false
				time.Sleep(500 * time.Millisecond)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !ok {
			continue
		}
		if err := s.dbInsertSeen(ctx, "public.crack_seen", "guid", map[string]string{
			"guid":  item.GUID,
			"title": item.Title,
		}); err != nil {
			continue
		}
	}
	return nil
}

// fetchCrackWatchFeed fetches and filters one CrackWatch feed pass. Only posts
// that look like scene releases are kept (title ends "-GROUP" with an
// uppercase alpha-numeric group, e.g. "Maneater-RUNE"); sticky, digest, and
// meta posts are dropped.
func (s *Service) fetchCrackWatchFeed(ctx context.Context, feedURL string) ([]feedItem, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Element-Orion/1.0 (+rss)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.1")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &rateLimitedError{retryAfter: parseRetryAfter(resp.Header, time.Now()), statusCode: resp.StatusCode}
	}
	if resp.StatusCode >= 400 {
		return nil, &httpStatusError{status: resp.StatusCode}
	}

	all := parseFeedEntries(readAll(resp.Body))

	var items []feedItem
	for _, it := range all {
		if strings.TrimSpace(it.Title) == "" || strings.TrimSpace(it.GUID) == "" {
			continue
		}
		if !releaseTitleRe.MatchString(it.Title) {
			continue
		}
		items = append(items, it)
	}
	return items, nil
}

// releaseTitleRe matches scene-release titles: "GameName-GROUP" where GROUP is
// 2-10 uppercase letters/digits at the end (RUNE, CODEX, SKIDROW, GOG, ...).
// "Daily Releases (...)", "[Crack Watch] ..." stickies, and question threads
// don't match.
var releaseTitleRe = regexp.MustCompile(`-([A-Z0-9]{2,10})$`)

// maxRateLimitWait caps the Retry-After sleep: a bogus huge value must not
// stall the 5m ticker loop for longer than this (the next tick retries anyway).
const maxRateLimitWait = 3 * time.Minute

// defaultRateLimitWait applies when reddit sends no (or a garbage)
// Retry-After header with its 429.
const defaultRateLimitWait = 60 * time.Second

// rateLimitedError is an HTTP 429 carrying the server's Retry-After.
type rateLimitedError struct {
	retryAfter time.Duration
	statusCode int
}

func (e *rateLimitedError) Error() string {
	return fmt.Sprintf("feed http %d (retry-after %s)", e.statusCode, e.retryAfter)
}

// httpStatusError is any other HTTP >= 400: fail fast, never retried.
type httpStatusError struct {
	status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("feed http %d", e.status)
}

// parseRetryAfter reads the Retry-After header (delta-seconds or HTTP date).
// Missing/garbage/non-positive values fall back to defaultRateLimitWait.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return defaultRateLimitWait
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return time.Second
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
		return time.Second
	}
	return defaultRateLimitWait
}
