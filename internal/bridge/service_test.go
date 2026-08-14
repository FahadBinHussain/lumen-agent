package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"element-orion/internal/config"
	"element-orion/internal/llm"
)

func writeTestConfig(t *testing.T, extra string) config.Config {
	t.Helper()
	dir := t.TempDir()
	base := `
app:
  workspace_root: .
  session_dir: ./.element-orion
llm:
  base_url: https://example.invalid/v1
  api_key: "test"
  model: test-model
discord:
  token_mode: bot
  bot_token: "dummy"
  allow_direct_messages: true
bridge:
  enabled: true
  listen_addr: 127.0.0.1:0
  notifications_path: /api/automation/notifications
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(base+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func TestBridgeNotificationsEndpoint(t *testing.T) {
	cfg := writeTestConfig(t, "")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// port 0 means the HTTP server picks a random port, but we cannot read it back.
	// just verify the server boots and health endpoint answers via a direct handler run
	go func() {
		_ = s.serveHTTP(ctx)
	}()

	// exercise the notification handler directly against the service
	body, _ := json.Marshal(map[string]string{
		"source":   "test",
		"threadId": "12345",
		"title":    "Title",
		"message":  "Hello bridge",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/api/automation/notifications", bytes.NewReader(body))
	rec := newRecorder()
	s.handleAutomationNotification(rec, req)
	if rec.status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.status, rec.body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "sent" {
		t.Fatalf("expected status sent, got %q", resp["status"])
	}

	// missing threadId must 400
	body, _ = json.Marshal(map[string]string{"message": "x"})
	req, _ = http.NewRequest(http.MethodPost, "http://127.0.0.1/api/automation/notifications", bytes.NewReader(body))
	rec = newRecorder()
	s.handleAutomationNotification(rec, req)
	if rec.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing threadId, got %d", rec.status)
	}
}

func TestBridgeNotificationsAuth(t *testing.T) {
	cfg := writeTestConfig(t, "  secret: hunter2\n  secret_env: \"\"\n")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	defer s.Close()

	body, _ := json.Marshal(map[string]string{
		"threadId": "12345",
		"message":  "secret test",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/api/automation/notifications", bytes.NewReader(body))
	rec := newRecorder()
	s.handleAutomationNotification(rec, req)
	if rec.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without secret, got %d", rec.status)
	}

	req, _ = http.NewRequest(http.MethodPost, "http://127.0.0.1/api/automation/notifications", bytes.NewReader(body))
	req.Header.Set("X-HF-Authorization", "hunter2")
	rec = newRecorder()
	s.handleAutomationNotification(rec, req)
	if rec.status != http.StatusOK {
		t.Fatalf("expected 200 with secret, got %d: %s", rec.status, rec.body.String())
	}

	req, _ = http.NewRequest(http.MethodPost, "http://127.0.0.1/api/automation/notifications", bytes.NewReader(body))
	req.Header.Set("X-HF-Authorization", "Bearer hunter2")
	rec = newRecorder()
	s.handleAutomationNotification(rec, req)
	if rec.status != http.StatusOK {
		t.Fatalf("expected 200 with bearer-prefixed secret (poller contract), got %d: %s", rec.status, rec.body.String())
	}
}

func TestBridgeHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := writeTestConfig(t, "")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	s.historyPath = filepath.Join(dir, bridgeHistoryFile)

	s.mu.Lock()
	s.sessions["messenger:12345"] = []llm.Message{{Role: "user", Content: "hello"}}
	s.mu.Unlock()
	s.saveHistory()

	loaded := &Service{sessions: make(map[string][]llm.Message), historyPath: s.historyPath}
	loaded.loadHistory()
	loaded.mu.Lock()
	defer loaded.mu.Unlock()
	got, ok := loaded.sessions["messenger:12345"]
	if !ok || len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("history did not survive round trip: %+v", loaded.sessions)
	}
}

type recorder struct {
	status int
	header http.Header
	body   bytes.Buffer
}

func newRecorder() *recorder {
	return &recorder{status: 200, header: make(http.Header)}
}

func (r *recorder) Header() http.Header {
	return r.header
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
}

func (r *recorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
}

func TestBridgeNotificationsRequiresRunningServer(t *testing.T) {
	// sanity: serveHTTP on a real port boots and answers /api/health
	dir := t.TempDir()
	cfgText := `
app:
  workspace_root: .
  session_dir: ./.element-orion
llm:
  base_url: https://example.invalid/v1
  api_key: "test"
  model: test-model
discord:
  token_mode: bot
  bot_token: "dummy"
  allow_direct_messages: true
bridge:
  enabled: true
  listen_addr: 127.0.0.1:18791
  notifications_path: /api/automation/notifications
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- s.serveHTTP(ctx)
	}()

	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:18791/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			t.Fatalf("health returned %d", resp.StatusCode)
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("health never answered: %v", lastErr)
}

func TestConfigRejectsBadBridgePath(t *testing.T) {
	dir := t.TempDir()
	cfgText := `
app:
  workspace_root: .
llm:
  base_url: https://example.invalid/v1
  api_key: "test"
  model: test-model
discord:
  token_mode: bot
  bot_token: "dummy"
  allow_direct_messages: true
bridge:
  enabled: true
  listen_addr: 127.0.0.1:0
  notifications_path: notifications
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "notifications_path") {
		t.Fatalf("expected notifications_path validation error, got %v", err)
	}
}
