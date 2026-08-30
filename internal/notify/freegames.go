package notify

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// pollFreeGames is a faithful Go port of murmur/vercel/app/api/free-games/route.ts:
// fetches the lootscraper Atom feed, regex-parses <entry> blocks, skips amazon
// sources + blocked hosts, dedupes against Neon game_seen, POSTs new ones as
// source "gamebot" with dedupeKey = guid.
func (s *Service) pollFreeGames(ctx context.Context) error {
	threads := splitList(s.cfg.FreeGames.ThreadIDs, "")
	if len(threads) == 0 {
		return fmt.Errorf("no threads configured")
	}

	const feedURL = "https://feed.eikowagenknecht.com/lootscraper.xml"
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("feed fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("feed http %d", resp.StatusCode)
	}

	xmlData := readAll(resp.Body)
	items := parseFeedEntries(xmlData)
	if len(items) == 0 {
		return nil
	}

	dbr, err := s.dbQuery(ctx, "public.game_seen", "guid", func() []string {
		guids := make([]string, 0, len(items))
		for _, it := range items {
			guids = append(guids, it.GUID)
		}
		return guids
	})
	if err != nil {
		return fmt.Errorf("game_seen query: %w", err)
	}

	for _, item := range items {
		if dbr.seen[item.GUID] {
			continue
		}
		msg := item.Content
		if msg == "" {
			msg = "New free game available!"
		}
		sourceLabel := ""
		if item.Source != "" {
			sourceLabel = "[" + item.Source + "] "
		}
		linkLine := ""
		if item.Link != "" {
			linkLine = "\n\n" + item.Link
		}

		ok := true
		for _, tid := range threads {
			if err := s.postWebhook(ctx, s.cfg.FreeGames.WebhookURL, "gamebot", tid,
				"🎮 FREE: "+item.Title,
				sourceLabel+msg+linkLine, item.Link, item.GUID); err != nil {
				ok = false
				time.Sleep(500 * time.Millisecond)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !ok {
			continue
		}
		if err := s.dbInsertSeen(ctx, "public.game_seen", "guid", map[string]string{
			"guid":  item.GUID,
			"title": item.Title,
		}); err != nil {
			continue
		}
	}
	return nil
}

type feedItem struct {
	GUID    string
	Title   string
	Link    string
	Content string
	Source  string
}

var (
	entryRe   = regexp.MustCompile(`(?s)<entry>(.*?)</entry>`)
	tagRe     = func(tag string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)<` + tag + `[^>]*>(.*?)</` + tag + `>`)
	}
	linkHrefRe = regexp.MustCompile(`<link[^>]*href="([^"]*)"[^>]*>`)
	categoryRe = regexp.MustCompile(`<category[^>]*term="([^"]*)"[^>]*>`)
)

func parseFeedEntries(xmlData string) []feedItem {
	blockedHosts := []string{"luna.amazon.com", "appraven.net", "fab.com"}
	var items []feedItem
	for _, m := range entryRe.FindAllStringSubmatch(xmlData, -1) {
		block := m[1]
		getTag := func(tag string) string {
			if m2 := tagRe(tag).FindStringSubmatch(block); m2 != nil {
				return strings.TrimSpace(m2[1])
			}
			return ""
		}
		getLink := ""
		if m2 := linkHrefRe.FindStringSubmatch(block); m2 != nil {
			getLink = m2[1]
		}
		var categories []string
		for _, cm := range categoryRe.FindAllStringSubmatch(block, -1) {
			categories = append(categories, cm[1])
		}

		title := html.UnescapeString(getTag("title"))
		guid := getTag("id")
		if guid == "" {
			guid = getLink
		}
		content := stripHTML(html.UnescapeString(getTag("content")))

		source := ""
		for _, c := range categories {
			if strings.HasPrefix(c, "source:") {
				source = strings.TrimPrefix(c, "source:")
				break
			}
		}

		if guid == "" || title == "" {
			continue
		}
		if strings.Contains(strings.ToLower(source), "amazon") {
			continue
		}
		if u, err := url.Parse(getLink); err == nil {
			host := strings.TrimPrefix(u.Hostname(), "www.")
			for _, b := range blockedHosts {
				if host == b || strings.HasSuffix(host, "."+b) {
					goto blocked
				}
			}
		}
		items = append(items, feedItem{GUID: guid, Title: title, Link: getLink, Content: content, Source: source})
	blocked:
	}
	return items
}