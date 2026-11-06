package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// pollFreeGames polls free game giveaways:
// 1. Directly from the Epic Games Store promotions API (official metadata, 0 lag).
// 2. From the lootscraper Atom feed for Steam, GOG, Humble, itch.io, etc.
// Dedupes against Neon public.game_seen (by guid and title), POSTs new ones as
// source "gamebot" with dedupeKey = guid.
func (s *Service) pollFreeGames(ctx context.Context) error {
	threads := splitList(s.cfg.FreeGames.ThreadIDs, "")
	if len(threads) == 0 {
		return fmt.Errorf("no threads configured")
	}

	var allItems []feedItem

	// 1. Direct Epic Games Store promotions
	epicItems, err := s.fetchEpicFreeGames(ctx)
	if err != nil {
		log.Printf("notify: epic free games fetch error: %v", err)
	} else {
		allItems = append(allItems, epicItems...)
	}

	// 2. Aggregator feed for other platforms (Steam, GOG, etc.)
	feedURL := s.cfg.FreeGames.FeedURL
	if feedURL == "" {
		feedURL = "https://feed.eikowagenknecht.com/lootscraper.xml"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		log.Printf("notify: free games feed request error: %v", err)
	} else {
		resp, err := s.client.Do(req)
		if err != nil {
			log.Printf("notify: free games feed fetch error: %v", err)
		} else {
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				log.Printf("notify: free games feed http %d", resp.StatusCode)
			} else {
				xmlData := readAll(resp.Body)
				feedItems := parseFeedEntries(xmlData)
				allItems = append(allItems, feedItems...)
			}
		}
	}

	if len(allItems) == 0 {
		return nil
	}

	dbr, err := s.dbQuery(ctx, "public.game_seen", "guid", func() []string {
		guids := make([]string, 0, len(allItems))
		for _, it := range allItems {
			guids = append(guids, it.GUID)
		}
		return guids
	})
	if err != nil {
		return fmt.Errorf("game_seen query: %w", err)
	}

	titleSeen, _ := s.dbGameTitles(ctx)

	for _, item := range allItems {
		titleKey := normalizeGameTitle(item.Title)
		if dbr.seen[item.GUID] || (titleSeen != nil && titleSeen[titleKey]) {
			if !dbr.seen[item.GUID] {
				_ = s.dbInsertSeen(ctx, "public.game_seen", "guid", map[string]string{
					"guid":  item.GUID,
					"title": item.Title,
				})
			}
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
		if titleSeen != nil {
			titleSeen[titleKey] = true
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

func normalizeGameTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	if idx := strings.Index(title, " - "); idx != -1 {
		title = title[idx+3:]
	}
	return strings.Join(strings.Fields(title), " ")
}

// fetchEpicFreeGames calls Epic's official promotions endpoint directly.
func (s *Service) fetchEpicFreeGames(ctx context.Context) ([]feedItem, error) {
	apiURL := s.cfg.FreeGames.EpicPromoURL
	if apiURL == "" {
		apiURL = "https://store-site-backend-static-ipv4.ak.epicgames.com/freeGamesPromotions?locale=en-US&country=US&allowCountries=US"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("epic request create: %w", err)
	}
	req.Header.Set("User-Agent", "lumen-agent/1.0 (+https://github.com/FahadBinHussain/lumen-agent)")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("epic request do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("epic http %d", resp.StatusCode)
	}

	data := readAll(resp.Body)
	return parseEpicPromotions([]byte(data), time.Now().UTC()), nil
}

type epicSearchStoreResponse struct {
	Data struct {
		Catalog struct {
			SearchStore struct {
				Elements []epicElement `json:"elements"`
			} `json:"searchStore"`
		} `json:"Catalog"`
	} `json:"data"`
}

type epicElement struct {
	ID            string             `json:"id"`
	Title         string             `json:"title"`
	Description   string             `json:"description"`
	ProductSlug   string             `json:"productSlug"`
	URLSlug       string             `json:"urlSlug"`
	OfferMappings []epicOfferMapping `json:"offerMappings"`
	Price         epicPrice          `json:"price"`
	Promotions    *epicPromotions    `json:"promotions"`
}

type epicOfferMapping struct {
	PageSlug string `json:"pageSlug"`
	PageType string `json:"pageType"`
}

type epicPrice struct {
	TotalPrice epicTotalPrice `json:"totalPrice"`
}

type epicTotalPrice struct {
	DiscountPrice int `json:"discountPrice"`
	OriginalPrice int `json:"originalPrice"`
}

type epicPromotions struct {
	PromotionalOffers []epicPromoOfferGroup `json:"promotionalOffers"`
}

type epicPromoOfferGroup struct {
	PromotionalOffers []epicPromoOffer `json:"promotionalOffers"`
}

type epicPromoOffer struct {
	StartDate       string              `json:"startDate"`
	EndDate         string              `json:"endDate"`
	DiscountSetting epicDiscountSetting `json:"discountSetting"`
}

type epicDiscountSetting struct {
	DiscountType       string `json:"discountType"`
	DiscountPercentage int    `json:"discountPercentage"`
}

func parseEpicPromotions(jsonData []byte, now time.Time) []feedItem {
	var resp epicSearchStoreResponse
	if err := json.Unmarshal(jsonData, &resp); err != nil {
		return nil
	}

	var items []feedItem
	for _, el := range resp.Data.Catalog.SearchStore.Elements {
		if strings.TrimSpace(el.Title) == "" || el.Price.TotalPrice.DiscountPrice != 0 {
			continue
		}
		if el.Promotions == nil || len(el.Promotions.PromotionalOffers) == 0 {
			continue
		}

		var (
			isFree  bool
			endDate time.Time
		)
		for _, group := range el.Promotions.PromotionalOffers {
			for _, offer := range group.PromotionalOffers {
				start, err1 := time.Parse(time.RFC3339, offer.StartDate)
				end, err2 := time.Parse(time.RFC3339, offer.EndDate)
				if err1 != nil || err2 != nil {
					continue
				}
				if !now.Before(start) && now.Before(end) {
					isFree = true
					endDate = end
					break
				}
			}
			if isFree {
				break
			}
		}

		if !isFree {
			continue
		}

		slug := ""
		for _, m := range el.OfferMappings {
			if m.PageSlug != "" {
				if m.PageType == "productHome" || slug == "" {
					slug = m.PageSlug
					if m.PageType == "productHome" {
						break
					}
				}
			}
		}
		if slug == "" {
			slug = el.ProductSlug
		}
		if slug == "" {
			slug = el.URLSlug
		}

		link := "https://store.epicgames.com/"
		if slug != "" {
			link = "https://store.epicgames.com/p/" + slug
		}

		guid := "epic:" + el.ID
		if el.ID == "" {
			guid = link
		}

		content := strings.TrimSpace(el.Description)
		if !endDate.IsZero() {
			endStr := endDate.UTC().Format("Jan 02, 2006 15:04 UTC")
			if content != "" {
				content += fmt.Sprintf("\n\nFree until %s", endStr)
			} else {
				content = fmt.Sprintf("Free until %s", endStr)
			}
		}

		items = append(items, feedItem{
			GUID:    guid,
			Title:   el.Title,
			Link:    link,
			Content: content,
			Source:  "Epic Games",
		})
	}

	return items
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
		// Skip Epic Games items from lootscraper: we poll Epic Games Store API directly
		// with authoritative 0-cost verification and direct store links.
		if strings.Contains(strings.ToLower(source), "epic") {
			continue
		}
		if u, err := url.Parse(getLink); err == nil {
			host := strings.TrimPrefix(u.Hostname(), "www.")
			if host == "store.epicgames.com" {
				goto blocked
			}
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
