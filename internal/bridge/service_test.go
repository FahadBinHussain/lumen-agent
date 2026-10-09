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
	"element-orion/internal/vision"
	"element-orion/internal/whatsapp"
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

// TestScopedDedupeKeyIsPerDestination covers the 2026-10-08 bug: one feed
// item posted to two free-games threads with the SAME key was delivered only
// to the first thread (the second POST read the first thread's delivery row
// and returned deduped). The key must be unique per destination while staying
// stable for retries of the same destination.
func TestScopedDedupeKeyIsPerDestination(t *testing.T) {
	key := "guid-123"
	threadA := scopedDedupeKey(key, "", "messenger", "30738305889116993")
	threadB := scopedDedupeKey(key, "", "messenger", "953525124128433")
	if threadA == threadB {
		t.Fatalf("two destinations must not share a dedupe key: %q", threadA)
	}
	// same destination must stay stable (retries stay deduped)
	if again := scopedDedupeKey(key, "", "messenger", "30738305889116993"); again != threadA {
		t.Fatalf("same destination key changed: %q != %q", again, threadA)
	}
	// platform default mirrors the handler (messenger)
	if def := scopedDedupeKey(key, "", "", "30738305889116993"); def != threadA {
		t.Fatalf("empty platform must default to messenger: %q != %q", def, threadA)
	}
	// route mode scopes on the route name
	routeKey := scopedDedupeKey(key, "bnp", "", "")
	if routeKey == threadA || routeKey != scopedDedupeKey(key, "bnp", "", "") {
		t.Fatalf("route key wrong: %q", routeKey)
	}
	// empty key keeps the dedupe path off
	if got := scopedDedupeKey("", "", "messenger", "1"); got != "" {
		t.Fatalf("empty key must stay empty, got %q", got)
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

func TestWhatsAppPairErrorsAreJSON(t *testing.T) {
	t.Setenv("WHATSAPP_PAIR_TOKEN", "test-token")
	cfg := writeTestConfig(t, "")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	defer s.Close()

	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/api/whatsapp/pair", bytes.NewBufferString(`{}`))
	q := req.URL.Query()
	q.Set("token", "test-token")
	req.URL.RawQuery = q.Encode()
	rec := newRecorder()
	s.handleWhatsAppPair(rec, req)
	if rec.status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.status)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.body.Bytes(), &resp); err != nil {
		t.Fatalf("pair error was not JSON: %v; body=%q", err, rec.body.String())
	}
	if resp["status"] != "error" || resp["message"] == "" {
		t.Fatalf("unexpected pair error response: %#v", resp)
	}
}

// TestWhatsAppQRPageNeverShowsBrokenImage pins the 2026-10-09 report: the QR
// box used to render a bare <img> with no src whenever the tail-exit flapped
// (status=waiting), so the page showed a broken-image icon instead of an
// explanation, and failures printed in the green success color.
func TestWhatsAppQRPageNeverShowsBrokenImage(t *testing.T) {
	if !strings.Contains(whatsappPairPage, `#qrbox{background:#fff;padding:16px;border-radius:12px;display:none`) {
		t.Fatal("qrbox must start hidden so an unloaded <img> never renders as a broken-image icon")
	}
	if !strings.Contains(whatsappPairPage, "img.onerror=") {
		t.Fatal("page must handle a failed QR image load (session died between poll and png fetch)")
	}
	if !strings.Contains(whatsappPairPage, `#status.err{color:#ff6b6b`) {
		t.Fatal("errors must render red, not the green success color")
	}
	if !strings.Contains(whatsappPairPage, "placeholder('waiting for a fresh QR") {
		t.Fatal("status=waiting must show a visible placeholder with the reason")
	}
}

func TestWhatsAppQRAuthAndStateAreJSON(t *testing.T) {
	t.Setenv("WHATSAPP_PAIR_TOKEN", "test-token")
	cfg := writeTestConfig(t, "")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	defer s.Close()

	// wrong token: loud 401 JSON
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/api/whatsapp/qr?format=json&token=wrong", nil)
	rec := newRecorder()
	s.handleWhatsAppQR(rec, req)
	if rec.status != http.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d: %s", rec.status, rec.body.String())
	}
	var unauth map[string]string
	if err := json.Unmarshal(rec.body.Bytes(), &unauth); err != nil || unauth["status"] != "error" {
		t.Fatalf("401 body must be JSON with status=error: %v %q", err, rec.body.String())
	}

	// valid token: authorized (state depends on config, but never 401)
	req, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/api/whatsapp/qr?format=json&token=test-token", nil)
	rec = newRecorder()
	s.handleWhatsAppQR(rec, req)
	if rec.status != http.StatusOK {
		t.Fatalf("valid token: expected 200, got %d: %s", rec.status, rec.body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.body.Bytes(), &resp); err != nil {
		t.Fatalf("qr response not JSON: %v; body=%q", err, rec.body.String())
	}
	switch resp["status"] {
	case "disabled", "waiting", "qr", "paired":
	default:
		t.Fatalf("unexpected qr status %q", resp["status"])
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
			var body struct {
				Status    string          `json:"status"`
				Platforms map[string]bool `json:"platforms"`
				Vision    struct {
					Enabled bool   `json:"enabled"`
					Status  string `json:"status"`
				} `json:"vision"`
			}
			decErr := json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("health returned %d", resp.StatusCode)
			}
			if decErr != nil {
				t.Fatalf("health body not JSON: %v", decErr)
			}
			if body.Status != "ok" {
				t.Fatalf("health status = %q, want ok", body.Status)
			}
			for _, p := range []string{"messenger", "whatsapp", "discord"} {
				if _, ok := body.Platforms[p]; !ok {
					t.Fatalf("health platforms missing %q: %+v", p, body.Platforms)
				}
			}
			// vision rides along: this config has vision.enabled unset and no
			// runner wired, so it must read the plain "disabled" state.
			if body.Vision.Status != "disabled" || body.Vision.Enabled {
				t.Fatalf("health vision = %+v, want disabled/enabled=false", body.Vision)
			}
			return
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("health never answered: %v", lastErr)
}

func TestVisionHealthLoudWhenEnabledWithoutRunner(t *testing.T) {
	cfg := config.Config{}
	cfg.Vision.Enabled = true
	s := &Service{cfg: cfg}
	h := s.visionHealth()
	if h.Status != vision.StatusMisconfigured || !h.Enabled || h.LastError == "" {
		t.Fatalf("vision health = %+v, want enabled misconfigured naming the wiring gap", h)
	}

	disabled := &Service{cfg: config.Config{}}
	if h := disabled.visionHealth(); h.Status != vision.StatusDisabled || h.Enabled {
		t.Fatalf("vision health (disabled config) = %+v, want plain disabled", h)
	}
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

func TestWhatsAppTriggerPrompt(t *testing.T) {
	wa := func(text string, replyToUs, mentionsMe, hasMedia bool) whatsapp.ParsedMessage {
		m := whatsapp.ParsedMessage{
			Chat:        whatsapp.ChatJID{User: "8801111111111", Server: "s.whatsapp.net"},
			SenderJID:   "8802222222222@s.whatsapp.net",
			Text:        text,
			IsReplyToUs: replyToUs,
			MentionsMe:  mentionsMe,
		}
		if hasMedia {
			m.Media = &whatsapp.Media{Type: "image"}
		}
		return m
	}

	cases := []struct {
		name string
		msg  whatsapp.ParsedMessage
		want string
	}{
		{"plain dm stays silent", wa("hello there", false, false, false), ""},
		{"plain group message stays silent", wa("hello everyone", false, false, false), ""},
		{"ai prefix triggers", wa("/ai who are you", false, false, false), "who are you"},
		{"ai prefix with leading space", wa("  /ai  hi  ", false, false, false), "hi"},
		{"ai prefix on media caption", wa("/ai describe this", false, false, true), "describe this"},
		{"reply to our message triggers", wa("what did you mean?", true, false, false), "what did you mean?"},
		{"mention triggers", wa("hey @Kite help", false, true, false), "hey @Kite help"},
		{"reply and mention both", wa("explain", true, true, false), "explain"},
		{"media-only reply stays silent", wa("", true, false, true), ""},
		{"media-only mention stays silent", wa("", false, true, true), ""},
		{"empty ai prompt skipped", wa("/ai", false, false, false), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := whatsappTriggerPrompt(tc.msg); got != tc.want {
				t.Fatalf("whatsappTriggerPrompt() = %q, want %q", got, tc.want)
			}
		})
	}
}
