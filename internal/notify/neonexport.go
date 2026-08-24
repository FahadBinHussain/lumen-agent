package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// exportFile is one encrypted pg_dump ready to be committed. srcPath is the
// local encrypted file (in /tmp), destName is its path inside the repo branch.
type exportFile struct {
	SrcPath  string
	DestName string
}

// neonProject describes one project under an org (id + name from the list
// endpoint; the connection URI is fetched per project later).
type neonProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// neonBranch is the first branch of a project (used for connection URIs).
type neonBranch struct {
	ID string `json:"id"`
}

// exportNeonOrg dumps every project under an over-threshold org, encrypts each
// dump, and returns the list of encrypted files ready for one batched push.
// It does NOT push — pushExports handles that after all orgs have exported.
func (s *Service) exportNeonOrg(ctx context.Context, apiKey, orgID string) ([]exportFile, error) {
	cfg := s.cfg.NeonUsage.Export
	key := strings.TrimSpace(os.Getenv(cfg.KeyEnv))
	if key == "" {
		return nil, fmt.Errorf("no %s set, skipping export", cfg.KeyEnv)
	}

	projects, err := s.neonOrgProjects(ctx, apiKey, orgID)
	if err != nil {
		return nil, err
	}

	var files []exportFile
	for _, p := range projects {
		uri, err := s.neonConnectionURI(ctx, apiKey, p.ID)
		if err != nil {
			log.Printf("notify: export %s (%s): connection uri: %v", p.ID, p.Name, err)
			continue
		}
		dest := p.ID + ".sql.gz.enc"
		src, err := s.dumpEncrypt(ctx, uri, key, dest)
		if err != nil {
			log.Printf("notify: export %s (%s): %v", p.ID, p.Name, err)
			continue
		}
		files = append(files, exportFile{SrcPath: src, DestName: dest})
	}
	return files, nil
}

// neonOrgProjects lists every project under an org.
func (s *Service) neonOrgProjects(ctx context.Context, apiKey, orgID string) ([]neonProject, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://console.neon.tech/api/v2/projects?org_id="+orgID+"&limit=200", nil)
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
		return nil, fmt.Errorf("projects http %d", resp.StatusCode)
	}
	var payload struct {
		Projects []neonProject `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Projects, nil
}

// neonConnectionURI resolves the pooled connection URI for a project's main
// branch (first branch, first database, matching role) via the Neon REST API.
func (s *Service) neonConnectionURI(ctx context.Context, apiKey, projectID string) (string, error) {
	var branches struct {
		Branches []neonBranch `json:"branches"`
	}
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://console.neon.tech/api/v2/projects/"+projectID+"/branches", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	if err := json.NewDecoder(resp.Body).Decode(&branches); err != nil {
		resp.Body.Close()
		return "", err
	}
	resp.Body.Close()
	if len(branches.Branches) == 0 {
		return "", fmt.Errorf("no branches")
	}
	branchID := branches.Branches[0].ID

	// first database name on the branch
	dbName, err := s.neonDatabaseName(ctx, apiKey, projectID, branchID)
	if err != nil {
		return "", err
	}

	// role matching "<db>_owner", else the first role
	roleName, err := s.neonRoleName(ctx, apiKey, projectID, branchID, dbName)
	if err != nil {
		return "", err
	}

	uriReq, err := http.NewRequestWithContext(ctx, "GET",
		"https://console.neon.tech/api/v2/projects/"+projectID+
			"/connection_uri?database_name="+dbName+"&branch_id="+branchID+"&role_name="+roleName, nil)
	if err != nil {
		return "", err
	}
	uriReq.Header.Set("Authorization", "Bearer "+apiKey)
	uriReq.Header.Set("Accept", "application/json")
	uriResp, err := s.client.Do(uriReq)
	if err != nil {
		return "", err
	}
	defer uriResp.Body.Close()
	if uriResp.StatusCode >= 400 {
		return "", fmt.Errorf("connection_uri http %d", uriResp.StatusCode)
	}
	var uriPayload struct {
		URI string `json:"uri"`
	}
	if err := json.NewDecoder(uriResp.Body).Decode(&uriPayload); err != nil {
		return "", err
	}
	return uriPayload.URI, nil
}

func (s *Service) neonDatabaseName(ctx context.Context, apiKey, projectID, branchID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://console.neon.tech/api/v2/projects/"+projectID+"/branches/"+branchID+"/databases", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var payload struct {
		Databases []struct {
			Name string `json:"name"`
		} `json:"databases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if len(payload.Databases) == 0 {
		return "", fmt.Errorf("no databases on branch")
	}
	return payload.Databases[0].Name, nil
}

func (s *Service) neonRoleName(ctx context.Context, apiKey, projectID, branchID, dbName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://console.neon.tech/api/v2/projects/"+projectID+"/branches/"+branchID+"/roles", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var payload struct {
		Roles []struct {
			Name string `json:"name"`
		} `json:"roles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if len(payload.Roles) == 0 {
		return "", fmt.Errorf("no roles on branch")
	}
	owner := dbName + "_owner"
	for _, r := range payload.Roles {
		if r.Name == owner {
			return r.Name, nil
		}
	}
	return payload.Roles[0].Name, nil
}

// dumpEncrypt runs pg_dump | gzip | openssl into /tmp/<name>, returning the
// encrypted file path. The encryption key is passed to openssl via its env var
// (never on the command line, so it can't leak through /proc).
func (s *Service) dumpEncrypt(ctx context.Context, uri, key, name string) (string, error) {
	outPath := filepath.Join(os.TempDir(), name)

	timeout := 60 * time.Second
	if s.cfg.NeonUsage.Export.ExportTimeout != "" {
		if d, err := time.ParseDuration(s.cfg.NeonUsage.Export.ExportTimeout); err == nil && d > 0 {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dump := exec.CommandContext(ctx, "pg_dump", "--no-owner", "--no-privileges", "--format=plain", uri)
	gzip := exec.CommandContext(ctx, "gzip", "-1")
	openssl := exec.CommandContext(ctx, "openssl", "enc", "-aes-256-cbc", "-pbkdf2", "-salt",
		"-pass", "env:"+s.cfg.NeonUsage.Export.KeyEnv)
	openssl.Env = append(os.Environ(), s.cfg.NeonUsage.Export.KeyEnv+"="+key)

	// dump -> gzip
	dumpToGzip, gzipIn, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer dumpToGzip.Close()
	gzip.Stdin = gzipIn
	dump.Stdout = dumpToGzip

	// gzip -> openssl
	gzipToSSL, sslIn, err := os.Pipe()
	if err != nil {
		gzipIn.Close()
		return "", err
	}
	defer gzipToSSL.Close()
	openssl.Stdin = sslIn
	gzip.Stdout = gzipToSSL

	outFile, err := os.Create(outPath)
	if err != nil {
		gzipIn.Close()
		sslIn.Close()
		return "", err
	}
	openssl.Stdout = outFile

	if err := dump.Start(); err != nil {
		gzipIn.Close()
		sslIn.Close()
		outFile.Close()
		os.Remove(outPath)
		return "", err
	}
	if err := gzip.Start(); err != nil {
		cleanupExport(outPath, outFile, dump)
		return "", err
	}
	if err := openssl.Start(); err != nil {
		cleanupExport(outPath, outFile, dump, gzip)
		return "", err
	}
	// parent's copies of the write ends must close so the children see EOF
	gzipIn.Close()
	sslIn.Close()

	dumpErr := dump.Wait()
	gzipErr := gzip.Wait()
	sslErr := openssl.Wait()
	outFile.Close()
	if dumpErr != nil {
		os.Remove(outPath)
		return "", fmt.Errorf("pg_dump: %w", dumpErr)
	}
	if gzipErr != nil {
		os.Remove(outPath)
		return "", fmt.Errorf("gzip: %w", gzipErr)
	}
	if sslErr != nil {
		os.Remove(outPath)
		return "", fmt.Errorf("openssl: %w", sslErr)
	}
	return outPath, nil
}

func cleanupExport(path string, f *os.File, procs ...*exec.Cmd) {
	if f != nil {
		f.Close()
	}
	for _, p := range procs {
		if p != nil && p.Process != nil {
			p.Process.Kill()
		}
	}
	os.Remove(path)
}

// pushExports force-pushes every collected encrypted export to the configured
// repo branch as ONE orphan commit (flat history, overwrites the previous
// export every poll so storage doesn't stack).
func (s *Service) pushExports(ctx context.Context, files []exportFile) error {
	cfg := s.cfg.NeonUsage.Export
	tok := strings.TrimSpace(os.Getenv(cfg.GitHubTokenEnv))
	if tok == "" {
		return fmt.Errorf("no %s set, skipping push", cfg.GitHubTokenEnv)
	}

	repoDir, err := os.MkdirTemp("", "lumen-exports-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(repoDir)

	// new orphan branch (no parent, so history stays flat)
	if out, err := runGit(ctx, repoDir, nil, "init", "-b", cfg.Branch); err != nil {
		return fmt.Errorf("git init: %v: %s", err, out)
	}
	// local identity for the commit (never leaks; not the user's identity)
	if out, err := runGit(ctx, repoDir, nil, "config", "user.name", "lumen export"); err != nil {
		return fmt.Errorf("git config: %v: %s", err, out)
	}
	if out, err := runGit(ctx, repoDir, nil, "config", "user.email", "lumen-export@localhost"); err != nil {
		return fmt.Errorf("git config: %v: %s", err, out)
	}

	destDir := filepath.Join(repoDir, cfg.Path)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		in, err := os.Open(f.SrcPath)
		if err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(destDir, filepath.Base(f.DestName)))
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			return err
		}
	}

	if out, err := runGit(ctx, repoDir, nil, "add", "."); err != nil {
		return fmt.Errorf("git add: %v: %s", err, out)
	}
	msg := fmt.Sprintf("export %d project(s) %s", len(files), time.Now().UTC().Format(time.RFC3339))
	if out, err := runGit(ctx, repoDir, nil, "commit", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %v: %s", err, out)
	}

	pushURL := "https://x-access-token:" + tok + "@github.com/" + cfg.Repo + ".git"
	if out, err := runGit(ctx, repoDir, []string{"GIT_TERMINAL_PROMPT=0"},
		"push", "--force", pushURL, cfg.Branch); err != nil {
		return fmt.Errorf("git push: %v: %s", err, out)
	}
	log.Printf("notify: exports pushed to %s:%s (%d file(s))", cfg.Repo, cfg.Branch, len(files))
	return nil
}

func runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
