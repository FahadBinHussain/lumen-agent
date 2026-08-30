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
		email := s.neonEmail(ctx, key)
		orgs, err := s.neonOrgs(ctx, key)
		if err != nil {
			return fmt.Errorf("%s: orgs: %w", envName, err)
		}
		for _, org := range orgs {
			u, err := s.neonConsumption(ctx, key, org.ID)
			if err != nil {
				return fmt.Errorf("%s: %s consumption: %w", envName, org.ID, err)
			}
			u.Email = email
			if org.Name != "" {
				u.Account = org.Name
			} else {
				u.Account = org.ID
			}
			if strings.TrimSpace(u.Project) == "" {
				u.Project = u.Account
			}
			// compute (CU-hours)
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
			// storage (0.5 GB free per project)
			if u.StoragePct >= s.cfg.NeonUsage.WarningStoragePct {
				s.sendNeonStorageWarning(ctx, &u)
			}
			// egress (5 GB free per month)
			if u.EgressPct >= s.cfg.NeonUsage.WarningEgressPct {
				s.sendNeonEgressWarning(ctx, &u)
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
	Account     string
	Email       string
	ProjectID   string
	Project     string
	Used        float64
	Left        float64
	QuotaReset  string
	StorageUsed float64
	StoragePct  float64
	EgressUsed  float64
	EgressPct   float64
}

// usageLabel renders "Project (Account, email)" or falls back to the plain
// project name when no account is known.
func usageLabel(r *neonOrgUsage) string {
	label := r.Project
	parts := make([]string, 0, 2)
	if r.Account != "" && r.Account != r.Project {
		parts = append(parts, r.Account)
	}
	if r.Email != "" {
		parts = append(parts, r.Email)
	}
	if len(parts) > 0 {
		label = fmt.Sprintf("%s (%s)", r.Project, strings.Join(parts, ", "))
	}
	return label
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

	label := usageLabel(r)
	msg := fmt.Sprintf("%s has used %s of 100 CU-hours. %s CU-hours remain. Quota reset: %s UTC.",
		label, trimFloat(r.Used), trimFloat(r.Left), resetDate)

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

// sendNeonStorageWarning fires when an org's storage crosses the free-plan
// threshold (0.5 GB per project). Deduped per project per reset period, same
// pattern as the compute warning.
func (s *Service) sendNeonStorageWarning(ctx context.Context, r *neonOrgUsage) {
	resetDate := canonicalResetDate(r.QuotaReset)
	key := "storage:" + r.ProjectID

	s.mu.Lock()
	if s.neonState[key] == resetDate {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	label := usageLabel(r)
	msg := fmt.Sprintf("%s is using %s MB of 512 MB storage (%s%%). Quota reset: %s UTC.",
		label, trimFloat(r.StorageUsed), trimFloat(r.StoragePct), resetDate)

	err := s.postWebhook(ctx, "", "neon-usage", s.cfg.NeonUsage.ThreadID,
		"Neon storage warning", msg, "", "neon-storage:"+key+":"+resetDate)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.neonState[key] = resetDate
	if s.statePath != "" {
		s.saveNeonStateLocked()
	}
	s.mu.Unlock()
}

// sendNeonEgressWarning fires when an org's data transfer crosses the free-plan
// threshold (5 GB per month). Deduped per project per reset period.
func (s *Service) sendNeonEgressWarning(ctx context.Context, r *neonOrgUsage) {
	resetDate := canonicalResetDate(r.QuotaReset)
	key := "egress:" + r.ProjectID

	s.mu.Lock()
	if s.neonState[key] == resetDate {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	label2 := usageLabel(r)
	msg := fmt.Sprintf("%s has transferred %s GB of 5 GB (%s%%). Quota reset: %s UTC.",
		label2, trimFloat(r.EgressUsed), trimFloat(r.EgressPct), resetDate)

	err := s.postWebhook(ctx, "", "neon-usage", s.cfg.NeonUsage.ThreadID,
		"Neon egress warning", msg, "", "neon-egress:"+key+":"+resetDate)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.neonState[key] = resetDate
	if s.statePath != "" {
		s.saveNeonStateLocked()
	}
	s.mu.Unlock()
}

type neonOrg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// neonEmail resolves the account email for an API key via the /users/me
// endpoint. Best-effort: empty string on any failure (the warnings still
// work, they just omit the email).
func (s *Service) neonEmail(ctx context.Context, apiKey string) string {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://console.neon.tech/api/v2/users/me", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	var payload struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.User.Email)
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
			ComputeTime     float64 `json:"compute_time"`
			PeriodEnd       string  `json:"period_end"`
			DataTransfer    float64 `json:"data_transfer"`
			PeakDataStorage float64 `json:"peak_data_storage"`
			DataStorage     float64 `json:"data_storage"`
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
		// storage: 0.5 GB free per project = 536870912 bytes
		storage := last.PeakDataStorage
		if storage == 0 {
			storage = last.DataStorage
		}
		u.StorageUsed = round2(storage / 1048576) // MB
		u.StoragePct = round2((storage / 536870912) * 100)
		// egress: 5 GB free per month = 5368709120 bytes
		u.EgressUsed = round2(last.DataTransfer / 1073741824) // GB
		u.EgressPct = round2((last.DataTransfer / 5368709120) * 100)
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
	s.touchPersistence()
}

func readAll(r io.Reader) string {
	b, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	return string(b)
}
