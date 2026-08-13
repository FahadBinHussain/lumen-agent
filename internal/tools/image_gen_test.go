package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"element-orion/internal/config"
)

func TestGenerateImageMissingPrompt(t *testing.T) {
	registry := &Registry{cfg: config.Config{LLM: config.LLMConfig{APIKey: "test"}}}
	result, err := registry.handleGenerateImage(context.Background(), json.RawMessage(`{"prompt":"  "}`))
	if err == nil || !strings.Contains(err.Error(), "prompt must not be empty") {
		t.Fatalf("expected empty-prompt error, got %v / %q", err, result)
	}
}

func TestGenerateImageRequiresAPIKey(t *testing.T) {
	registry := &Registry{cfg: config.Config{LLM: config.LLMConfig{APIKeyEnv: "ELEMENT_ORION_TEST_NO_SUCH_KEY"}}}
	t.Setenv("ELEMENT_ORION_TEST_NO_SUCH_KEY", "")
	_, err := registry.handleGenerateImage(context.Background(), json.RawMessage(`{"prompt":"a cat"}`))
	if err == nil || !strings.Contains(err.Error(), "environment variable") {
		t.Fatalf("expected api key error, got %v", err)
	}
}

func TestGenerateImageWritesFile(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "gen/model" || body.Prompt != "a cat" {
			t.Fatalf("unexpected body %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"b64_json":"aSBhbSBhIHBORyBpbWFnZQ=="}]}`))
	}))
	defer srv.Close()

	registry := &Registry{
		root: dir,
		cfg: config.Config{
			LLM: config.LLMConfig{
				BaseURL: srv.URL + "/v1",
				APIKey:  "test",
			},
			ImageGen: config.ImageGenConfig{
				Enabled:   true,
				Model:     "gen/model",
				OutputDir: "out",
			},
		},
	}
	result, err := registry.handleGenerateImage(context.Background(), json.RawMessage(`{"prompt":"a cat"}`))
	if err != nil {
		t.Fatalf("handleGenerateImage: %v", err)
	}
	if !strings.Contains(result, "out/gen_") {
		t.Fatalf("expected saved path in result, got %q", result)
	}
}