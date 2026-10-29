package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type messengerToolContextKey struct{}

type MessengerToolContext struct {
	ThreadID  string
	UserID    string
	MessageID string
}

func WithMessengerToolContext(ctx context.Context, metadata MessengerToolContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, messengerToolContextKey{}, metadata)
}

func MessengerToolContextFromContext(ctx context.Context) (MessengerToolContext, bool) {
	if ctx == nil {
		return MessengerToolContext{}, false
	}
	metadata, ok := ctx.Value(messengerToolContextKey{}).(MessengerToolContext)
	return metadata, ok
}

type MessengerReactionSender interface {
	SendReaction(ctx context.Context, threadID int64, messageID string, reaction string) error
}

func (r *Registry) SetMessengerReactionSender(sender MessengerReactionSender) {
	r.messengerReactionSender = sender
}

func (r *Registry) registerMessengerTool() {
	r.register(
		"add_messenger_reaction",
		"Add a reaction to a Messenger message in the current thread or an allowlisted thread.",
		objectSchema(map[string]any{
			"emoji":      stringSchema("Unicode emoji to react with."),
			"message_id": stringSchema("Optional target message ID. Defaults to the triggering message when available."),
			"thread_id":  stringSchema("Optional target thread ID."),
		}, "emoji"),
		r.handleAddMessengerReaction,
	)
}

func (r *Registry) handleAddMessengerReaction(ctx context.Context, payload json.RawMessage) (string, error) {
	type args struct {
		Emoji     string `json:"emoji"`
		MessageID string `json:"message_id"`
		ThreadID  string `json:"thread_id"`
	}

	var input args
	if err := decodeArgs(payload, &input); err != nil {
		return "", err
	}

	emoji := strings.TrimSpace(input.Emoji)
	if emoji == "" {
		return "", fmt.Errorf("emoji must not be empty")
	}

	metadata, _ := MessengerToolContextFromContext(ctx)
	threadIDStr := strings.TrimSpace(input.ThreadID)
	if threadIDStr == "" {
		threadIDStr = strings.TrimSpace(metadata.ThreadID)
	}
	if threadIDStr == "" {
		return "", fmt.Errorf("thread_id is required when no active Messenger thread context is available")
	}
	if !r.cfg.MessengerThreadAllowedTarget(threadIDStr, metadata.ThreadID) {
		return "", fmt.Errorf("thread_id %q is not allowed for outbound Messenger actions", threadIDStr)
	}

	threadID, err := strconv.ParseInt(threadIDStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid thread_id %q: %w", threadIDStr, err)
	}

	messageID := strings.TrimSpace(input.MessageID)
	if messageID == "" {
		messageID = strings.TrimSpace(metadata.MessageID)
	}
	if messageID == "" {
		return "", fmt.Errorf("message_id is required when no active Messenger message context is available")
	}

	if r.messengerReactionSender == nil {
		return "", fmt.Errorf("messenger reaction client is not configured or available")
	}

	if err := r.messengerReactionSender.SendReaction(ctx, threadID, messageID, emoji); err != nil {
		return "", fmt.Errorf("send messenger reaction: %w", err)
	}

	return jsonResult(map[string]any{
		"thread_id":  threadIDStr,
		"message_id": messageID,
		"emoji":      emoji,
	})
}
