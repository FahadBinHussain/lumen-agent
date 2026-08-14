package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// pollSteamUpdates is a faithful Go port of
// murmur/vercel/app/api/steam-updates/route.ts: polls Steam GetNewsForApp per
// appid in AppIDs ("appid:Display Name" comma list), keeps only Community
// Announcements, dedupes against Neon steam_seen, POSTs new ones to the
// webhook with dedupeKey = gid.
func (s *Service) pollSteamUpdates(ctx context.Context) error {
	appSpecs := splitList(s.cfg.SteamUpdates.AppIDs, "3527290:PEAK")
	threads := splitList(s.cfg.SteamUpdates.ThreadIDs, "30738305889116993,2637078310061988")
	if len(threads) == 0 {
		return fmt.Errorf("no threads configured")
	}

	maxAge := time.Duration(s.cfg.SteamUpdates.MaxAgeDays) * 24 * time.Hour

	type steamItem struct {
		gid      string
		appid    string
		gameName string
		title    string
		dateMS   int64
		contents string
	}
	var items []steamItem

	for _, spec := range appSpecs {
		appid, name := spec, ""
		if i := strings.Index(spec, ":"); i >= 0 {
			appid = spec[:i]
			name = strings.TrimSpace(spec[i+1:])
		}
		gameName := name
		if gameName == "" {
			gameName = "app " + appid
		}

		url := fmt.Sprintf("https://api.steampowered.com/ISteamNews/GetNewsForApp/v2/?appid=%s&count=30&maxlength=800&format=json", appid)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			continue
		}
		resp, err := s.client.Do(req)
		if err != nil {
			continue
		}
		var payload struct {
			Appnews struct {
				Newsitems []struct {
					GID        string `json:"gid"`
					Title      string `json:"title"`
					Feedlabel  string `json:"feedlabel"`
					Date       int64  `json:"date"`
					Contents   string `json:"contents"`
				} `json:"newsitems"`
			} `json:"appnews"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if decodeErr != nil {
			continue
		}

		for _, n := range payload.Appnews.Newsitems {
			if n.Feedlabel != "Community Announcements" {
				continue
			}
			title := strings.TrimSpace(n.Title)
			if title == "" || strings.TrimSpace(n.GID) == "" {
				continue
			}
			dateMS := n.Date * 1000
			if dateMS > 0 && time.Since(time.UnixMilli(dateMS)) > maxAge {
				continue
			}
			items = append(items, steamItem{
				gid:      strings.TrimSpace(n.GID),
				appid:    appid,
				gameName: gameName,
				title:    title,
				dateMS:   dateMS,
				contents: stripHTML(n.Contents),
			})
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].dateMS < items[j].dateMS })
	if len(items) == 0 {
		return nil
	}

	dbr, err := s.dbQuery(ctx, "steam_seen", "gid", func() []string {
		gids := make([]string, 0, len(items))
		for _, it := range items {
			gids = append(gids, it.gid)
		}
		return gids
	})
	if err != nil {
		return fmt.Errorf("steam_seen query: %w", err)
	}

	for _, item := range items {
		if dbr.seen[item.gid] {
			continue
		}
		dateLine := ""
		if item.dateMS > 0 {
			dateLine = fmt.Sprintf("\n\n(%s)", time.UnixMilli(item.dateMS).UTC().Format("2006-01-02"))
		}
		contentLine := ""
		if item.contents != "" {
			if len(item.contents) > 600 {
				contentLine = "\n\n" + item.contents[:600]
			} else {
				contentLine = "\n\n" + item.contents
			}
		}
		link := fmt.Sprintf("https://steamcommunity.com/games/%s/announcements/detail/%s", item.appid, item.gid)
		linkLine := "\n\n" + link

		ok := true
		for _, tid := range threads {
			if err := s.postWebhook(ctx, s.cfg.SteamUpdates.WebhookURL, "steam-updates", tid,
				fmt.Sprintf("🆕 %s: %s", item.gameName, item.title),
				contentLine+dateLine+linkLine, link, item.gid); err != nil {
				ok = false
				time.Sleep(500 * time.Millisecond)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !ok {
			continue
		}
		if err := s.dbInsertSeen(ctx, "steam_seen", "gid", map[string]string{
			"gid":       item.gid,
			"game_name": item.gameName,
			"title":     item.title,
		}); err != nil {
			continue
		}
	}
	return nil
}

func splitList(v, fallback string) []string {
	if strings.TrimSpace(v) == "" {
		v = fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}