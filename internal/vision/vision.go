package vision

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"element-orion/internal/config"
)

// Describer turns an image into a text description by shelling out to the
// opencode CLI. opencode's free-tier models (mimo-v2.6-flash-free and
// friends) only answer requests made from within opencode itself, and only
// through the default agent, so there is no plain HTTP route to them.
type Describer struct {
	binary   string
	model    string
	prompt   string
	timeout  time.Duration
	attempts int
	client   *http.Client
}

const maxImageBytes = 25 << 20

func New(cfg config.VisionConfig) *Describer {
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 120 * time.Second
	}
	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	return &Describer{
		binary:   cfg.Binary,
		model:    cfg.Model,
		prompt:   cfg.Prompt,
		timeout:  timeout,
		attempts: attempts,
		client:   &http.Client{Timeout: timeout},
	}
}

// Describe returns a text description of imageURL (a data: URL or an
// http(s) URL). Every failure is returned as an error that names the model,
// the attempt count and the underlying cause.
func (d *Describer) Describe(ctx context.Context, imageURL string) (string, error) {
	imagePath, cleanup, err := materialize(ctx, d.client, imageURL)
	if err != nil {
		return "", fmt.Errorf("vision: could not read image: %w", err)
	}
	defer cleanup()

	var lastErr error
	for attempt := 1; attempt <= d.attempts; attempt++ {
		if attempt > 1 {
			if err := sleepBackoff(ctx, attempt); err != nil {
				return "", fmt.Errorf("vision: canceled while retrying %s: %w", d.model, err)
			}
		}
		text, runErr := d.runOnce(ctx, imagePath)
		if runErr == nil {
			return text, nil
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("vision: canceled while describing image with %s: %w", d.model, ctx.Err())
		}
		lastErr = runErr
	}
	return "", fmt.Errorf("vision: %s failed after %d attempt(s): %w", d.model, d.attempts, lastErr)
}

func (d *Describer) runOnce(ctx context.Context, imagePath string) (string, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	cmd := exec.CommandContext(attemptCtx, d.binary, "run", "-m", d.model, "--format", "json", d.prompt, "-f", imagePath)
	cmd.Dir = filepath.Dir(imagePath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	text, parseErr := parseRunOutput(stdout.String())
	if text != "" {
		return text, nil
	}
	if attemptCtx.Err() != nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("timed out after %s", d.timeout)
	}
	if parseErr != nil {
		return "", parseErr
	}
	if runErr != nil {
		return "", fmt.Errorf("%w (stderr: %s)", runErr, singleLine(stderr.String()))
	}
	return "", errors.New("opencode produced no text output (stdout: " + tail(stdout.String()) + ")")
}

type runEvent struct {
	Type string `json:"type"`
	Part *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"part"`
	Error *struct {
		Message string `json:"message"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// parseRunOutput reads the newline-delimited JSON event stream that
// `opencode run --format json` prints and returns the assistant text.
func parseRunOutput(output string) (string, error) {
	var builder strings.Builder
	var eventErrors []string

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event runEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		switch event.Type {
		case "text":
			if event.Part != nil && event.Part.Type == "text" && strings.TrimSpace(event.Part.Text) != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(strings.TrimSpace(event.Part.Text))
			}
		case "error":
			message := ""
			if event.Error != nil {
				message = strings.TrimSpace(event.Error.Data.Message)
				if message == "" {
					message = strings.TrimSpace(event.Error.Message)
				}
			}
			if message == "" {
				message = "unknown opencode error event"
			}
			eventErrors = append(eventErrors, message)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading opencode output: %w", err)
	}
	if text := strings.TrimSpace(builder.String()); text != "" {
		return text, nil
	}
	if len(eventErrors) > 0 {
		return "", errors.New(strings.Join(eventErrors, "; "))
	}
	return "", errors.New("opencode reported no text (raw output: " + tail(output) + ")")
}

func sleepBackoff(ctx context.Context, attempt int) error {
	delay := time.Second
	if attempt > 2 {
		delay = 3 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// materialize writes the image reference to a temp file so the opencode CLI
// can attach it with -f. Supports base64 data: URLs (what the discord
// bridge produces) and plain http(s) URLs.
func materialize(ctx context.Context, client *http.Client, imageURL string) (string, func(), error) {
	imageURL = strings.TrimSpace(imageURL)
	noop := func() {}
	if imageURL == "" {
		return "", noop, errors.New("empty image reference")
	}

	var payload []byte
	var contentType string

	switch {
	case strings.HasPrefix(imageURL, "data:"):
		meta, encoded, ok := strings.Cut(strings.TrimPrefix(imageURL, "data:"), ",")
		if !ok {
			return "", noop, errors.New("malformed data: URL (no comma separator)")
		}
		if !strings.HasSuffix(strings.ToLower(meta), ";base64") {
			return "", noop, errors.New("data: URL is not base64 encoded")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", noop, fmt.Errorf("decoding base64 image: %w", err)
		}
		payload = decoded
		contentType = strings.TrimSuffix(strings.ToLower(meta), ";base64")
	case strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
		if err != nil {
			return "", noop, fmt.Errorf("building image request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", noop, fmt.Errorf("fetching image: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", noop, fmt.Errorf("fetching image: unexpected status %s", resp.Status)
		}
		contentType = resp.Header.Get("Content-Type")
		payload, err = io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
		if err != nil {
			return "", noop, fmt.Errorf("reading image body: %w", err)
		}
		if len(payload) > maxImageBytes {
			return "", noop, fmt.Errorf("image exceeds the %d byte vision cap", maxImageBytes)
		}
	default:
		return "", noop, fmt.Errorf("unsupported image reference %q (want a data: or http(s) URL)", truncate(imageURL, 80))
	}

	if len(payload) == 0 {
		return "", noop, errors.New("image payload is empty")
	}

	dir, err := os.MkdirTemp("", "lumen-vision-")
	if err != nil {
		return "", noop, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	path := filepath.Join(dir, "image"+extensionFor(imageURL, contentType))
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("writing temp image: %w", err)
	}
	return path, cleanup, nil
}

func extensionFor(imageURL string, contentType string) string {
	if parsed, err := url.Parse(imageURL); err == nil {
		if ext := strings.ToLower(filepath.Ext(parsed.Path)); ext != "" {
			switch ext {
			case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
				return ext
			}
		}
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/png", "":
		return ".png"
	}
	return ".png"
}

func tail(value string) string {
	value = singleLine(value)
	return truncate(value, 400)
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
