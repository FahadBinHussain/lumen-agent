package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"element-orion/internal/config"
)

func testConfig(baseURL string) config.VisionConfig {
	return config.VisionConfig{
		Enabled:     true,
		BaseURL:     baseURL,
		APIKey:      "public",
		UserAgent:   "opencode/1.18.25 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
		Model:       "mimo-v2.6-flash-free",
		Prompt:      "Describe this image.",
		Timeout:     "30s",
		MaxAttempts: 3,
	}
}

func sseBody(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(`data: {"choices":[{"delta":{"content":` + jsonString(c) + `}}]}` + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func jsonString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func redPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding red png: %v", err)
	}
	return buf.Bytes()
}

func redDataURL(t *testing.T) string {
	t.Helper()
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(redPNG(t))
}

func TestNewAppliesDefaults(t *testing.T) {
	d := New(config.VisionConfig{})
	if d.apiKey != "public" {
		t.Fatalf("apiKey = %q, want public", d.apiKey)
	}
	if d.userAgent != visionClientUA {
		t.Fatalf("userAgent = %q, want %q", d.userAgent, visionClientUA)
	}
	if d.timeout != 120*time.Second {
		t.Fatalf("timeout = %v, want 120s", d.timeout)
	}
	if d.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", d.attempts)
	}
}

func TestDescribeRejectsBadProxyLoudly(t *testing.T) {
	cfg := testConfig("https://opencode.ai/zen/v1")
	cfg.ProxyURL = "ftp://not-a-supported-proxy"
	d := New(cfg)
	if d.initErr == nil {
		t.Fatal("expected initErr for unsupported proxy scheme, got nil")
	}
	if _, err := d.Describe(context.Background(), "data:image/png;base64,AAAA"); err == nil || !strings.Contains(err.Error(), "proxy_url") {
		t.Fatalf("Describe error = %v, want proxy_url mention", err)
	}
}

func TestDescribeRequestShape(t *testing.T) {
	type captured struct {
		method, path, authorization, userAgent string
		headers                                map[string]string
		body                                   map[string]any
	}
	got := captured{headers: map[string]string{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.authorization = r.Header.Get("Authorization")
		got.userAgent = r.Header.Get("User-Agent")
		for _, key := range []string{"x-opencode-client", "x-opencode-project", "x-opencode-session", "x-opencode-request", "Accept"} {
			got.headers[key] = r.Header.Get(key)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading body: %v", err)
			return
		}
		if err := json.Unmarshal(raw, &got.body); err != nil {
			t.Errorf("decoding body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseBody("solid red square"))
	}))
	defer srv.Close()

	d := New(testConfig(srv.URL + "/v1"))
	text, err := d.Describe(context.Background(), redDataURL(t))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if text != "solid red square" {
		t.Fatalf("text = %q, want %q", text, "solid red square")
	}

	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", got.path)
	}
	if got.authorization != "Bearer public" {
		t.Errorf("authorization = %q, want Bearer public", got.authorization)
	}
	if got.userAgent != "opencode/1.18.25 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14" {
		t.Errorf("user-agent = %q", got.userAgent)
	}
	if got.headers["x-opencode-client"] != "cli" || got.headers["x-opencode-project"] != "global" {
		t.Errorf("identity headers = %v", got.headers)
	}
	idPattern := regexp.MustCompile(`^(ses|msg)_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	if !idPattern.MatchString(got.headers["x-opencode-session"]) {
		t.Errorf("x-opencode-session = %q, want ses_ + 12 hex + 14 base62", got.headers["x-opencode-session"])
	}
	if !idPattern.MatchString(got.headers["x-opencode-request"]) {
		t.Errorf("x-opencode-request = %q, want msg_ + 12 hex + 14 base62", got.headers["x-opencode-request"])
	}

	if got.body["model"] != "mimo-v2.6-flash-free" {
		t.Errorf("model = %v", got.body["model"])
	}
	if got.body["stream"] != true {
		t.Errorf("stream = %v, want true (zen gate requires it)", got.body["stream"])
	}
	streamOptions, _ := got.body["stream_options"].(map[string]any)
	if streamOptions["include_usage"] != true {
		t.Errorf("stream_options.include_usage = %v", streamOptions)
	}
	if got.body["max_tokens"] != float64(32000) || got.body["temperature"] != 0.5 {
		t.Errorf("max_tokens/temperature = %v/%v", got.body["max_tokens"], got.body["temperature"])
	}

	messages, _ := got.body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(messages))
	}
	system, _ := messages[0].(map[string]any)
	systemText, _ := system["content"].(string)
	if systemText != zengateSystemPrompt+zengateOverrideTail {
		t.Errorf("system prompt must be the embedded official prompt + override tail (gate prefix-match), got %d chars", len(systemText))
	}
	user, _ := messages[1].(map[string]any)
	parts, _ := user["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("user content parts = %d, want 2 (text + image)", len(parts))
	}
	textPart, _ := parts[0].(map[string]any)
	if textPart["text"] != "Describe this image." {
		t.Errorf("user text = %v", textPart["text"])
	}
	imagePart, _ := parts[1].(map[string]any)
	imageURL, _ := imagePart["image_url"].(map[string]any)
	if url, _ := imageURL["url"].(string); !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image url prefix = %.40q, want data:image/png;base64,", url)
	}
}

func TestDescribeRetriesThenSucceeds(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"transient"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseBody("recovered"))
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxAttempts = 3
	d := New(cfg)
	text, err := d.Describe(context.Background(), redDataURL(t))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if text != "recovered" {
		t.Fatalf("text = %q, want recovered", text)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestDescribeFailsLoudlyWithStatusAndBody(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode"}}`)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxAttempts = 2
	d := New(cfg)
	_, err := d.Describe(context.Background(), redDataURL(t))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	for _, want := range []string{"status 403", "FreeTierError", "after 2 attempt(s)", "mimo-v2.6-flash-free"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestDescribeRejectsNonEventStreamResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"unexpected":"shape"}`)
	}))
	defer srv.Close()

	d := New(testConfig(srv.URL))
	if _, err := d.Describe(context.Background(), redDataURL(t)); err == nil || !strings.Contains(err.Error(), "content-type") {
		t.Fatalf("error = %v, want content-type complaint", err)
	}
}

func TestParseSSEJoinsContentAndSurfacesErrors(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"a red"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":" square"}}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	text, err := parseSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if text != "a red square" {
		t.Fatalf("text = %q, want %q", text, "a red square")
	}

	errStream := "data: {\"error\":{\"message\":\"rate limited\"}}\ndata: [DONE]\n"
	text, err = parseSSE(strings.NewReader(errStream))
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want rate limited", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
}

func TestLoadImage(t *testing.T) {
	ctx := context.Background()
	client := &http.Client{Timeout: 5 * time.Second}

	dataURL, err := loadImage(ctx, client, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(redPNG(t)))
	if err != nil {
		t.Fatalf("loadImage data URL: %v", err)
	}
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("dataURL prefix = %.40q", dataURL)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(redPNG(t))
	}))
	defer srv.Close()
	if _, err := loadImage(ctx, client, srv.URL+"/pic.png"); err != nil {
		t.Fatalf("loadImage http URL: %v", err)
	}

	if _, err := loadImage(ctx, client, "ftp://example.com/x.png"); err == nil || !strings.Contains(err.Error(), "unsupported image reference") {
		t.Fatalf("err = %v, want unsupported image reference", err)
	}

	if _, err := loadImage(ctx, client, ""); err == nil || !strings.Contains(err.Error(), "empty image reference") {
		t.Fatalf("err = %v, want empty image reference", err)
	}
}

func TestLoadImageRejectsNonImagePayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html>not an image</html>")
	}))
	defer srv.Close()
	if _, err := loadImage(context.Background(), &http.Client{Timeout: 5 * time.Second}, srv.URL); err == nil || !strings.Contains(err.Error(), "not an image") {
		t.Fatalf("err = %v, want not an image", err)
	}
}

// TestDescribeLive costs one real free-tier call. Opt in with:
//
//	OPENCODE_VISION_LIVE=1 go test -count=1 -run TestDescribeLive ./internal/vision/
func TestDescribeLive(t *testing.T) {
	if os.Getenv("OPENCODE_VISION_LIVE") != "1" {
		t.Skip("set OPENCODE_VISION_LIVE=1 to hit the real zen endpoint")
	}
	cfg := config.VisionConfig{
		Enabled:     true,
		BaseURL:     "https://opencode.ai/zen/v1",
		APIKey:      "public",
		UserAgent:   visionClientUA,
		Model:       "mimo-v2.6-flash-free",
		Prompt:      "Describe this image in one short sentence, including any visible color.",
		Timeout:     "90s",
		MaxAttempts: 3,
	}
	d := New(cfg)
	start := time.Now()
	text, err := d.Describe(context.Background(), redDataURL(t))
	if err != nil {
		t.Fatalf("live Describe: %v", err)
	}
	t.Logf("live describe in %s: %s", time.Since(start).Round(time.Millisecond), text)
	if !strings.Contains(strings.ToLower(text), "red") {
		t.Fatalf("description %q does not mention red", text)
	}
}
