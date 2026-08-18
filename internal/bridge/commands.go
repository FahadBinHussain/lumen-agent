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
	case "threads", "allow", "block", "allowlist":
		if !s.cfg.BridgeAdminThread(platform, threadID) {
			return "admin command — this thread is not in bridge.admin_threads.", true
		}
		switch cmd {
		case "threads":
			return s.commandThreads(platform, threadID, fields), true
		case "allow":
			return s.commandAllow(platform, fields), true
		case "block":
			return s.commandBlock(platform, fields), true
		case "allowlist":
			return s.commandAllowlist(platform), true
		}
	}
	return "", false
}

const threadsPageSize = 25

// commandThreads lists the threads this platform's account can reach, newest
// activity first. Optional page argument (/threads 2).
func (s *Service) commandThreads(platform, threadID string, fields []string) string {
	page := 1
	if len(fields) > 1 {
		if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
			page = n
		}
	}

	var lines []string
	switch platform {
	case "whatsapp":
		if s.whatsapp == nil {
			return "whatsapp is not enabled."
		}
		threads, err := s.whatsapp.Threads(context.Background())
		if err != nil {
			return fmt.Sprintf("failed to list whatsapp chats: %v", err)
		}
		for _, t := range threads {
			name := t.Name
			if name == "" {
				name = "(no name)"
			}
			lines = append(lines, fmt.Sprintf("%s — %s", t.JID, name))
		}
	case "messenger":
		if s.messenger == nil {
			return "messenger is not enabled."
		}
		s.messenger.NudgeThreadSync(context.Background())
		for _, t := range s.messenger.Threads() {
			name := t.Name
			if name == "" {
				name = "(no name)"
			}
			lines = append(lines, fmt.Sprintf("%d — %s", t.ThreadID, name))
		}
	case "discord":
		if s.discord == nil {
			return "discord is not wired."
		}
		channels, err := s.discord.ListChannels()
		if err != nil {
			return fmt.Sprintf("failed to list discord channels: %v", err)
		}
		for _, ch := range channels {
			lines = append(lines, fmt.Sprintf("%s — %s", ch.ID, ch.Name+" ("+ch.GuildName+")"))
		}
	default:
		return "unknown platform."
	}

	if len(lines) == 0 {
		return "no threads found yet. messenger and whatsapp lists fill from traffic/sync; run again in a bit."
	}

	start := (page - 1) * threadsPageSize
	if start >= len(lines) {
		return fmt.Sprintf("page %d is past the end (%d threads).", page, len(lines))
	}
	end := start + threadsPageSize
	if end > len(lines) {
		end = len(lines)
	}
	header := fmt.Sprintf("%d threads (page %d of %d):\n", len(lines), page, (len(lines)+threadsPageSize-1)/threadsPageSize)
	return header + strings.Join(lines[start:end], "\n")
}

// commandAllow adds a thread to the runtime allowlist overlay, persisted to
// bridge-allowlist.json. The id format is validated per platform.
func (s *Service) commandAllow(platform string, fields []string) string {
	if len(fields) < 2 {
		return "usage: /allow <thread id>"
	}
	id := strings.TrimSpace(fields[1])
	if id == "" {
		return "usage: /allow <thread id>"
	}
	switch platform {
	case "messenger":
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return "messenger thread ids are numeric, e.g. /allow 984803114200952"
		}
	case "whatsapp":
		if !strings.Contains(id, "@") {
			return "whatsapp ids are JIDs, e.g. /allow 8801711472629@s.whatsapp.net or a @g.us group"
		}
	case "discord":
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return "discord channel ids are numeric snowflakes, e.g. /allow 1537650032441032765"
		}
	}

	s.mu.Lock()
	if _, ok := contains(s.allowlist[platform], id); !ok {
		s.allowlist[platform] = append(s.allowlist[platform], id)
	}
	s.mu.Unlock()
	s.saveAllowlist()
	return fmt.Sprintf("allowed %s on %s (runtime overlay, survives deploys).", id, platform)
}

// commandBlock removes a thread from the runtime allowlist overlay.
func (s *Service) commandBlock(platform string, fields []string) string {
	if len(fields) < 2 {
		return "usage: /block <thread id>"
	}
	id := strings.TrimSpace(fields[1])
	if id == "" {
		return "usage: /block <thread id>"
	}
	s.mu.Lock()
	if idx, ok := contains(s.allowlist[platform], id); ok {
		s.allowlist[platform] = append(s.allowlist[platform][:idx], s.allowlist[platform][idx+1:]...)
	}
	s.mu.Unlock()
	s.saveAllowlist()
	return fmt.Sprintf("removed %s from the runtime allowlist on %s.", id, platform)
}

// commandAllowlist shows the effective send gate for this platform: config
// allowlist + runtime overlay additions.
func (s *Service) commandAllowlist(platform string) string {
	var cfgIDs []string
	switch platform {
	case "messenger":
		cfgIDs = s.cfg.Messenger.AllowedThreadIDs
	case "whatsapp":
		cfgIDs = s.cfg.WhatsApp.AllowedJIDs
	}

	s.mu.Lock()
	runtimeIDs := append([]string(nil), s.allowlist[platform]...)
	s.mu.Unlock()

	var b strings.Builder
	if len(cfgIDs) == 0 {
		b.WriteString("config allowlist: empty (all threads allowed)\n")
	} else {
		fmt.Fprintf(&b, "config allowlist (%d):\n", len(cfgIDs))
		for _, id := range cfgIDs {
			b.WriteString("  " + id + "\n")
		}
	}
	if len(runtimeIDs) == 0 {
		b.WriteString("runtime overlay: empty\n")
	} else {
		fmt.Fprintf(&b, "runtime overlay (%d):\n", len(runtimeIDs))
		for _, id := range runtimeIDs {
			b.WriteString("  + " + id + "\n")
		}
	}
	return strings.TrimSpace(b.String())
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