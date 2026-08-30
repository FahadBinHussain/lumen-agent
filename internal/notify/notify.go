package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Config holds the notify pollers' settings. All default to disabled; enable
// per-poller during cutover when lumen takes over a feed.
type Config struct {
	WebhookURL    string          `yaml:"webhook_url"`
	WebhookToken  string          `yaml:"webhook_token"`
	WebhookTokenEnv string        `yaml:"webhook_token_env"`
	DatabaseURL   string          `yaml:"database_url"`
	DatabaseURLEnv string         `yaml:"database_url_env"`
	SteamUpdates  SteamUpdatesCfg  `yaml:"steam_updates"`
	FreeGames     FreeGamesCfg     `yaml:"free_games"`
	NeonUsage     NeonUsageCfg     `yaml:"neon_usage"`
	Supabase      SupabaseCfg      `yaml:"supabase"`
	CrackWatch    CrackWatchCfg    `yaml:"crack_watch"`
}

type SteamUpdatesCfg struct {
	Enabled     bool   `yaml:"enabled"`
	Interval    string `yaml:"interval"`
	AppIDs      string `yaml:"app_ids"`
	ThreadIDs   string `yaml:"thread_ids"`
	MaxAgeDays  int    `yaml:"max_age_days"`
	WebhookURL  string `yaml:"webhook_url"`
}

type FreeGamesCfg struct {
	Enabled    bool   `yaml:"enabled"`
	Interval   string `yaml:"interval"`
	ThreadIDs  string `yaml:"thread_ids"`
	WebhookURL string `yaml:"webhook_url"`
}

type CrackWatchCfg struct {
	Enabled    bool   `yaml:"enabled"`
	Interval   string `yaml:"interval"`
	FeedURL    string `yaml:"feed_url"`
	ThreadIDs  string `yaml:"thread_ids"`
	WebhookURL string `yaml:"webhook_url"`
}

type NeonUsageCfg struct {
	Enabled           bool          `yaml:"enabled"`
	Interval          string        `yaml:"interval"`
	WarningHours      float64       `yaml:"warning_hours"`
	WarningStoragePct float64       `yaml:"warning_storage_pct"`
	WarningEgressPct  float64       `yaml:"warning_egress_pct"`
	ThreadID          string        `yaml:"thread_id"`
	APIKeyEnv         []string      `yaml:"api_key_env"`
	StatePath         string        `yaml:"state_path"`
	Export            NeonExportCfg `yaml:"export"`
}

// NeonExportCfg mirrors config.NotifyNeonExportCfg: encrypted pg_dump exports
// of over-threshold orgs to a dedicated repo branch, force-pushed each poll.
type NeonExportCfg struct {
	Enabled        bool   `yaml:"enabled"`
	Repo           string `yaml:"repo"`
	Branch         string `yaml:"branch"`
	Path           string `yaml:"path"`
	KeyEnv         string `yaml:"key_env"`
	GitHubTokenEnv string `yaml:"github_token_env"`
	ExportTimeout  string `yaml:"export_timeout"`
	ExportInterval string `yaml:"export_interval"`
}

// SupabaseCfg watches supabase project quotas. tokens live in lumen's own Neon
// app_state table (see supabase.go), NOT env vars.
type SupabaseCfg struct {
	Enabled       bool     `yaml:"enabled"`
	Interval      string   `yaml:"interval"`
	ThreadID      string   `yaml:"thread_id"`
	AppStateTable string   `yaml:"app_state_table"`
	ProjectRefs   []string `yaml:"project_refs"`
	EgressThreshold float64 `yaml:"egress_threshold"`
	DBThreshold     float64 `yaml:"db_threshold"`
	AppStateDatabaseURL    string `yaml:"app_state_database_url"`
	AppStateDatabaseURLEnv string `yaml:"app_state_database_url_env"`
	StatePath              string `yaml:"state_path"`
}

// Service runs the ported Vercel pollers (steam-updates, free-games) and the
// neon usage warning check. Faithful ports of
//   - murmur/vercel/app/api/steam-updates/route.ts
//   - murmur/vercel/app/api/free-games/route.ts
//   - murmur/scripts/murmur.ps1 Check-NeonUsage + mainframe/neon-hours-table.ps1
// All gated off by default (copy-only until cutover).
type Service struct {
	cfg       Config
	client    *http.Client
	mu        sync.Mutex
	db        *dedupeDB
	appState  *appStateDB
	neonState map[string]string // orgId -> period reset yyyy-MM-dd (state file persisted)
	statePath string
	supabaseState map[string]string // dedupeKey -> date (supabase watcher)
	supabaseStatePath string
	lastExport time.Time // when the last neon export batch was pushed (export_interval throttle)
	toucher   func()    // persist callback; fires after state file writes so Neon snapshot syncs promptly
}

func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.SteamUpdates.Interval == "" {
		cfg.SteamUpdates.Interval = "1m"
	}
	if cfg.FreeGames.Interval == "" {
		cfg.FreeGames.Interval = "1m"
	}
	if cfg.CrackWatch.Interval == "" {
		cfg.CrackWatch.Interval = "5m"
	}
	if cfg.CrackWatch.FeedURL == "" {
		cfg.CrackWatch.FeedURL = "https://www.reddit.com/r/CrackWatch/.rss"
	}
	if cfg.NeonUsage.Interval == "" {
		cfg.NeonUsage.Interval = "1h"
	}
	if cfg.NeonUsage.WarningHours <= 0 {
		cfg.NeonUsage.WarningHours = 90
	}
	if cfg.NeonUsage.WarningStoragePct <= 0 {
		cfg.NeonUsage.WarningStoragePct = 80
	}
	if cfg.NeonUsage.WarningEgressPct <= 0 {
		cfg.NeonUsage.WarningEgressPct = 80
	}
	if cfg.NeonUsage.Export.Repo == "" {
		cfg.NeonUsage.Export.Repo = "FahadBinHussain/lumen-agent"
	}
	if cfg.NeonUsage.Export.Branch == "" {
		cfg.NeonUsage.Export.Branch = "exports"
	}
	if cfg.NeonUsage.Export.Path == "" {
		cfg.NeonUsage.Export.Path = "backups"
	}
	if cfg.NeonUsage.Export.KeyEnv == "" {
		cfg.NeonUsage.Export.KeyEnv = "LUMEN_EXPORT_KEY"
	}
	if cfg.NeonUsage.Export.GitHubTokenEnv == "" {
		cfg.NeonUsage.Export.GitHubTokenEnv = "LUMEN_EXPORT_GITHUB_TOKEN"
	}
	if cfg.NeonUsage.Export.ExportTimeout == "" {
		cfg.NeonUsage.Export.ExportTimeout = "60s"
	}
	if cfg.NeonUsage.Export.ExportInterval == "" {
		cfg.NeonUsage.Export.ExportInterval = "24h"
	}
	if cfg.Supabase.Interval == "" {
		cfg.Supabase.Interval = "6h"
	}
	if cfg.Supabase.AppStateTable == "" {
		cfg.Supabase.AppStateTable = "public.app_state"
	}
	if cfg.Supabase.EgressThreshold <= 0 {
		cfg.Supabase.EgressThreshold = 0.8
	}
	if cfg.Supabase.DBThreshold <= 0 {
		cfg.Supabase.DBThreshold = 0.8
	}
	if cfg.SteamUpdates.MaxAgeDays <= 0 {
		cfg.SteamUpdates.MaxAgeDays = 30
	}
	if cfg.WebhookToken == "" && cfg.WebhookTokenEnv != "" {
		cfg.WebhookToken = strings.TrimSpace(os.Getenv(cfg.WebhookTokenEnv))
	}
	if cfg.DatabaseURL == "" && cfg.DatabaseURLEnv != "" {
		cfg.DatabaseURL = strings.TrimSpace(os.Getenv(cfg.DatabaseURLEnv))
	}
	s := &Service{
		cfg:       cfg,
		client:    &http.Client{Timeout: 60 * time.Second},
		neonState: map[string]string{},
		statePath: cfg.NeonUsage.StatePath,
		supabaseState: map[string]string{},
		supabaseStatePath: cfg.Supabase.StatePath,
	}
	if (cfg.SteamUpdates.Enabled || cfg.FreeGames.Enabled || cfg.CrackWatch.Enabled) && cfg.DatabaseURL != "" {
		db, err := newDedupeDB(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Printf("notify: dedupe db unavailable (%v), steam/free-games/crack_watch will run without dedupe", err)
		} else {
			s.db = db
		}
	}
	if cfg.Supabase.Enabled {
		appDSN := cfg.Supabase.AppStateDatabaseURL
		if appDSN == "" && cfg.Supabase.AppStateDatabaseURLEnv != "" {
			appDSN = strings.TrimSpace(os.Getenv(cfg.Supabase.AppStateDatabaseURLEnv))
		}
		if appDSN == "" {
			appDSN = cfg.DatabaseURL
		}
		if appDSN != "" {
			appDB, err := newAppStateDB(ctx, appDSN, cfg.Supabase.AppStateTable)
			if err != nil {
				return nil, fmt.Errorf("notify app_state db: %w", err)
			}
			s.appState = appDB
		}
	}
	return s, nil
}

func (s *Service) Close() {
	if s.db != nil {
		s.db.Close()
	}
	if s.appState != nil {
		s.appState.Close()
	}
}

// SetPersistenceToucher registers a callback fired after state-file writes so
// the Neon snapshot backup (internal/persist) syncs promptly — mirrors the
// bridge/discord persistence contract. Without it, neon-usage/supabase dedupe
// state stays local and a container restart re-fires warnings.
func (s *Service) SetPersistenceToucher(fn func()) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	s.toucher = fn
	s.mu.Unlock()
}

// touchPersistence notifies the persist store (best-effort) after a state
// write so the dedupe state survives a container restart.
func (s *Service) touchPersistence() {
	if s.toucher == nil {
		return
	}
	s.toucher()
}

// Run blocks until ctx is done, starting each enabled poller.
func (s *Service) Run(ctx context.Context) error {
	type job struct {
		name string
		run  func(context.Context) error
		iv   time.Duration
	}
	var jobs []job

	if s.cfg.SteamUpdates.Enabled {
		iv := parseInterval(s.cfg.SteamUpdates.Interval, time.Minute)
		jobs = append(jobs, job{"steam-updates", s.pollSteamUpdates, iv})
	}
	if s.cfg.FreeGames.Enabled {
		iv := parseInterval(s.cfg.FreeGames.Interval, time.Minute)
		jobs = append(jobs, job{"free-games", s.pollFreeGames, iv})
	}
	if s.cfg.NeonUsage.Enabled {
		iv := parseInterval(s.cfg.NeonUsage.Interval, time.Hour)
		jobs = append(jobs, job{"neon-usage", s.checkNeonUsage, iv})
	}
	if s.cfg.Supabase.Enabled {
		iv := parseInterval(s.cfg.Supabase.Interval, time.Hour)
		jobs = append(jobs, job{"supabase", s.checkSupabase, iv})
	}
	if s.cfg.CrackWatch.Enabled {
		iv := parseInterval(s.cfg.CrackWatch.Interval, 5*time.Minute)
		jobs = append(jobs, job{"crack-watch", s.pollCrackWatch, iv})
	}

	if len(jobs) == 0 {
		log.Printf("notify: no pollers enabled, idling")
		<-ctx.Done()
		return nil
	}

	for _, j := range jobs {
		j := j
		go func() {
			ticker := time.NewTicker(j.iv)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := j.run(ctx); err != nil {
						log.Printf("notify: %s: %v", j.name, err)
					}
				}
			}
		}()
	}

	<-ctx.Done()
	return nil
}

// postWebhook mirrors the Vercel pollers' webhook POST (notifications endpoint).
// Per-poll webhook_url overrides the global one (default: murmur space URL).
func (s *Service) postWebhook(ctx context.Context, url, source, threadID, title, message, link, dedupeKey string) error {
	if url == "" {
		url = s.cfg.WebhookURL
	}
	body := map[string]string{
		"source":    source,
		"threadId":  threadID,
		"title":     title,
		"message":   message,
		"url":       link,
		"dedupeKey": dedupeKey,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if hf := s.cfg.WebhookToken; hf != "" {
		req.Header.Set("X-HF-Authorization", "Bearer "+hf)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook http %d", resp.StatusCode)
	}
	return nil
}

func parseInterval(v string, fallback time.Duration) time.Duration {
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func stripHTML(html string) string {
	out := make([]byte, 0, len(html))
	inTag := false
	for i := 0; i < len(html); i++ {
		c := html[i]
		switch {
		case inTag:
			if c == '>' {
				inTag = false
			}
		case c == '<':
			inTag = true
		default:
			out = append(out, c)
		}
	}
	return strings.TrimSpace(string(out))
}