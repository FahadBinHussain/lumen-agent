package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"element-orion/internal/agent"
)

// runCommand mirrors the discord slash command surface (/new /stop /status
// /memory /compact) on the text platforms. Triggered by the same rules as the
// agent (an /ai prefix, a reply to us, or a mention) — the prompt just starts
// with "/". Returns the reply text and whether the command was recognized;
// unrecognized "/..." prompts fall through to the agent.
func (s *Service) runCommand(platform, threadID, prompt string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(prompt))
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	switch cmd {
	case "new":
		return s.commandNew(platform, threadID), true
	case "stop":
		return s.commandStop(platform, threadID), true
	case "status":
		return s.commandStatus(platform, threadID), true
	case "memory":
		return s.commandMemory(platform, threadID), true
	case "compact":
		return s.commandCompact(platform, threadID), true
	}
	return "", false
}

// handleCommand runs a command and sends the reply through the normal
// platform send path. Returns true when the prompt was handled as a command.
func (s *Service) handleCommand(platform, threadID, jid, prompt string) bool {
	reply, ok := s.runCommand(platform, threadID, prompt)
	if !ok {
		return false
	}
	if strings.TrimSpace(reply) != "" {
		s.send(platform, threadID, jid, reply)
	}
	return true
}

func (s *Service) commandNew(platform, threadID string) string {
	key := platform + ":" + threadID
	s.mu.Lock()
	_, existed := s.sessions[key]
	delete(s.sessions, key)
	s.mu.Unlock()
	s.saveHistory()
	if existed {
		return "Started a new session in this thread. Previous context cleared."
	}
	return "Started a new session in this thread."
}

func (s *Service) commandStop(platform, threadID string) string {
	key := platform + ":" + threadID
	s.mu.Lock()
	rc, ok := s.runCancels[key]
	if ok {
		delete(s.runCancels, key)
	}
	s.mu.Unlock()
	if !ok {
		return "No active session was running in this thread."
	}
	rc.cancel()
	return "Stopped the active session in this thread."
}

func (s *Service) commandStatus(platform, threadID string) string {
	key := platform + ":" + threadID

	modelName := s.cfg.ResolveLLMModel()
	if entry, ok := s.cfg.LLM.ActiveModelEntry(); ok && strings.TrimSpace(entry.Name) != "" {
		modelName = entry.Name
	}

	s.mu.Lock()
	historyLen := len(s.sessions[key])
	_, active := s.runCancels[key]
	s.mu.Unlock()

	wa := "n/a"
	if s.whatsapp != nil {
		if s.whatsapp.IsConnected() {
			wa = "connected"
		} else {
			wa = "disconnected"
		}
	}
	msg := "n/a"
	if s.messenger != nil {
		if s.messenger.IsConnected() {
			msg = "connected"
		} else {
			msg = "disconnected"
		}
	}
	disc := "n/a"
	if s.discord != nil {
		if s.discord.IsConnected() {
			disc = "connected"
		} else {
			disc = "disconnected"
		}
	}

	var b strings.Builder
	b.WriteString("status for " + platform + " " + threadID + "\n")
	fmt.Fprintf(&b, "model:      %s\n", modelName)
	fmt.Fprintf(&b, "history:    %d messages\n", historyLen)
	if active {
		b.WriteString("active run: yes\n")
	} else {
		b.WriteString("active run: no\n")
	}
	fmt.Fprintf(&b, "whatsapp:   %s\n", wa)
	fmt.Fprintf(&b, "messenger:  %s\n", msg)
	fmt.Fprintf(&b, "discord:    %s\n", disc)
	return strings.TrimSuffix(b.String(), "\n")
}

func (s *Service) commandMemory(platform, threadID string) string {
	memoryRoot, why := agent.SharedConversationMemoryRoot(s.cfg, platform, threadID)
	if memoryRoot == "" {
		return "no memory shard root for this thread (" + why + ")"
	}

	entries, err := os.ReadDir(memoryRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "no memory shards for this thread yet (root: " + memoryRoot + ")"
		}
		return "memory scan failed: " + err.Error()
	}

	type shardInfo struct {
		name string
		size int64
		mod  time.Time
	}
	var shards []shardInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		shards = append(shards, shardInfo{e.Name(), info.Size(), info.ModTime()})
	}
	if len(shards) == 0 {
		return "no memory shards for this thread yet (root: " + memoryRoot + ")"
	}

	sort.Slice(shards, func(i, j int) bool { return shards[i].mod.After(shards[j].mod) })

	var total int64
	for _, sh := range shards {
		total += sh.size
	}

	var b strings.Builder
	fmt.Fprintf(&b, "memory shards: %d (%s) in %s\n", len(shards), humanizeBytes(total), memoryRoot)
	for _, sh := range shards {
		fmt.Fprintf(&b, "  %s  %6s  %s\n", sh.mod.Format("2006-01-02 15:04"), humanizeBytes(sh.size), sh.name)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (s *Service) commandCompact(platform, threadID string) string {
	key := platform + ":" + threadID
	s.mu.Lock()
	history := cloneMessages(s.sessions[key])
	s.mu.Unlock()

	if len(history) == 0 {
		return "Nothing to compact (0 messages)."
	}

	compacted := agent.CompactHistoryForStorage(s.cfg, history)
	s.mu.Lock()
	s.sessions[key] = compacted
	s.mu.Unlock()
	s.saveHistory()

	if len(compacted) == len(history) {
		return fmt.Sprintf("Context already compact enough (%d messages).", len(history))
	}
	return fmt.Sprintf("History compacted: %d -> %d messages.", len(history), len(compacted))
}

func humanizeBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

// runCancel tracks an in-flight agent run so /stop can cancel it. The seq
// guard stops a slow run from clearing a newer run's cancel entry.
type runCancel struct {
	cancel context.CancelFunc
	seq    int64
}

func (s *Service) clearRunCancel(key string, seq int64) {
	s.mu.Lock()
	if rc, ok := s.runCancels[key]; ok && rc.seq == seq {
		delete(s.runCancels, key)
	}
	s.mu.Unlock()
}