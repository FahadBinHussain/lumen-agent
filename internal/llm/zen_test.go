package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"element-orion/internal/zengate"
)

// zenIdentityPattern pins the opencode identity id shape: 12 hex + 14 base62
// after the ses_/msg_ prefix.
var zenIdentityPattern = regexp.MustCompile(`^(ses|msg)_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

func zenTestServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
			return
		}
		handler(w, r, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func zenTestWriteSSE(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, chunk := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func zenTestWrite403(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprint(w, `{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}`)
}

func TestZenChatSendsGateEnvelopeAndParsesReply(t *testing.T) {
	var gotPath, gotAuth, gotClient, gotSession, gotRequest string
	server := zenTestServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotClient = r.Header.Get("x-opencode-client")
		gotSession = r.Header.Get("x-opencode-session")
		gotRequest = r.Header.Get("x-opencode-request")

		if model, _ := body["model"].(string); model != "mimo-v2.6-flash-free" {
			t.Errorf("model = %q", model)
		}
		if stream, _ := body["stream"].(bool); !stream {
			t.Errorf("stream must be true, got %v", body["stream"])
		}
		if _, ok := body["stream_options"]; !ok {
			t.Errorf("stream_options.include_usage must be present")
		}
		if maxTokens, _ := body["max_tokens"].(float64); int(maxTokens) != zenMaxTokens {
			t.Errorf("max_tokens = %v, want %d", body["max_tokens"], zenMaxTokens)
		}
		if _, ok := body["tools"]; ok {
			t.Errorf("tools must be omitted on the zen lane")
		}

		messages, _ := body["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages len = %d, want 2 (system + one live user)", len(messages))
		}
		system, _ := messages[0].(map[string]any)
		systemText, _ := system["content"].(string)
		if !strings.HasPrefix(systemText, zengate.SystemPrompt()) {
			t.Errorf("system message must start with the byte-exact gate prompt")
		}
		for _, want := range []string{"You are Kite.", "Free-lane chat mode", "User: hey there", "Assistant: hi!", "FINAL REMINDER"} {
			if !strings.Contains(systemText, want) {
				t.Errorf("system tail missing %q", want)
			}
		}
		user, _ := messages[1].(map[string]any)
		if user["role"] != "user" || user["content"] != "what's up" {
			t.Errorf("live message = %v/%v, want user/what's up", user["role"], user["content"])
		}

		zenTestWriteSSE(w,
			`{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
			`{"choices":[{"delta":{"content":"All"}}]}`,
			`{"choices":[{"delta":{"content":" good"}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`,
		)
	})

	client := NewClient(server.URL, "", APITypeZen, nil, false, false, 0)
	response, err := client.Chat(context.Background(), Request{
		Model: "mimo-v2.6-flash-free",
		Tools: []ToolDefinition{{Type: "function", Function: ToolFunctionDefinition{Name: "send_discord_message"}}},
		Messages: []Message{
			{Role: "system", Content: "You are Kite."},
			{Role: "user", Content: "hey there"},
			{Role: "assistant", Content: "hi!"},
			{Role: "user", Content: "what's up"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if response.Content != "All good" {
		t.Errorf("content = %q, want %q", response.Content, "All good")
	}
	if response.ReasoningContent != "thinking" {
		t.Errorf("reasoning = %q, want thinking", response.ReasoningContent)
	}
	if response.OutputTokens != 2 {
		t.Errorf("output tokens = %d, want 2", response.OutputTokens)
	}
	if response.RequestPayload == nil {
		t.Errorf("RequestPayload must be captured for auditing")
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer public" {
		t.Errorf("auth = %q, want Bearer public", gotAuth)
	}
	if gotClient != "cli" {
		t.Errorf("x-opencode-client = %q", gotClient)
	}
	if !zenIdentityPattern.MatchString(gotSession) || !strings.HasPrefix(gotSession, "ses_") {
		t.Errorf("x-opencode-session = %q", gotSession)
	}
	if !zenIdentityPattern.MatchString(gotRequest) || !strings.HasPrefix(gotRequest, "msg_") {
		t.Errorf("x-opencode-request = %q", gotRequest)
	}
}

func TestZenStreamChatForwardsDeltas(t *testing.T) {
	server := zenTestServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		zenTestWriteSSE(w,
			`{"choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
			`{"choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
		)
	})

	client := NewClient(server.URL, "", APITypeZen, nil, false, false, 0)
	var deltas []StreamDelta
	message, err := client.StreamChat(context.Background(), Request{
		Model:    "mimo-v2.6-flash-free",
		Messages: []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}},
	}, func(delta StreamDelta) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if len(deltas) != 3 {
		t.Fatalf("deltas = %d, want 3: %+v", len(deltas), deltas)
	}
	if deltas[0].ReasoningContent != "hmm" || deltas[1].Content != "Hel" || deltas[2].Content != "lo" {
		t.Errorf("deltas out of order: %+v", deltas)
	}
	if message.Content != "Hello" {
		t.Errorf("content = %q", message.Content)
	}
}

func TestZenRetriesGateBlipsThenSucceeds(t *testing.T) {
	attempts := 0
	server := zenTestServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		attempts++
		if attempts < 3 {
			zenTestWrite403(w)
			return
		}
		zenTestWriteSSE(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
	})

	client := NewClient(server.URL, "", APITypeZen, nil, false, false, 0)
	impl := client.impl.(*zenClient)
	var sleeps []time.Duration
	impl.sleep = func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	}

	message, err := client.Chat(context.Background(), Request{
		Model:    "mimo-v2.6-flash-free",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if attempts != 3 {
		t.Errorf("server attempts = %d, want 3", attempts)
	}
	if len(sleeps) != 2 {
		t.Errorf("sleeps = %d, want 2 (between the three attempts)", len(sleeps))
	}
	if message.Content != "ok" {
		t.Errorf("content = %q", message.Content)
	}
}

func TestZen403BurnoutIsLoudAndNotRetriableAtAgentLayer(t *testing.T) {
	server := zenTestServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		zenTestWrite403(w)
	})

	client := NewClient(server.URL, "", APITypeZen, nil, false, false, 0)
	impl := client.impl.(*zenClient)
	impl.maxAttempts = 2
	impl.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.Chat(context.Background(), Request{
		Model:    "mimo-v2.6-flash-free",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("want an error after burning every attempt")
	}
	for _, want := range []string{"API error (403)", "FreeTierError", "2 attempt(s)", "throttled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	if IsRetriableError(err) {
		t.Errorf("a burned-out 403 must not re-retry at the agent layer: %v", err)
	}
}

func TestZen429BurnoutIsRetriableAtAgentLayer(t *testing.T) {
	server := zenTestServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	})

	client := NewClient(server.URL, "", APITypeZen, nil, false, false, 0)
	impl := client.impl.(*zenClient)
	impl.maxAttempts = 2
	impl.sleep = func(context.Context, time.Duration) error { return nil }

	_, err := client.Chat(context.Background(), Request{
		Model:    "mimo-v2.6-flash-free",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "API error (429)") {
		t.Errorf("error = %v", err)
	}
	if !IsRetriableError(err) {
		t.Errorf("429 burn-out must stay retriable at the agent layer: %v", err)
	}
}

func TestZenBackoffCadence(t *testing.T) {
	throttle := &zenStatusError{Status: http.StatusTooManyRequests, Body: "slow down"}
	if got := zenBackoff(2, throttle); got != 5*time.Second {
		t.Errorf("429 attempt 2 backoff = %v, want 5s", got)
	}
	if got := zenBackoff(3, throttle); got != 12*time.Second {
		t.Errorf("429 attempt 3 backoff = %v, want 12s", got)
	}
	if got := zenBackoff(4, throttle); got != 25*time.Second {
		t.Errorf("429 attempt 4 backoff = %v, want 25s", got)
	}

	blip := &zenStatusError{Status: http.StatusForbidden, Body: "x"}
	if got := zenBackoff(2, blip); got < time.Second || got > 1400*time.Millisecond {
		t.Errorf("blip attempt 2 backoff = %v, want ~1s + jitter", got)
	}
	if got := zenBackoff(4, blip); got < 4*time.Second || got > 4400*time.Millisecond {
		t.Errorf("blip attempt 4 backoff = %v, want ~4s + jitter", got)
	}
}

func TestZenRetriableClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"403 gate blip", &zenStatusError{Status: http.StatusForbidden, Body: "x"}, true},
		{"429 throttle", &zenStatusError{Status: http.StatusTooManyRequests, Body: "x"}, true},
		{"503 server", &zenStatusError{Status: http.StatusServiceUnavailable, Body: "x"}, true},
		{"401 auth", &zenStatusError{Status: http.StatusUnauthorized, Body: "x"}, false},
		{"400 bad request", &zenStatusError{Status: http.StatusBadRequest, Body: "x"}, false},
		{"caller canceled", context.Canceled, false},
		{"transport error", fmt.Errorf("zen lane: send request: connection reset by peer"), true},
	}
	for _, tc := range cases {
		if got := zenRetriable(tc.err); got != tc.want {
			t.Errorf("%s: zenRetriable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestZenBuildTurnRejectsEmptyRequests(t *testing.T) {
	if _, _, err := buildZenTurn(Request{}); err == nil {
		t.Error("empty request must fail loudly")
	}
	if _, _, err := buildZenTurn(Request{Messages: []Message{{Role: "system", Content: "sys"}}}); err == nil {
		t.Error("system-only request must fail loudly (nothing to answer)")
	}
	if _, _, err := buildZenTurn(Request{Messages: []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "   "},
	}}); err == nil {
		t.Error("blank latest message must fail loudly")
	}
}

func TestZenBuildTurnUsesPartsTextForLiveMessage(t *testing.T) {
	system, live, err := buildZenTurn(Request{Messages: []Message{
		{Role: "system", Content: "You are Kite."},
		{Role: "user", Content: "", Parts: []ContentPart{
			{Type: ContentPartText, Text: "[attached image 1 of 1] a red square"},
		}},
	}})
	if err != nil {
		t.Fatalf("buildZenTurn: %v", err)
	}
	if live != "[attached image 1 of 1] a red square" {
		t.Errorf("live = %q (vision descriptions must become the live message)", live)
	}
	if !strings.HasPrefix(system, zengate.SystemPrompt()) {
		t.Errorf("system must still start with the gate prompt")
	}
}

func TestZenEmptyStreamIsAnError(t *testing.T) {
	server := zenTestServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		zenTestWriteSSE(w)
	})

	client := NewClient(server.URL, "", APITypeZen, nil, false, false, 0)
	impl := client.impl.(*zenClient)
	impl.maxAttempts = 1

	_, err := client.Chat(context.Background(), Request{
		Model:    "mimo-v2.6-flash-free",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "without any text") {
		t.Fatalf("empty stream must be a loud error, got %v", err)
	}
}

func TestZenRequiresModelID(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "", APITypeZen, nil, false, false, 0)
	_, err := client.Chat(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "model id") {
		t.Fatalf("missing model id must fail before any network call, got %v", err)
	}
}

// TestZenChatLive burns one real free-tier call against the zen endpoint.
// Gated so the offline suite stays network-free; run with
// OPENCODE_ZEN_LIVE=1 go test -count=1 -run TestZenChatLive ./internal/llm/.
// A 403 here is AMBIGUOUS (egress throttle vs shape) - the whole point of
// the client's retry cadence; it does not by itself prove a shape problem.
func TestZenChatLive(t *testing.T) {
	if os.Getenv("OPENCODE_ZEN_LIVE") != "1" {
		t.Skip("set OPENCODE_ZEN_LIVE=1 to burn a real free-tier call")
	}
	client := NewClient("https://opencode.ai/zen/v1", "public", APITypeZen, nil, false, false, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	message, err := client.Chat(ctx, Request{
		Model: "mimo-v2.6-flash-free",
		Messages: []Message{
			{Role: "system", Content: "You are a terse test bot."},
			{Role: "user", Content: "Reply with exactly one word: pong"},
		},
	})
	if err != nil {
		t.Fatalf("live zen chat failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(message.Content), "pong") {
		t.Fatalf("live reply = %q, want it to contain pong", message.Content)
	}
	t.Logf("live zen reply: %q (tokens=%d, reasoning=%q)", message.Content, message.OutputTokens, message.ReasoningContent)
}
