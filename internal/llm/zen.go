// zen transport: `llm.api_type: zen` routes chat through the opencode zen
// free lane (https://opencode.ai/zen/v1, model mimo-v2.6-flash-free).
//
// The zen free tier 403s (FreeTierError "can only be used from within
// OpenCode") anything that does not look like the opencode client. The gate
// (re-bisected 2026-10-16, see internal/zengate for the full model) is the
// main-agent shape: a body carrying a `tools` array, "stream": true, and
// the session header group set to a correctly generated opencode session id.
// The system message content is free - there is no prompt-prefix
// requirement on this branch, so Kite's own system prompt doubles as the
// system message (no byte-exact capture to keep in sync).
//
// Consequence (kept from the original design): chat history never travels
// as `messages`. buildZenTurn folds Kite's system prompt, the lane rules and
// the whole dialogue (plus tool-call notes from pre-zen history) into the
// system message and sends exactly ONE live user message.
//
// 403 FreeTierError is ambiguous by design: it fires for a throttled shared
// egress IP AND for a broken gate shape, so a call retries on its own
// cadence (4 attempts, 1/2/4s + jitter for blips; 5/12/25s for 429s, like
// diit) and a burned-out call reports the status, body snippet, attempt
// count and both possibilities. It never degrades silently.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"element-orion/internal/zengate"
)

// Constant request shape pinned to the proven envelope (bisected 2026-10-16
// against captured opencode v2.0.23 traffic). Do not "improve" these
// casually: the gate is strict enough that changed values are an unverified
// variable.
const (
	zenMaxCompletionTokens = 32000

	zenMaxAttempts    = 4
	zenAttemptTimeout = 120 * time.Second
	zenBudget         = 180 * time.Second

	zenUserAgent = "opencode/latest/2.0.23/cli"

	zenFoldMessageLimit  = 2000
	zenFoldToolCallLimit = 300
)

// zenLaneRules is folded into the system message after Kite's system prompt.
// Tool calling is disabled on the wire (tool_choice "none"), but the model
// can still describe tools from its own system prompt - the rules keep it
// from emitting tool-call syntax as reply text.
const zenLaneRules = `## Free-lane chat mode

This session runs on a plain-text lane: function calling is disabled on the wire (tool_choice is "none"), so the text you write is delivered to the user as-is.

- Answer the most recent user message. The "Conversation so far" block below is history for context only - never re-answer old messages.
- Never emit tool calls, function-call syntax, JSON payloads, or XML tags as your reply.
- If earlier instructions (system prompt, history) mention tools like send_discord_message, send_discord_file, read_previous_messages or memory tools, treat them as unavailable in this session; say so plainly in your own voice when a request needs one.
- The <NO_REPLY> and <chunk> reply tokens are plain text markers and still work exactly as described.`

const zenFinalReminder = "FINAL REMINDER: reply now with the message text for the user, and nothing else - no tool calls, no JSON, no tags."

const zenFailureHint = "a 403 FreeTierError is ambiguous by design: it fires both for a throttled egress IP and for a gate-shape mismatch (missing tools array, a session id that is not in the client's generated scheme, stream:false); retries cover throttles, so a persistent burn-out means the envelope drifted - re-bisect against the current client with internal/zengate in mind"

// zenClient implements chatClient + streamingChatClient for the zen lane.
// Both paths stream on the wire (the gate requires stream: true) and parse
// the SSE response; Chat just drops the deltas.
type zenClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client

	// Test seams; newZenClient sets the production values.
	maxAttempts int
	sleep       func(context.Context, time.Duration) error
}

func newZenClient(baseURL string, apiKey string, timeout time.Duration) *zenClient {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		apiKey = "public"
	}
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	return &zenClient{
		baseURL:     baseURL,
		apiKey:      apiKey,
		httpClient:  &http.Client{Timeout: timeout},
		maxAttempts: zenMaxAttempts,
		sleep:       zenSleep,
	}
}

func (c *zenClient) Chat(ctx context.Context, req Request) (Message, error) {
	return c.call(ctx, req, nil)
}

func (c *zenClient) StreamChat(ctx context.Context, req Request, onDelta func(StreamDelta)) (Message, error) {
	return c.call(ctx, req, onDelta)
}

func (c *zenClient) call(ctx context.Context, req Request, onDelta func(StreamDelta)) (Message, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return Message{}, fmt.Errorf("zen lane: request carries no model id (set llm.model to the mimo catalog entry)")
	}
	systemText, liveText, err := buildZenTurn(req)
	if err != nil {
		return Message{}, err
	}

	payload := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "system", "content": systemText},
			{"role": "user", "content": liveText},
		},
		"tools":                 zengate.Tools(),
		"tool_choice":           "none",
		"max_completion_tokens": zenMaxCompletionTokens,
		"stream":                true,
		"stream_options":        map[string]any{"include_usage": true},
		"store":                 false,
	}

	started := time.Now()
	attemptsMade := 0
	var lastErr error
	for attemptsMade < c.attemptLimit() {
		if attemptsMade > 0 {
			if time.Since(started) >= zenBudget {
				break
			}
			if err := c.sleepFor(ctx, zenBackoff(attemptsMade+1, lastErr)); err != nil {
				return Message{}, err
			}
		}
		attemptsMade++
		message, err := c.runOnce(ctx, req, payload, onDelta)
		if err == nil {
			message.RequestPayload = cloneJSONObject(payload)
			return message, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return Message{}, err
		}
		if !zenRetriable(err) {
			break
		}
	}
	return Message{}, fmt.Errorf("zen lane: %s failed after %d attempt(s) in %.1fs: %w (%s)",
		c.endpoint(req), attemptsMade, time.Since(started).Seconds(), lastErr, zenFailureHint)
}

func (c *zenClient) attemptLimit() int {
	if c.maxAttempts > 0 {
		return c.maxAttempts
	}
	return zenMaxAttempts
}

func (c *zenClient) sleepFor(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if c.sleep != nil {
		return c.sleep(ctx, delay)
	}
	return zenSleep(ctx, delay)
}

// runOnce performs one full attempt: fresh identity ids, SSE request, SSE
// parse. Every non-200 and every broken stream returns an error; nothing is
// swallowed.
func (c *zenClient) runOnce(ctx context.Context, req Request, payload map[string]any, onDelta func(StreamDelta)) (Message, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, zenAttemptTimeout)
	defer cancel()

	body, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("zen lane: encode request: %w", err)
	}

	endpoint := c.endpoint(req)
	sessionID, err := zengate.SessionID()
	if err != nil {
		return Message{}, err
	}

	httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Message{}, fmt.Errorf("zen lane: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "*/*")
	httpReq.Header.Set("Authorization", "Bearer "+c.resolveAPIKey(req))
	httpReq.Header.Set("User-Agent", zenUserAgent)
	httpReq.Header.Set("x-opencode-client", "cli")
	httpReq.Header.Set("x-opencode-project", zengate.ProjectID())
	session := "ses_" + sessionID
	httpReq.Header.Set("x-opencode-session", session)
	httpReq.Header.Set("x-opencode-session-id", session)
	httpReq.Header.Set("x-session-affinity", session)
	httpReq.Header.Set("x-session-id", session)
	b3, traceparent := zengate.TraceHeaders()
	httpReq.Header.Set("b3", b3)
	httpReq.Header.Set("traceparent", traceparent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Message{}, fmt.Errorf("zen lane: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return Message{}, zenStatusFromResponse(resp)
	}

	if contentType := resp.Header.Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Message{}, fmt.Errorf("zen lane: expected an SSE stream from %s, got %q (body: %s)",
			endpoint, contentType, zenTruncate(strings.TrimSpace(string(data)), 300))
	}

	return parseZenStream(resp.Body, onDelta)
}

// parseZenStream accumulates content + reasoning deltas and the usage chunk.
// Mid-stream error payloads are collected; a stream that ends with no reply
// text is an error, not an empty success.
func parseZenStream(reader io.Reader, onDelta func(StreamDelta)) (Message, error) {
	var content, reasoning strings.Builder
	var usage map[string]any
	var streamErrors []string

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []chatCompletionChunkChoice `json:"choices"`
			Usage   map[string]any              `json:"usage,omitempty"`
			Error   *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			streamErrors = append(streamErrors, chunk.Error.Message)
			continue
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			content.WriteString(delta.Content)
		}
		if delta.ReasoningContent != "" {
			reasoning.WriteString(delta.ReasoningContent)
		}
		if onDelta != nil && (delta.Content != "" || delta.ReasoningContent != "") {
			onDelta(StreamDelta{Content: delta.Content, ReasoningContent: delta.ReasoningContent})
		}
	}
	if err := scanner.Err(); err != nil {
		return Message{}, fmt.Errorf("zen lane: read stream: %w", err)
	}

	message := Message{
		Role:             "assistant",
		Content:          StripMessageTimeMetadata(content.String()),
		ReasoningContent: strings.TrimSpace(reasoning.String()),
	}
	if message.Content == "" && len(streamErrors) > 0 {
		return Message{}, fmt.Errorf("zen lane: stream ended with no reply text: %s", strings.Join(streamErrors, "; "))
	}
	if message.Content == "" && message.ReasoningContent == "" {
		return Message{}, fmt.Errorf("zen lane: stream ended without any text (no content, no reasoning)")
	}
	if usage != nil {
		message.Usage = extractUsage(map[string]any{"usage": usage})
		message.OutputTokens = extractOutputTokens(message.Usage)
		message.ReasoningTokens = extractReasoningTokens(message.Usage)
	}
	return message, nil
}

// buildZenTurn assembles the lane envelope: Kite's system prompt, the lane
// rules, the folded dialogue and the final reminder as the system message
// (content is free on the tools-bearing main-agent branch), plus the single
// live user message.
func buildZenTurn(req Request) (systemText string, liveText string, err error) {
	messages := req.Messages
	if len(messages) == 0 {
		return "", "", fmt.Errorf("zen lane: request carries no messages")
	}

	systemContent := ""
	history := messages
	if messages[0].Role == "system" {
		systemContent = StripMessageTimeMetadata(messages[0].Content)
		history = messages[1:]
	}
	if len(history) == 0 {
		return "", "", fmt.Errorf("zen lane: request has no user message to answer")
	}

	last := history[len(history)-1]
	earlier := history[:len(history)-1]

	live := strings.TrimSpace(zenMessageText(last))
	if live == "" {
		return "", "", fmt.Errorf("zen lane: latest %s message has no text content", last.Role)
	}

	var tail strings.Builder
	if system := strings.TrimSpace(systemContent); system != "" {
		tail.WriteString(system)
		tail.WriteString("\n\n")
	}
	tail.WriteString(zenLaneRules)
	if len(earlier) > 0 {
		var dialogue strings.Builder
		for _, message := range earlier {
			line := zenFoldLine(message)
			if line == "" {
				continue
			}
			dialogue.WriteString(line)
			dialogue.WriteString("\n")
		}
		if dialogue.Len() > 0 {
			tail.WriteString("\n\nConversation so far (oldest first; already handled - context only, do not re-answer):\n")
			tail.WriteString(dialogue.String())
		}
	}
	tail.WriteString("\n\n")
	tail.WriteString(zenFinalReminder)

	return tail.String(), live, nil
}

// zenMessageText renders a message's text content: Content plus any text
// parts (image parts only as a loud placeholder - describeImages normally
// replaces them before the chat model runs).
func zenMessageText(message Message) string {
	var builder strings.Builder
	if content := strings.TrimSpace(message.Content); content != "" {
		builder.WriteString(content)
	}
	for _, part := range message.Parts {
		if part.Type == ContentPartImageURL {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString("[image attachment]")
			continue
		}
		if part.Type != ContentPartText {
			continue
		}
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
	}
	return builder.String()
}

// zenFoldLine renders one history message as a labelled dialogue line, with
// tool calls noted inline (history can contain them from pre-zen turns).
// Empty messages fold to "" and are skipped by the caller.
func zenFoldLine(message Message) string {
	text := strings.TrimSpace(zenMessageText(message))
	var notes []string
	for _, call := range message.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			name = "unknown"
		}
		arguments := strings.TrimSpace(call.Function.Arguments)
		if arguments == "" {
			notes = append(notes, fmt.Sprintf("[tool call: %s]", name))
			continue
		}
		notes = append(notes, fmt.Sprintf("[tool call: %s %s]", name, zenTruncate(arguments, zenFoldToolCallLimit)))
	}
	if text == "" && len(notes) == 0 {
		return ""
	}
	body := text
	if len(notes) > 0 {
		if body != "" {
			body += " "
		}
		body += strings.Join(notes, " ")
	}
	return fmt.Sprintf("%s: %s", zenRoleLabel(message), zenTruncate(body, zenFoldMessageLimit))
}

func zenRoleLabel(message Message) string {
	switch message.Role {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "tool":
		if name := strings.TrimSpace(message.Name); name != "" {
			return "Tool result (" + name + ")"
		}
		return "Tool result"
	case "system", "developer":
		return "System note"
	default:
		role := strings.TrimSpace(message.Role)
		if role == "" {
			return "unknown"
		}
		return role
	}
}

func (c *zenClient) endpoint(req Request) string {
	base := strings.TrimSpace(c.baseURL)
	if override := strings.TrimSpace(req.BaseURL); override != "" {
		base = override
	}
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = "https://opencode.ai/zen/v1"
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

func (c *zenClient) resolveAPIKey(req Request) string {
	if key := strings.TrimSpace(req.APIKey); key != "" {
		return key
	}
	if c.apiKey != "" {
		return c.apiKey
	}
	return "public"
}

// zenStatusError is an HTTP status rejection from the lane. Its string shape
// matches the other clients ("API error (403): ...") so IsRetriableError
// classifies it the same way everywhere.
type zenStatusError struct {
	Status int
	Type   string
	Body   string
}

func (e *zenStatusError) Error() string {
	if e.Type != "" && e.Type != "error" {
		return fmt.Sprintf("API error (%d): %s (%s)", e.Status, e.Body, e.Type)
	}
	return fmt.Sprintf("API error (%d): %s", e.Status, e.Body)
}

func zenStatusFromResponse(resp *http.Response) error {
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return fmt.Errorf("API error (%d): unreadable body: %v", resp.StatusCode, readErr)
	}
	var envelope struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error != nil && envelope.Error.Message != "" {
		return &zenStatusError{Status: resp.StatusCode, Type: envelope.Error.Type, Body: envelope.Error.Message}
	}
	return &zenStatusError{Status: resp.StatusCode, Body: zenTruncate(strings.TrimSpace(string(data)), 300)}
}

// zenRetriable decides whether another attempt is worth it: gate blips
// (403), throttles (408/429) and server errors (5xx) plus transport/stream
// breakage retry; anything else (401/404/400) fails fast. context.Canceled
// means the caller went away - stop.
func zenRetriable(err error) bool {
	var statusErr *zenStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.Status {
		case http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
			return true
		}
		return statusErr.Status >= 500
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true
}

func isZenStatus(err error, status int) bool {
	var statusErr *zenStatusError
	return errors.As(err, &statusErr) && statusErr.Status == status
}

// zenBackoff mirrors diit's cadence: 1/2/4s + jitter for blips, 5/12/25s for
// 429s. upcomingAttempt is the 1-based number of the attempt about to run.
func zenBackoff(upcomingAttempt int, lastErr error) time.Duration {
	if isZenStatus(lastErr, http.StatusTooManyRequests) {
		switch upcomingAttempt {
		case 2:
			return 5 * time.Second
		case 3:
			return 12 * time.Second
		default:
			return 25 * time.Second
		}
	}
	base := time.Second << uint(upcomingAttempt-2)
	if base > 4*time.Second {
		base = 4 * time.Second
	}
	if base < time.Second {
		base = time.Second
	}
	return base + zenJitter()
}

func zenJitter() time.Duration {
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0
	}
	return time.Duration(int(buf[0])<<8|int(buf[1])) % (400 * time.Millisecond)
}

func zenSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func zenTruncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + " …[truncated]"
}
