package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"element-orion/internal/config"
)

func TestParseRunOutputReturnsText(t *testing.T) {
	output := strings.Join([]string{
		`{"type":"step_start","timestamp":1,"part":{"type":"step-start"}}`,
		`{"type":"text","timestamp":2,"part":{"type":"text","text":"BANANA 42"}}`,
		`{"type":"step_finish","timestamp":3,"part":{"type":"step-finish"}}`,
	}, "\n")

	text, err := parseRunOutput(output)
	if err != nil {
		t.Fatalf("parseRunOutput returned error: %v", err)
	}
	if text != "BANANA 42" {
		t.Fatalf("got %q, want %q", text, "BANANA 42")
	}
}

func TestParseRunOutputJoinsMultipleTextEvents(t *testing.T) {
	output := strings.Join([]string{
		`{"type":"text","part":{"type":"text","text":"first line"}}`,
		`{"type":"text","part":{"type":"text","text":"second line"}}`,
	}, "\n")

	text, err := parseRunOutput(output)
	if err != nil {
		t.Fatalf("parseRunOutput returned error: %v", err)
	}
	if text != "first line\nsecond line" {
		t.Fatalf("got %q", text)
	}
}

func TestParseRunOutputSurfacesFreeTierError(t *testing.T) {
	output := `{"type":"error","error":{"name":"APIError","data":{"message":"Error from provider (Console): OpenCode's free tier can only be used from within OpenCode","statusCode":403}}}`

	text, err := parseRunOutput(output)
	if err == nil {
		t.Fatalf("expected an error, got text %q", text)
	}
	if !strings.Contains(err.Error(), "free tier") {
		t.Fatalf("error should name the free tier rejection, got: %v", err)
	}
}

func TestParseRunOutputWithoutEventsFailsLoudly(t *testing.T) {
	if _, err := parseRunOutput("complete garbage"); err == nil {
		t.Fatal("expected an error for output with no events")
	}
}

func TestMaterializeDataURL(t *testing.T) {
	payload := []byte("fake-png-bytes")
	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(payload)

	path, cleanup, err := materialize(context.Background(), httpDefaultClient(), imageURL)
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	defer cleanup()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading temp image: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("temp image payload mismatch: %q", got)
	}
	if filepath.Ext(path) != ".png" {
		t.Fatalf("expected .png extension, got %q", path)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove the temp dir, stat err = %v", err)
	}
}

func TestMaterializeRejectsUnsupportedSources(t *testing.T) {
	for _, imageURL := range []string{"", "   ", "ftp://example.com/img.png", "data:image/png;notbase64,AAAA"} {
		if _, _, err := materialize(context.Background(), httpDefaultClient(), imageURL); err == nil {
			t.Fatalf("expected an error for %q", imageURL)
		}
	}
}

func TestNewAppliesDefaultsAndValidationTimeouts(t *testing.T) {
	describer := New(config.VisionConfig{Timeout: "bogus", MaxAttempts: 0})
	if describer.timeout != 120*time.Second {
		t.Fatalf("invalid timeout should fall back to 120s, got %s", describer.timeout)
	}
	if describer.attempts != 3 {
		t.Fatalf("non-positive attempts should fall back to 3, got %d", describer.attempts)
	}
}

// TestDescribeLive proves the whole path against the real opencode CLI.
// Opt in with OPENCODE_VISION_LIVE=1 (it costs a free-tier model call).
func TestDescribeLive(t *testing.T) {
	if os.Getenv("OPENCODE_VISION_LIVE") != "1" {
		t.Skip("set OPENCODE_VISION_LIVE=1 to run the live opencode vision test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Fatalf("opencode is not on PATH: %v", err)
	}

	describer := New(config.VisionConfig{
		Binary:      "opencode",
		Model:       "opencode/mimo-v2.6-flash-free",
		Prompt:      "What single solid color fills this image? Answer with only the color name.",
		Timeout:     "120s",
		MaxAttempts: 3,
	})

	description, err := describer.Describe(context.Background(), solidColorDataURL(t, color.RGBA{R: 220, G: 20, B: 20, A: 255}))
	if err != nil {
		t.Fatalf("Describe failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(description), "red") {
		t.Fatalf("description should name the red background, got %q", description)
	}
}

func httpDefaultClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func solidColorDataURL(t *testing.T, fill color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encoding probe png: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes())
}
