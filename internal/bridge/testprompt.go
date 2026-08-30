package bridge

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"element-orion/internal/agent"
)

// testPromptRequest is the body for POST /api/test/prompt. It runs the REAL
// agent path (same runner, same tools, same memory/session persistence) and
// returns the final reply as JSON — for testing tool usage and the agent loop
// without needing to message the bot on a connected platform.
type testPromptRequest struct {
	Prompt   string `json:"prompt"`
	Platform string `json:"platform"`
	ThreadID string `json:"threadId"`
	// ChannelID optionally injects a real Discord channel context so
	// Discord-context tools (send_discord_message, background tasks, heartbeat
	// wakeups, etc.) work from the test route the same way they do when the
	// bot is actually triggered in a Discord channel.
	ChannelID string `json:"channelId"`
	GuildID   string `json:"guildId"`
	UserID    string `json:"userId"`
}

// handleTestPrompt is secret-gated like the notifications endpoints. It runs a
// prompt through the agent synchronously and returns {"reply": "..."}.
func (s *Service) handleTestPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req testPromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	platform := strings.TrimSpace(req.Platform)
	if platform == "" {
		platform = "test"
	}
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		threadID = "test"
	}

	reply, err := s.runAgentPrompt(r.Context(), platform, threadID, req.Prompt, req.GuildID, req.ChannelID, req.UserID)
	if err != nil {
		log.Printf("bridge: test prompt failed (%s %s): %v", platform, threadID, err)
		http.Error(w, "agent run failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"reply": reply}); err != nil {
		log.Printf("bridge: test prompt encode response: %v", err)
	}
}

// runAgentPrompt runs a single prompt through the shared agent Runner (the
// same path messenger/whatsapp/discord use) and returns the final assistant
// content. It mirrors agentRun's history/compaction/memory handling but does
// NOT send anything to a platform — the reply is returned to the caller.
func (s *Service) runAgentPrompt(ctx context.Context, platform, threadID, prompt, guildID, channelID, userID string) (string, error) {
	key := platform + ":" + threadID

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// When a Discord channel is provided, inject the same Discord tool context
	// the discordbot platform injects on real triggers, so Discord-context
	// tools (send_discord_message, background tasks, heartbeat wakeups) work
	// identically from the test route.
	if strings.TrimSpace(channelID) != "" {
		runCtx = tools.WithDiscordToolContext(runCtx, tools.DiscordToolContext{
			GuildID:   strings.TrimSpace(guildID),
			ChannelID: strings.TrimSpace(channelID),
			UserID:    strings.TrimSpace(userID),
		})
	}

	s.mu.Lock()
	history := cloneMessages(s.sessions[key])
	s.mu.Unlock()

	conversation := agent.ConversationContext{
		IsDirectMessage: false,
		GuildID:         platform,
		ChannelID:       threadID,
		Now:             time.Now().UTC(),
	}

	emit := func(ev agent.Event) {
		if ev.Kind == agent.EventStreamDelta {
			return
		}
		if ev.Kind == agent.EventToolStarted || ev.Kind == agent.EventToolFinished {
			log.Printf("bridge: [%s %s] %s %s (%.0fms) %s", platform, threadID, ev.Kind, ev.ToolName, float64(ev.DurationMS), ev.Detail)
			return
		}
		log.Printf("bridge: [%s %s] %s %s", platform, threadID, ev.Kind, ev.Message)
	}

	newHistory, err := s.runner.Run(runCtx, history, prompt, conversation, emit)
	if err != nil {
		return "", err
	}

	reply := lastAssistantContent(newHistory)

	s.mu.Lock()
	kept := agent.CompactHistoryForStorage(s.cfg, newHistory)
	s.sessions[key] = kept
	s.mu.Unlock()
	s.saveHistory()

	if memoryRoot, _ := agent.SharedConversationMemoryRoot(s.cfg, platform, threadID); memoryRoot != "" {
		if err := agent.AppendToMemoryShard(memoryRoot, agent.IdentityDisplayName(s.cfg), prompt, reply, time.Now()); err != nil {
			log.Printf("bridge: append memory shard failed (%s %s): %v", platform, threadID, err)
		}
	}

	return reply, nil
}
