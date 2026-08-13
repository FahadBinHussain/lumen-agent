package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (r *Registry) registerImageGenTool() {
	if !r.cfg.ImageGen.Enabled {
		return
	}

	r.register(
		"generate_image",
		"Generate an image from a text prompt using the configured image model. Returns the output file path (or URL) of the generated image.",
		objectSchema(map[string]any{
			"prompt": stringSchema("What image to generate."),
		}, "prompt"),
		r.handleGenerateImage,
	)
}

func (r *Registry) handleGenerateImage(ctx context.Context, payload json.RawMessage) (string, error) {
	type args struct {
		Prompt string `json:"prompt"`
	}

	var input args
	if err := decodeArgs(payload, &input); err != nil {
		return "", err
	}

	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt must not be empty")
	}

	apiKey, err := r.cfg.ResolveAPIKey()
	if err != nil {
		return "", err
	}

	baseURL := strings.TrimRight(r.cfg.LLM.BaseURL, "/")
	body, err := json.Marshal(map[string]any{
		"model":  r.cfg.ImageGen.Model,
		"prompt": prompt,
		"n":      1,
	})
	if err != nil {
		return "", fmt.Errorf("encode image request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build image request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("generate image: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read image response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("generate image: API error (%d): %s", resp.StatusCode, truncateBytes(string(raw), 200))
	}

	var parsed struct {
		Error any `json:"error"`
		Data  []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode image response: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("generate image: %v", parsed.Error)
	}
	if len(parsed.Data) == 0 {
		return "", fmt.Errorf("generate image: no image data in response")
	}

	item := parsed.Data[0]

	if url := strings.TrimSpace(item.URL); url != "" {
		return fmt.Sprintf("image generated: %s", url), nil
	}

	data, err := base64.StdEncoding.DecodeString(item.B64JSON)
	if err != nil {
		return "", fmt.Errorf("decode image base64: %w", err)
	}

	ext := ".png"
	if contentType := mime.TypeByExtension(ext); contentType == "" {
		ext = ".png"
	}

	outputDir := r.cfg.ImageGen.OutputDir
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(r.root, outputDir)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	filename := fmt.Sprintf("gen_%d%s", time.Now().UnixMilli(), ext)
	path := filepath.Join(outputDir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write generated image: %w", err)
	}

	return fmt.Sprintf("image generated and saved to %s (%d bytes)", r.relPath(path), len(data)), nil
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}