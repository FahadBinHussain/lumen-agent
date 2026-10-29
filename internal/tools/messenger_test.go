package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"element-orion/internal/config"
)

type mockMessengerReactionSender struct {
	lastThreadID  int64
	lastMessageID string
	lastReaction  string
	err           error
}

func (m *mockMessengerReactionSender) SendReaction(ctx context.Context, threadID int64, messageID string, reaction string) error {
	m.lastThreadID = threadID
	m.lastMessageID = messageID
	m.lastReaction = reaction
	return m.err
}

func TestHandleAddMessengerReactionUsesCurrentMessageContext(t *testing.T) {
	mock := &mockMessengerReactionSender{}
	registry := &Registry{
		cfg: config.Config{
			Messenger: config.MessengerConfig{
				AllowedThreadIDs: []string{"984803114200952"},
			},
		},
		messengerReactionSender: mock,
	}

	ctx := WithMessengerToolContext(context.Background(), MessengerToolContext{
		ThreadID:  "984803114200952",
		MessageID: "msg-test-123",
	})

	result, err := registry.handleAddMessengerReaction(ctx, json.RawMessage(`{"emoji":"👍"}`))
	if err != nil {
		t.Fatalf("handleAddMessengerReaction returned error: %v", err)
	}

	if mock.lastThreadID != 984803114200952 {
		t.Errorf("expected thread ID 984803114200952, got %d", mock.lastThreadID)
	}
	if mock.lastMessageID != "msg-test-123" {
		t.Errorf("expected message ID msg-test-123, got %s", mock.lastMessageID)
	}
	if mock.lastReaction != "👍" {
		t.Errorf("expected reaction 👍, got %s", mock.lastReaction)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if decoded["thread_id"] != "984803114200952" || decoded["message_id"] != "msg-test-123" || decoded["emoji"] != "👍" {
		t.Errorf("unexpected decoded result: %v", decoded)
	}
}

func TestHandleAddMessengerReactionExplicitThreadAndMessage(t *testing.T) {
	mock := &mockMessengerReactionSender{}
	registry := &Registry{
		cfg:                     config.Config{},
		messengerReactionSender: mock,
	}

	payload := `{"thread_id":"123456789","message_id":"mid.123","emoji":"❤️"}`
	result, err := registry.handleAddMessengerReaction(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("handleAddMessengerReaction returned error: %v", err)
	}

	if mock.lastThreadID != 123456789 || mock.lastMessageID != "mid.123" || mock.lastReaction != "❤️" {
		t.Errorf("unexpected call to sender: thread=%d, msg=%s, react=%s", mock.lastThreadID, mock.lastMessageID, mock.lastReaction)
	}
	if !strings.Contains(result, `"emoji": "❤️"`) {
		t.Errorf("expected emoji in result: %s", result)
	}
}

func TestHandleAddMessengerReactionMissingContextErrors(t *testing.T) {
	registry := &Registry{
		cfg:                     config.Config{},
		messengerReactionSender: &mockMessengerReactionSender{},
	}

	// No thread ID
	_, err := registry.handleAddMessengerReaction(context.Background(), json.RawMessage(`{"emoji":"👍","message_id":"123"}`))
	if err == nil || !strings.Contains(err.Error(), "thread_id is required") {
		t.Errorf("expected thread_id error, got: %v", err)
	}

	// With thread ID but no message ID
	ctx := WithMessengerToolContext(context.Background(), MessengerToolContext{ThreadID: "123"})
	_, err = registry.handleAddMessengerReaction(ctx, json.RawMessage(`{"emoji":"👍"}`))
	if err == nil || !strings.Contains(err.Error(), "message_id is required") {
		t.Errorf("expected message_id error, got: %v", err)
	}
}

func TestHandleAddMessengerReactionEmptyEmoji(t *testing.T) {
	registry := &Registry{
		cfg:                     config.Config{},
		messengerReactionSender: &mockMessengerReactionSender{},
	}

	ctx := WithMessengerToolContext(context.Background(), MessengerToolContext{
		ThreadID:  "123",
		MessageID: "msg-1",
	})
	_, err := registry.handleAddMessengerReaction(ctx, json.RawMessage(`{"emoji":"   "}`))
	if err == nil || !strings.Contains(err.Error(), "emoji must not be empty") {
		t.Errorf("expected emoji empty error, got: %v", err)
	}
}

func TestHandleAddMessengerReactionUnallowedThread(t *testing.T) {
	registry := &Registry{
		cfg: config.Config{
			Messenger: config.MessengerConfig{
				AllowedThreadIDs: []string{"111"},
			},
		},
		messengerReactionSender: &mockMessengerReactionSender{},
	}

	_, err := registry.handleAddMessengerReaction(context.Background(), json.RawMessage(`{"thread_id":"222","message_id":"msg-1","emoji":"👍"}`))
	if err == nil || !strings.Contains(err.Error(), "not allowed for outbound Messenger actions") {
		t.Errorf("expected allowlist error, got: %v", err)
	}
}

func TestHandleAddMessengerReactionNoSenderConfigured(t *testing.T) {
	registry := &Registry{
		cfg: config.Config{},
	}

	ctx := WithMessengerToolContext(context.Background(), MessengerToolContext{
		ThreadID:  "123",
		MessageID: "msg-1",
	})
	_, err := registry.handleAddMessengerReaction(ctx, json.RawMessage(`{"emoji":"👍"}`))
	if err == nil || !strings.Contains(err.Error(), "messenger reaction client is not configured") {
		t.Errorf("expected no sender error, got: %v", err)
	}
}

func TestHandleAddMessengerReactionSenderError(t *testing.T) {
	mock := &mockMessengerReactionSender{err: errors.New("network timeout")}
	registry := &Registry{
		cfg:                     config.Config{},
		messengerReactionSender: mock,
	}

	ctx := WithMessengerToolContext(context.Background(), MessengerToolContext{
		ThreadID:  "123",
		MessageID: "msg-1",
	})
	_, err := registry.handleAddMessengerReaction(ctx, json.RawMessage(`{"emoji":"👍"}`))
	if err == nil || !strings.Contains(err.Error(), "send messenger reaction: network timeout") {
		t.Errorf("expected wrapped sender error, got: %v", err)
	}
}
