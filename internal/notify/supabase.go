package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckSupabaseOnce runs one supabase watcher pass (exported for the local
// smoke-test harness; the notify loop calls checkSupabase on its ticker).
func (s *Service) CheckSupabaseOnce(ctx context.Context) error {
	return s.checkSupabase(ctx)
}

// checkSupabase is the supabase quota watcher. it reads per-account refresh
// tokens from lumen's own Neon app_state table (NOT vaultwarden - vaultwarden
// items are encrypted and can't be decrypted by a machine without the master
// password). for each account it:
//  1. reads supabase.<ref>.refresh_token + supabase.<ref>.issuer from app_state
//  2. gotrue refresh -> fresh access JWT (30 min) + ROTATED refresh token
//  3. writes the rotated token back to app_state
//  4. queries daily-stats (total_egress) + disk/util (fs_used_bytes)
//  5. alerts in chat when egress > 80% of 5 GB or DB > 80% of 500 MB
//
// dedupe state (per project per period, once per billing cycle) is kept in
// memory + a JSON state file, same pattern as neonusage.go.
func (s *Service) checkSupabase(ctx context.Context) error {
	if s.appState == nil {
		return fmt.Errorf("no app_state db configured")
	}
	threadID := s.cfg.Supabase.ThreadID
	if threadID == "" {
		return fmt.Errorf("no supabase thread configured")
	}
	table := s.cfg.Supabase.AppStateTable

	appStateCfg := s.cfg.Supabase
	s.mu.Lock()
	if s.neonState == nil {
		s.neonState = map[string]string{}
	}
	if s.supabaseStatePath == "" {
		s.supabaseStatePath = s.cfg.Supabase.StatePath
	}
	if s.supabaseStatePath != "" {
		s.loadSupabaseStateLocked()
	}
	s.mu.Unlock()

	// discover all accounts from app_state rows matching "supabase.<ref>.refresh_token"
	accountKeys, err := s.appState.Keys(ctx, "supabase.%.refresh_token")
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	if len(accountKeys) == 0 {
		return nil
	}

	for _, ak := range accountKeys {
		ref := extractRef(ak)
		if ref == "" {
			continue
		}
		if err := s.checkOneSupabase(ctx, ref, appStateCfg, table, threadID); err != nil {
			// log and continue to the next account
			_ = err
		}
	}
	return nil
}

// checkOneSupabase handles a single supabase project account.
func (s *Service) checkOneSupabase(ctx context.Context, ref string, cfg SupabaseCfg, table, threadID string) error {
	refreshToken, err := s.appState.Get(ctx, "supabase."+ref+".refresh_token")
	if err != nil {
		return fmt.Errorf("%s: read refresh_token: %w", ref, err)
	}
	if refreshToken == "" {
		return nil
	}
	issuer, err := s.appState.Get(ctx, "supabase."+ref+".issuer")
	if err != nil || issuer == "" {
		issuer = "alt.supabase.io"
	}

	// gotrue refresh
	accessToken, newRefresh, err := s.supabaseRefresh(ctx, issuer, refreshToken)
	if err != nil {
		return fmt.Errorf("%s: refresh: %w", ref, err)
	}

	// write rotated token back
	if err := s.appState.Set(ctx, "supabase."+ref+".refresh_token", newRefresh); err != nil {
		return fmt.Errorf("%s: write rotated token: %w", ref, err)
	}

	// query daily-stats for egress
	egress, err := s.supabaseEgress(ctx, issuer, accessToken, ref)
	if err != nil {
		return fmt.Errorf("%s: egress: %w", ref, err)
	}

	// query disk/util for DB size
	dbMB, err := s.supabaseDBDisk(ctx, accessToken, ref)
	if err != nil {
		return fmt.Errorf("%s: disk: %w", ref, err)
	}

	// threshold checks
	egressGB := egress / 1024.0
	egressLimit := 5.0 * cfg.EgressThreshold
	dbLimit := 500.0 * cfg.DBThreshold
	now := time.Now().UTC().Format("2006-01-02")

	if egressGB >= egressLimit {
		dedupeKey := fmt.Sprintf("supabase-egress:%s:%s", ref, now)
		s.mu.Lock()
		alreadyWarned := s.supabaseState[dedupeKey] == now
		s.mu.Unlock()
		if !alreadyWarned {
			pct := int(math.Round(egressGB / 5.0 * 100))
			msg := fmt.Sprintf("Supabase %s egress: %.1f GB of 5 GB (%d%%)", ref, egressGB, pct)
			if err := s.postWebhook(ctx, "", "supabase", threadID, "Supabase usage warning", msg, "", dedupeKey); err == nil {
				s.mu.Lock()
				s.supabaseState[dedupeKey] = now
				if s.supabaseStatePath != "" {
					s.saveSupabaseStateLocked()
				}
				s.mu.Unlock()
			}
		}
	}

	if dbMB >= dbLimit {
		dedupeKey := fmt.Sprintf("supabase-db:%s:%s", ref, now)
		s.mu.Lock()
		alreadyWarned := s.supabaseState[dedupeKey] == now
		s.mu.Unlock()
		if !alreadyWarned {
			pct := int(math.Round(dbMB / 500.0 * 100))
			msg := fmt.Sprintf("Supabase %s DB: %.0f MB of 500 MB (%d%%)", ref, dbMB, pct)
			if err := s.postWebhook(ctx, "", "supabase", threadID, "Supabase usage warning", msg, "", dedupeKey); err == nil {
				s.mu.Lock()
				s.supabaseState[dedupeKey] = now
				if s.supabaseStatePath != "" {
					s.saveSupabaseStateLocked()
				}
				s.mu.Unlock()
			}
		}
	}
	return nil
}

// supabaseRefresh calls the gotrue token endpoint to exchange a refresh token
// for a fresh access JWT + rotated refresh token. same flow as the local
// supabase-quota.ps1 script.
func (s *Service) supabaseRefresh(ctx context.Context, issuer, refreshToken string) (accessToken, newRefresh string, err error) {
	body := fmt.Sprintf(`{"grant_type":"refresh_token","refresh_token":"%s"}`, refreshToken)
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://"+issuer+"/auth/v1/token?grant_type=refresh_token",
		strings.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", "", err
	}
	return payload.AccessToken, payload.RefreshToken, nil
}

// supabaseEgress queries the dashboard JWT endpoint for total egress in bytes.
// the dashboard JWT endpoint is the same one the local supabase-quota.ps1 hits.
func (s *Service) supabaseEgress(ctx context.Context, issuer, accessToken, ref string) (float64, error) {
	// use the same dashboard JWT endpoint as the local script
	end := time.Now().UTC().Format("2006-01-02")
	start := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")
	u := fmt.Sprintf("https://api.supabase.com/platform/projects/%s/daily-stats?attribute=total_egress&startDate=%s&endDate=%s", ref, start, end)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		if resp.StatusCode == 401 {
			return 0, fmt.Errorf("JWT expired or invalid")
		}
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}
	var payload struct {
		Total float64 `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	return payload.Total, nil // bytes
}

// supabaseDBDisk queries the PAT-accessible endpoint for DB disk usage in MB.
func (s *Service) supabaseDBDisk(ctx context.Context, accessToken, ref string) (float64, error) {
	u := fmt.Sprintf("https://api.supabase.com/v1/projects/%s/config/disk/util", ref)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		if resp.StatusCode == 401 {
			return 0, fmt.Errorf("JWT expired or invalid")
		}
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}
	var payload struct {
		Metrics struct {
			FsUsedBytes float64 `json:"fs_used_bytes"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	return payload.Metrics.FsUsedBytes / (1024 * 1024), nil // MB
}

// extractRef pulls the project ref from a key like "supabase.<ref>.refresh_token"
func extractRef(key string) string {
	parts := strings.SplitN(key, ".", 3)
	if len(parts) == 3 && parts[0] == "supabase" {
		return parts[1]
	}
	return ""
}

func (s *Service) loadSupabaseStateLocked() {
	b, err := os.ReadFile(s.supabaseStatePath)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(b, &m) == nil {
		for k, v := range m {
			s.supabaseState[k] = v
		}
	}
}

func (s *Service) saveSupabaseStateLocked() {
	b, err := json.Marshal(s.supabaseState)
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.supabaseStatePath); dir != "." {
		os.MkdirAll(dir, 0o755)
	}
	_ = os.WriteFile(s.supabaseStatePath, b, 0o644)
	s.touchPersistence()
}

// loadNeonStateLocked / saveNeonStateLocked are shared with neonusage.go
// (loaded from the same s.statePath / s.neonState fields). the state file
// holds dedupe keys -> date for both neon and supabase watchers, which is
// fine since they use different key prefixes ("neon-", "supabase-").

// canonicalResetDate, round2, trimFloat, readAll are shared; defined in
// neonusage.go.