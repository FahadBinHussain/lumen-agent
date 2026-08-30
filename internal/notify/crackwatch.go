package notify

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
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

	// reddit's .rss is flaky from non-residential IPs; retry 3x with backoff.
	var items []feedItem
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		items, lastErr = s.fetchCrackWatchFeed(ctx, feedURL)
		if lastErr == nil {
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
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("feed http %d", resp.StatusCode)
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
