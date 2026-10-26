package notify

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var defaultTechFeeds = []TechNewsFeedCfg{
	{Name: "Ars Technica", URL: "https://feeds.arstechnica.com/arstechnica/index"},
	{Name: "The Verge", URL: "https://www.theverge.com/rss/index.xml"},
	{Name: "BleepingComputer", URL: "https://www.bleepingcomputer.com/feed/"},
	{Name: "Hacker News", URL: "https://news.ycombinator.com/rss"},
}

type techFeedItem struct {
	FeedName string
	GUID     string
	Title    string
	Link     string
	Comments string
	Summary  string
	PubDate  time.Time
}

// XML structures for RSS 2.0
type techRSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	GUID        string `xml:"guid"`
	Comments    string `xml:"comments"`
	PubDate     string `xml:"pubDate"`
}

type techRSSDoc struct {
	Channel struct {
		Title string        `xml:"title"`
		Items []techRSSItem `xml:"item"`
	} `xml:"channel"`
}

// XML structures for Atom 1.0
type techAtomEntry struct {
	Title     string `xml:"title"`
	Summary   string `xml:"summary"`
	Content   string `xml:"content"`
	ID        string `xml:"id"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
	Links     []struct {
		Rel  string `xml:"rel,attr"`
		Href string `xml:"href,attr"`
	} `xml:"link"`
}

type techAtomDoc struct {
	Title   string          `xml:"title"`
	Entries []techAtomEntry `xml:"entry"`
}

var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func cleanFeedText(s string) string {
	s = htmlTagRegex.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func parseFeedDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"02 Jan 2006 15:04:05 MST",
		"02 Jan 2006 15:04:05 -0700",
		"2 Jan 2006 15:04:05 MST",
		"2 Jan 2006 15:04:05 -0700",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func parseTechFeedData(feedName string, data []byte) []techFeedItem {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}

	var items []techFeedItem

	// 1. Try RSS 2.0
	var rssDoc techRSSDoc
	if err := xml.Unmarshal(trimmed, &rssDoc); err == nil && len(rssDoc.Channel.Items) > 0 {
		for _, it := range rssDoc.Channel.Items {
			link := strings.TrimSpace(it.Link)
			guid := strings.TrimSpace(it.GUID)
			if guid == "" {
				guid = link
			}
			if guid == "" {
				continue
			}
			title := cleanFeedText(it.Title)
			if title == "" {
				title = "Untitled update"
			}
			summary := cleanFeedText(it.Description)
			if strings.EqualFold(summary, "comments") || strings.EqualFold(summary, "description") {
				summary = ""
			}
			if len(summary) > 400 {
				summary = summary[:397] + "..."
			}

			items = append(items, techFeedItem{
				FeedName: feedName,
				GUID:     guid,
				Title:    title,
				Link:     link,
				Comments: strings.TrimSpace(it.Comments),
				Summary:  summary,
				PubDate:  parseFeedDate(it.PubDate),
			})
		}
		return items
	}

	// 2. Try Atom 1.0
	var atomDoc techAtomDoc
	if err := xml.Unmarshal(trimmed, &atomDoc); err == nil && len(atomDoc.Entries) > 0 {
		for _, en := range atomDoc.Entries {
			var link string
			for _, l := range en.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = strings.TrimSpace(l.Href)
					break
				}
			}
			if link == "" && len(en.Links) > 0 {
				link = strings.TrimSpace(en.Links[0].Href)
			}
			guid := strings.TrimSpace(en.ID)
			if guid == "" {
				guid = link
			}
			if guid == "" {
				continue
			}
			title := cleanFeedText(en.Title)
			if title == "" {
				title = "Untitled update"
			}
			rawSummary := en.Summary
			if strings.TrimSpace(rawSummary) == "" {
				rawSummary = en.Content
			}
			summary := cleanFeedText(rawSummary)
			if strings.EqualFold(summary, "comments") || strings.EqualFold(summary, "description") {
				summary = ""
			}
			if len(summary) > 400 {
				summary = summary[:397] + "..."
			}

			pubDate := parseFeedDate(en.Published)
			if pubDate.IsZero() {
				pubDate = parseFeedDate(en.Updated)
			}

			items = append(items, techFeedItem{
				FeedName: feedName,
				GUID:     guid,
				Title:    title,
				Link:     link,
				Summary:  summary,
				PubDate:  pubDate,
			})
		}
		return items
	}

	return nil
}

func (s *Service) fetchTechFeed(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Element-Orion/1.0 (+https://github.com/FahadBinHussain/lumen-agent)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.1")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("feed fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("feed http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
}

func (s *Service) pollTechNews(ctx context.Context) error {
	threads := splitList(s.cfg.TechNews.ThreadIDs, "")
	if len(threads) == 0 {
		return fmt.Errorf("no threads configured")
	}

	feeds := s.cfg.TechNews.Feeds
	if len(feeds) == 0 {
		feeds = defaultTechFeeds
	}

	for _, feed := range feeds {
		feedName := strings.TrimSpace(feed.Name)
		if feedName == "" {
			feedName = "Tech News"
		}
		feedURL := strings.TrimSpace(feed.URL)
		if feedURL == "" {
			continue
		}

		data, err := s.fetchTechFeed(ctx, feedURL)
		if err != nil {
			log.Printf("tech-news: fetch %s (%s) failed: %v", feedName, feedURL, err)
			continue
		}

		items := parseTechFeedData(feedName, data)
		if len(items) == 0 {
			continue
		}

		// Initial seed check: if this feed has 0 seen rows, seed current items so we don't spam historical news
		if s.db != nil {
			hasSeen, err := s.dbHasSeenSource(ctx, feedName)
			if err != nil {
				log.Printf("tech-news: check seen source %s failed: %v", feedName, err)
			} else if !hasSeen {
				for _, it := range items {
					_ = s.dbInsertSeen(ctx, "public.tech_seen", "guid", map[string]string{
						"guid":   it.GUID,
						"source": feedName,
						"title":  it.Title,
					})
				}
				log.Printf("tech-news: initialized %s with %d existing items (seeding baseline)", feedName, len(items))
				continue
			}
		}

		dbr, err := s.dbQuery(ctx, "public.tech_seen", "guid", func() []string {
			guids := make([]string, 0, len(items))
			for _, it := range items {
				guids = append(guids, it.GUID)
			}
			return guids
		})
		if err != nil {
			log.Printf("tech-news: tech_seen query failed: %v", err)
			continue
		}

		for _, item := range items {
			if dbr.seen[item.GUID] {
				continue
			}
			// Skip articles older than 48 hours to prevent stale news delivery
			if !item.PubDate.IsZero() && time.Since(item.PubDate) > 48*time.Hour {
				_ = s.dbInsertSeen(ctx, "public.tech_seen", "guid", map[string]string{
					"guid":   item.GUID,
					"source": feedName,
					"title":  item.Title,
				})
				continue
			}

			title := "📰 " + item.FeedName + ": " + item.Title
			msg := item.Summary
			if item.Link != "" {
				if msg != "" {
					msg += "\n\n" + item.Link
				} else {
					msg = item.Link
				}
			}
			if item.Comments != "" && item.Comments != item.Link {
				msg += "\nDiscussion: " + item.Comments
			}

			maxPerDay := s.cfg.TechNews.MaxPerDay
			if maxPerDay <= 0 {
				maxPerDay = 3
			}

			deliveredAny := false
			for _, tid := range threads {
				countToday, err := s.dbCountTechDeliveriesToday(ctx, tid)
				if err != nil {
					log.Printf("tech-news: count deliveries for %s failed: %v", tid, err)
				} else if countToday >= maxPerDay {
					log.Printf("tech-news: daily cap (%d/%d) reached for thread %s today, skipping %q", countToday, maxPerDay, tid, item.Title)
					continue
				}

				if err := s.postWebhook(ctx, s.cfg.TechNews.WebhookURL, "tech-news", tid,
					title, msg, item.Link, item.GUID); err != nil {
					log.Printf("tech-news: delivery to %s failed: %v", tid, err)
					time.Sleep(300 * time.Millisecond)
					continue
				}

				if err := s.dbRecordTechDelivery(ctx, tid, item.GUID); err != nil {
					log.Printf("tech-news: record delivery to %s failed: %v", tid, err)
				}
				deliveredAny = true
				time.Sleep(300 * time.Millisecond)
			}

			// Always mark seen in database so items past the daily cap don't accumulate into a backlog
			_ = s.dbInsertSeen(ctx, "public.tech_seen", "guid", map[string]string{
				"guid":   item.GUID,
				"source": feedName,
				"title":  item.Title,
			})
			_ = deliveredAny
		}
	}
	return nil
}
