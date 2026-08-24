package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// checkNeonUsage is a faithful Go port of murmur/scripts/murmur.ps1
// Check-NeonUsage + Send-NeonUsageWarning, querying the Neon REST API the same
// way mainframe/neon-hours-table.ps1 does (org consumption endpoint, period
// data from the LAST period entry). API keys come from the configured env var
// names (HF space secrets), mirroring mainframe's per-account api-key files.
func (s *Service) checkNeonUsage(ctx context.Context) error {
	if len(s.cfg.NeonUsage.APIKeyEnv) == 0 {
		return fmt.Errorf("no neon api keys configured (api_key_env)")
	}
	threadID := s.cfg.NeonUsage.ThreadID
	if threadID == "" {
		return fmt.Errorf("no neon usage thread configured")
	}

	s.mu.Lock()
	if s.neonState == nil {
		s.neonState = map[string]string{}
	}
	if s.statePath != "" {
		s.loadNeonStateLocked()
	}
	s.mu.Unlock()

	// The export runs on its own cadence (export_interval, default 24h),
	// decoupled from the 1h warning poll — so we re-dump at most once per
	// interval while an org stays over threshold. The WARNING still fires once
	// per reset period (deduped in sendNeonWarning).
	var exportFiles []exportFile
	overThreshold := 0
	exportDue := false
	if s.cfg.NeonUsage.Export.Enabled {
		s.mu.Lock()
		exportDue = time.Since(s.lastExport) >= s.exportIntervalDuration()
		s.mu.Unlock()
	}
	for _, envName := range s.cfg.NeonUsage.APIKeyEnv {
		key := strings.TrimSpace(os.Getenv(envName))
		if key == "" {
			continue
		}
		orgs, err := s.neonOrgs(ctx, key)
		if err != nil {
			return fmt.Errorf("%s: orgs: %w", envName, err)
		}
		for _, org := range orgs {
			u, err := s.neonConsumption(ctx, key, org.ID)
			if err != nil {
				return fmt.Errorf("%s: %s consumption: %w", envName, org.ID, err)
			}
			if u.Used >= s.cfg.NeonUsage.WarningHours {
				overThreshold++
				s.sendNeonWarning(ctx, &u)
				if s.cfg.NeonUsage.Export.Enabled && exportDue {
					files, err := s.exportNeonOrg(ctx, key, org.ID)
					if err != nil {
						log.Printf("notify: export %s: %v", org.ID, err)
					}
					exportFiles = append(exportFiles, files...)
				}
			}
		}
	}

	// Single batched push: every over-threshold org's projects go into ONE
	// commit, force-pushed over the previous export (flat branch history).
	if len(exportFiles) > 0 {
		if err := s.pushExports(ctx, exportFiles); err != nil {
			log.Printf("notify: push exports: %v", err)
		} else {
			s.mu.Lock()
			s.lastExport = time.Now()
			s.mu.Unlock()
		}
	}
	return nil
}

// exportIntervalDuration resolves the export cadence, defaulting to 24h.
func (s *Service) exportIntervalDuration() time.Duration {
	v := s.cfg.NeonUsage.Export.ExportInterval
	if v == "" {
		v = "24h"
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 24 * time.Hour
	}
	return d
}

type neonOrgUsage struct {
	Account    string
	ProjectID  string
	Project    string
	Used       float64
	Left       float64
	QuotaReset string
}

func (s *Service) sendNeonWarning(ctx context.Context, r *neonOrgUsage) {
	resetDate := canonicalResetDate(r.QuotaReset)
	dedupeKey := fmt.Sprintf("neon-%v:%s:%s", s.cfg.NeonUsage.WarningHours, r.ProjectID, resetDate)

	s.mu.Lock()
	if s.neonState[r.ProjectID] == resetDate {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	msg := fmt.Sprintf("%s (%s) has used %s of 100 CU-hours. %s CU-hours remain. Quota reset: %s UTC.",
		r.Project, r.Account, trimFloat(r.Used), trimFloat(r.Left), resetDate)

	err := s.postWebhook(ctx, "", "neon-usage", s.cfg.NeonUsage.ThreadID,
		"Neon usage warning", msg, "", dedupeKey)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.neonState[r.ProjectID] = resetDate
	if s.statePath != "" {
		s.saveNeonStateLocked()
	}
	s.mu.Unlock()
}

type neonOrg struct {
	ID string `json:"id"`
}

func (s *Service) neonOrgs(ctx context.Context, apiKey string) ([]neonOrg, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://console.neon.tech/api/v2/users/me/organizations", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	var payload struct {
		Organizations []neonOrg `json:"organizations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Organizations, nil
}

func (s *Service) neonConsumption(ctx context.Context, apiKey, orgID string) (neonOrgUsage, error) {
	u := neonOrgUsage{ProjectID: orgID, QuotaReset: "-"}

	// project names for visibility (comma-joined), same as the PS script
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://console.neon.tech/api/v2/projects?org_id="+orgID+"&limit=100", nil)
	if err != nil {
		return u, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return u, err
	}
	var projPayload struct {
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&projPayload); err == nil {
		names := make([]string, 0, len(projPayload.Projects))
		for _, p := range projPayload.Projects {
			names = append(names, p.Name)
		}
		u.Project = strings.Join(names, ",")
	}
	resp.Body.Close()

	req, err = http.NewRequestWithContext(ctx, "GET",
		"https://console.neon.tech/api/v2/organizations/"+orgID+"/consumption", nil)
	if err != nil {
		return u, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err = s.client.Do(req)
	if err != nil {
		return u, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return u, fmt.Errorf("http %d", resp.StatusCode)
	}
	var payload struct {
		Periods []struct {
			ComputeTime float64 `json:"compute_time"`
			PeriodEnd   string  `json:"period_end"`
		} `json:"periods"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return u, err
	}
	if len(payload.Periods) > 0 {
		// periods sorted oldest->newest; take the last (current period)
		last := payload.Periods[len(payload.Periods)-1]
		u.Used = round2(last.ComputeTime / 3600)
		u.Left = round2((360000 - last.ComputeTime) / 3600)
		u.QuotaReset = last.PeriodEnd
	}
	return u, nil
}

// canonicalResetDate matches the PS script's UtcDateTime->yyyy-MM-dd
func canonicalResetDate(raw string) string {
	if raw == "" || raw == "-" {
		return "unknown"
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format("2006-01-02")
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func (s *Service) loadNeonStateLocked() {
	b, err := os.ReadFile(s.statePath)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(b, &m) == nil {
		for k, v := range m {
			s.neonState[k] = v
		}
	}
}

func (s *Service) saveNeonStateLocked() {
	b, err := json.Marshal(s.neonState)
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.statePath); dir != "." {
		os.MkdirAll(dir, 0o755)
	}
	_ = os.WriteFile(s.statePath, b, 0o644)
}

func readAll(r io.Reader) string {
	b, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	return string(b)
}