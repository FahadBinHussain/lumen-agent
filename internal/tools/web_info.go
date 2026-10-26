package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	webClient = &http.Client{
		Timeout: 10 * time.Second,
	}

	scriptStyleRegex = regexp.MustCompile(`(?is)<(script|style|noscript|svg)[^>]*>.*?</(script|style|noscript|svg)>`)
	tagRegex         = regexp.MustCompile(`<[^>]+>`)
	lineBreakRegex   = regexp.MustCompile(`(?i)<(br|p|div|li|h[1-6]|tr|article|section)[^>]*>`)
	spaceRegex       = regexp.MustCompile(`[ \t]+`)
	titleRegex       = regexp.MustCompile(`(?i)<title[^>]*>(.*?)</title>`)
	metaDescRegex    = regexp.MustCompile(`(?i)<meta\s+[^>]*name=["']description["'][^>]*content=["']([^"']*)["']`)
)

func (r *Registry) registerWebInfoTools() {
	r.register(
		"get_weather",
		"Look up current weather and a short forecast for a city or place using a free weather API.",
		objectSchema(map[string]any{
			"location": stringSchema("City or place name to look up."),
			"days":     integerSchema("Optional number of forecast days to return, up to 5.", 1),
		}, "location"),
		r.handleGetWeather,
	)

	r.register(
		"search_web",
		"Search the web for current information, technical projects, topics, and documentation.",
		objectSchema(map[string]any{
			"query": stringSchema("Search query."),
		}, "query"),
		r.handleSearchWeb,
	)

	r.register(
		"search_news",
		"Search recent tech and startup news headlines using the free Hacker News Algolia API.",
		objectSchema(map[string]any{
			"query": stringSchema("News topic or keyword."),
			"limit": integerSchema("Optional number of results to return, up to 10.", 1),
		}, "query"),
		r.handleSearchNews,
	)

	r.register(
		"fetch_web_page",
		"Fetch and read the text content of a public web page or URL.",
		objectSchema(map[string]any{
			"url": stringSchema("The full HTTP or HTTPS URL to fetch."),
		}, "url"),
		r.handleFetchWebPage,
	)
}

func (r *Registry) handleGetWeather(ctx context.Context, payload json.RawMessage) (string, error) {
	type args struct {
		Location string `json:"location"`
		Days     int    `json:"days"`
	}
	var input args
	if err := decodeArgs(payload, &input); err != nil {
		return "", err
	}

	location := strings.TrimSpace(input.Location)
	if location == "" {
		return "", fmt.Errorf("location must not be empty")
	}

	forecastDays := input.Days
	if forecastDays <= 0 {
		forecastDays = 3
	}
	if forecastDays > 5 {
		forecastDays = 5
	}

	geoURL := "https://geocoding-api.open-meteo.com/v1/search?count=1&language=en&format=json&name=" + url.QueryEscape(location)
	geoReq, err := http.NewRequestWithContext(ctx, http.MethodGet, geoURL, nil)
	if err != nil {
		return "", fmt.Errorf("build weather geocoding request: %w", err)
	}

	geoResp, err := webClient.Do(geoReq)
	if err != nil {
		return "", fmt.Errorf("geocode weather location: %w", err)
	}
	defer geoResp.Body.Close()

	if geoResp.StatusCode < 200 || geoResp.StatusCode >= 300 {
		return "", fmt.Errorf("geocode weather location: API error (%d)", geoResp.StatusCode)
	}

	var geocoded struct {
		Results []struct {
			Name      string  `json:"name"`
			Country   string  `json:"country"`
			Admin1    string  `json:"admin1"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Timezone  string  `json:"timezone"`
		} `json:"results"`
	}
	if err := json.NewDecoder(geoResp.Body).Decode(&geocoded); err != nil {
		return "", fmt.Errorf("decode weather geocoding response: %w", err)
	}
	if len(geocoded.Results) == 0 {
		return "", fmt.Errorf("no weather match found for %q", location)
	}

	match := geocoded.Results[0]
	forecastURL := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%s&longitude=%s&current=temperature_2m,apparent_temperature,weather_code,wind_speed_10m&daily=weather_code,temperature_2m_max,temperature_2m_min&forecast_days=%d&timezone=auto",
		strconv.FormatFloat(match.Latitude, 'f', 4, 64),
		strconv.FormatFloat(match.Longitude, 'f', 4, 64),
		forecastDays,
	)
	forecastReq, err := http.NewRequestWithContext(ctx, http.MethodGet, forecastURL, nil)
	if err != nil {
		return "", fmt.Errorf("build weather forecast request: %w", err)
	}

	forecastResp, err := webClient.Do(forecastReq)
	if err != nil {
		return "", fmt.Errorf("fetch weather forecast: %w", err)
	}
	defer forecastResp.Body.Close()

	if forecastResp.StatusCode < 200 || forecastResp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch weather forecast: API error (%d)", forecastResp.StatusCode)
	}

	var forecast struct {
		Current struct {
			Temperature2M       float64 `json:"temperature_2m"`
			ApparentTemperature float64 `json:"apparent_temperature"`
			WeatherCode         int     `json:"weather_code"`
			WindSpeed10M        float64 `json:"wind_speed_10m"`
		} `json:"current"`
		Daily struct {
			Time             []string  `json:"time"`
			WeatherCode      []int     `json:"weather_code"`
			TemperatureMax2M []float64 `json:"temperature_2m_max"`
			TemperatureMin2M []float64 `json:"temperature_2m_min"`
		} `json:"daily"`
	}
	if err := json.NewDecoder(forecastResp.Body).Decode(&forecast); err != nil {
		return "", fmt.Errorf("decode weather forecast response: %w", err)
	}

	days := make([]map[string]any, 0, len(forecast.Daily.Time))
	for i := range forecast.Daily.Time {
		item := map[string]any{
			"date":        forecast.Daily.Time[i],
			"description": weatherCodeDescription(valueAtInt(forecast.Daily.WeatherCode, i)),
			"temp_max_c":  valueAtFloat(forecast.Daily.TemperatureMax2M, i),
			"temp_min_c":  valueAtFloat(forecast.Daily.TemperatureMin2M, i),
		}
		days = append(days, item)
	}

	label := strings.TrimSpace(match.Name)
	if strings.TrimSpace(match.Admin1) != "" {
		label += ", " + strings.TrimSpace(match.Admin1)
	}
	if strings.TrimSpace(match.Country) != "" {
		label += ", " + strings.TrimSpace(match.Country)
	}

	return jsonResult(map[string]any{
		"location": label,
		"timezone": strings.TrimSpace(match.Timezone),
		"current": map[string]any{
			"temperature_c":  forecast.Current.Temperature2M,
			"feels_like_c":   forecast.Current.ApparentTemperature,
			"wind_speed_kmh": forecast.Current.WindSpeed10M,
			"description":    weatherCodeDescription(forecast.Current.WeatherCode),
			"weather_code":   forecast.Current.WeatherCode,
		},
		"forecast": days,
	})
}

func (r *Registry) handleSearchWeb(ctx context.Context, payload json.RawMessage) (string, error) {
	type args struct {
		Query string `json:"query"`
	}
	var input args
	if err := decodeArgs(payload, &input); err != nil {
		return "", err
	}

	query := strings.TrimSpace(input.Query)
	if query == "" {
		return "", fmt.Errorf("query must not be empty")
	}

	braveKey := os.Getenv("BRAVE_SEARCH_API_KEY")
	if braveKey == "" {
		braveKey = os.Getenv("BRAVE_API_KEY")
	}
	if braveKey != "" {
		return searchBrave(ctx, query, braveKey)
	}

	tavilyKey := os.Getenv("TAVILY_API_KEY")
	if tavilyKey != "" {
		return searchTavily(ctx, query, tavilyKey)
	}

	return searchBuiltIn(ctx, query)
}

func searchBrave(ctx context.Context, query, apiKey string) (string, error) {
	searchURL := "https://api.search.brave.com/res/v1/web/search?count=8&q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("build Brave search request: %w", err)
	}
	req.Header.Set("X-Subscription-Token", apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := webClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("brave search %q: %w", query, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("brave search %q: API error (%d)", query, resp.StatusCode)
	}

	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode brave search response: %w", err)
	}

	results := make([]map[string]any, 0, len(parsed.Web.Results))
	for _, item := range parsed.Web.Results {
		results = append(results, map[string]any{
			"title":       strings.TrimSpace(item.Title),
			"url":         strings.TrimSpace(item.URL),
			"description": strings.TrimSpace(item.Description),
		})
	}
	return jsonResult(map[string]any{
		"engine":  "brave",
		"query":   query,
		"results": results,
	})
}

func searchTavily(ctx context.Context, query, apiKey string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"query":       query,
		"max_results": 8,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build Tavily search request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := webClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tavily search %q: %w", query, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("tavily search %q: API error (%d)", query, resp.StatusCode)
	}

	var parsed struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode tavily search response: %w", err)
	}

	results := make([]map[string]any, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		results = append(results, map[string]any{
			"title":       strings.TrimSpace(item.Title),
			"url":         strings.TrimSpace(item.URL),
			"description": strings.TrimSpace(item.Content),
		})
	}
	return jsonResult(map[string]any{
		"engine":  "tavily",
		"query":   query,
		"answer":  strings.TrimSpace(parsed.Answer),
		"results": results,
	})
}

func searchBuiltIn(ctx context.Context, query string) (string, error) {
	results := make([]map[string]any, 0, 10)

	// Hacker News Algolia API (covers developer projects, startups, tools, and technical discussions)
	hnURL := "https://hn.algolia.com/api/v1/search?tags=story&hitsPerPage=6&query=" + url.QueryEscape(query)
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, hnURL, nil); err == nil {
		if resp, err := webClient.Do(req); err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			var parsed struct {
				Hits []struct {
					Title     string `json:"title"`
					URL       string `json:"url"`
					StoryText string `json:"story_text"`
					Points    int    `json:"points"`
					ObjectID  string `json:"objectID"`
				} `json:"hits"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&parsed); err == nil {
				for _, h := range parsed.Hits {
					targetURL := strings.TrimSpace(h.URL)
					if targetURL == "" && h.ObjectID != "" {
						targetURL = "https://news.ycombinator.com/item?id=" + h.ObjectID
					}
					results = append(results, map[string]any{
						"source":      "hacker_news",
						"title":       strings.TrimSpace(h.Title),
						"url":         targetURL,
						"description": strings.TrimSpace(h.StoryText),
						"points":      h.Points,
					})
				}
			}
		}
	}

	// Wikipedia opensearch API (definitions and encyclopedic articles)
	wikiURL := "https://en.wikipedia.org/w/api.php?action=opensearch&limit=4&namespace=0&format=json&search=" + url.QueryEscape(query)
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, wikiURL, nil); err == nil {
		req.Header.Set("User-Agent", "lumen-agent/1.0 (https://github.com/FahadBinHussain/lumen-agent)")
		if resp, err := webClient.Do(req); err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			var wikiRaw []any
			if err := json.NewDecoder(resp.Body).Decode(&wikiRaw); err == nil && len(wikiRaw) >= 4 {
				titles, _ := wikiRaw[1].([]any)
				descs, _ := wikiRaw[2].([]any)
				urls, _ := wikiRaw[3].([]any)
				for i := range titles {
					tStr, _ := titles[i].(string)
					dStr := ""
					if i < len(descs) {
						dStr, _ = descs[i].(string)
					}
					uStr := ""
					if i < len(urls) {
						uStr, _ = urls[i].(string)
					}
					if tStr != "" && uStr != "" {
						results = append(results, map[string]any{
							"source":      "wikipedia",
							"title":       tStr,
							"url":         uStr,
							"description": dStr,
						})
					}
				}
			}
		}
	}

	return jsonResult(map[string]any{
		"engine":  "hn_algolia+wikipedia [tip: set BRAVE_SEARCH_API_KEY or TAVILY_API_KEY for broad web indexing]",
		"query":   query,
		"results": results,
	})
}

func (r *Registry) handleFetchWebPage(ctx context.Context, payload json.RawMessage) (string, error) {
	type args struct {
		URL string `json:"url"`
	}
	var input args
	if err := decodeArgs(payload, &input); err != nil {
		return "", err
	}

	rawURL := strings.TrimSpace(input.URL)
	if rawURL == "" {
		return "", fmt.Errorf("url must not be empty")
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return "", fmt.Errorf("invalid URL %q: must start with http:// or https://", rawURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request for %s: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := webClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch %s: HTTP %d %s", rawURL, resp.StatusCode, resp.Status)
	}

	lr := io.LimitReader(resp.Body, 256*1024)
	bodyBytes, err := io.ReadAll(lr)
	if err != nil {
		return "", fmt.Errorf("read response body from %s: %w", rawURL, err)
	}

	rawHTML := string(bodyBytes)
	title := extractHTMLTitle(rawHTML)
	desc := extractMetaDescription(rawHTML)
	cleaned := htmlToCleanText(rawHTML)

	if len(cleaned) > 6000 {
		cleaned = cleaned[:6000] + "\n... [truncated]"
	}

	return jsonResult(map[string]any{
		"url":         rawURL,
		"title":       title,
		"description": desc,
		"content":     cleaned,
	})
}

func extractHTMLTitle(raw string) string {
	if m := titleRegex.FindStringSubmatch(raw); len(m) > 1 {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}

func extractMetaDescription(raw string) string {
	if m := metaDescRegex.FindStringSubmatch(raw); len(m) > 1 {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}

func htmlToCleanText(raw string) string {
	s := scriptStyleRegex.ReplaceAllString(raw, "")
	s = lineBreakRegex.ReplaceAllString(s, "\n")
	s = tagRegex.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	cleanLines := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(spaceRegex.ReplaceAllString(l, " "))
		if l != "" {
			cleanLines = append(cleanLines, l)
		}
	}
	return strings.Join(cleanLines, "\n")
}

func (r *Registry) handleSearchNews(ctx context.Context, payload json.RawMessage) (string, error) {
	type args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	var input args
	if err := decodeArgs(payload, &input); err != nil {
		return "", err
	}

	query := strings.TrimSpace(input.Query)
	if query == "" {
		return "", fmt.Errorf("query must not be empty")
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}

	searchURL := "https://hn.algolia.com/api/v1/search_by_date?tags=story&hitsPerPage=" + strconv.Itoa(limit) + "&query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("build news search request: %w", err)
	}

	resp, err := webClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search news: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("search news: API error (%d)", resp.StatusCode)
	}

	var parsed struct {
		Hits []struct {
			Title     string `json:"title"`
			URL       string `json:"url"`
			Author    string `json:"author"`
			CreatedAt string `json:"created_at"`
			Points    int    `json:"points"`
			StoryText string `json:"story_text"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode news search response: %w", err)
	}

	results := make([]map[string]any, 0, len(parsed.Hits))
	for _, hit := range parsed.Hits {
		results = append(results, map[string]any{
			"title":      strings.TrimSpace(hit.Title),
			"url":        strings.TrimSpace(hit.URL),
			"author":     strings.TrimSpace(hit.Author),
			"created_at": strings.TrimSpace(hit.CreatedAt),
			"points":     hit.Points,
			"summary":    strings.TrimSpace(hit.StoryText),
		})
	}

	return jsonResult(map[string]any{
		"query":   query,
		"results": results,
	})
}

func valueAtFloat(values []float64, index int) float64 {
	if index < 0 || index >= len(values) {
		return 0
	}
	return values[index]
}

func valueAtInt(values []int, index int) int {
	if index < 0 || index >= len(values) {
		return 0
	}
	return values[index]
}

func weatherCodeDescription(code int) string {
	switch code {
	case 0:
		return "clear sky"
	case 1, 2, 3:
		return "partly cloudy"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 56, 57:
		return "freezing drizzle"
	case 61, 63, 65:
		return "rain"
	case 66, 67:
		return "freezing rain"
	case 71, 73, 75, 77:
		return "snow"
	case 80, 81, 82:
		return "rain showers"
	case 85, 86:
		return "snow showers"
	case 95:
		return "thunderstorm"
	case 96, 99:
		return "thunderstorm with hail"
	default:
		return "unknown"
	}
}
