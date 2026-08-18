package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"element-orion/internal/agent"
	"element-orion/internal/bnp"
	"element-orion/internal/config"
	"element-orion/internal/llm"
	"element-orion/internal/messenger"
	"element-orion/internal/neon"
	"element-orion/internal/whatsapp"
)

const bridgeHistoryFile = "bridge-sessions.json"

type Service struct {
	cfg       config.Config
	runner    *agent.Runner
	messenger *messenger.Client
	whatsapp  *whatsapp.WhatsmeowClient
	waDBPath  string
	neon      *neon.DB

	mu       sync.Mutex
	sessions map[string][]llm.Message

	historyPath string
	toucher     func()

	waLastMtime   time.Time
	waLastSize    int64
	waLastSavedAt time.Time
}

// SetPersistenceToucher registers a callback fired after session history is
// written so the Neon snapshot backup (internal/persist) syncs promptly —
// mirrors discordbot's session persistence contract.
func (s *Service) SetPersistenceToucher(fn func()) {
	s.toucher = fn
}

func New(cfg config.Config, runner *agent.Runner) (*Service, error) {
	s := &Service{
		cfg:         cfg,
		runner:      runner,
		sessions:    make(map[string][]llm.Message),
		historyPath: filepath.Join(cfg.App.SessionDir, bridgeHistoryFile),
	}
	s.loadHistory()

	if cfg.Messenger.Enabled {
		client, err := messenger.New(cfg.Messenger.CookiesPath, s.handleMessengerMessage)
		if err != nil {
			return nil, err
		}
		s.messenger = client
		log.Printf("bridge: messenger enabled with uid %d", client.UID())
	}

	if cfg.WhatsApp.Enabled {
		if err := os.MkdirAll(cfg.WhatsApp.StoreDir, 0o700); err != nil {
			return nil, err
		}
		s.waDBPath = cfg.WhatsApp.StoreDir + "/whatsmeow.db"

		if dsn := cfg.WhatsAppDatabaseURL(); dsn != "" {
			db, err := neon.New(context.Background(), dsn)
			if err != nil {
				log.Printf("bridge: neon unavailable, whatsapp session persistence disabled: %v", err)
			} else {
				s.neon = db
				if session, err := db.LoadWhatsAppSession(context.Background()); err == nil && len(session.SessionData) > 0 {
					if err := os.WriteFile(s.waDBPath, session.SessionData, 0o600); err != nil {
						log.Printf("bridge: failed to restore whatsapp session from neon: %v", err)
					} else {
						log.Printf("bridge: restored whatsapp session from neon (%d bytes)", len(session.SessionData))
					}
				}
			}
		}

		logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
		wa, err := whatsapp.NewWhatsmeowClient(s.waDBPath, cfg.WhatsApp.Proxy, logger, s.handleWhatsAppMessage)
		if err != nil {
			return nil, err
		}
		s.whatsapp = wa
		log.Printf("bridge: whatsapp enabled (store %s)", cfg.WhatsApp.StoreDir)
	}

	return s, nil
}

func (s *Service) Close() {
	if s.neon != nil {
		s.neon.Close()
	}
	if s.whatsapp != nil {
		s.whatsapp.Disconnect()
	}
	if s.messenger != nil {
		s.messenger.Disconnect()
	}
}

func (s *Service) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	if s.messenger != nil {
		uid, name, err := s.messenger.Login(ctx)
		if err != nil {
			log.Printf("bridge: messenger login failed: %v", err)
			return err
		}
		log.Printf("bridge: messenger logged in as %s (%d)", name, uid)
		if err := s.messenger.Start(ctx); err != nil {
			return err
		}
	}

	if s.whatsapp != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.whatsapp.Connect(ctx); err != nil {
				log.Printf("bridge: whatsapp connect failed: %v", err)
				return
			}
			s.saveWhatsAppSession(ctx)
			log.Printf("bridge: whatsapp connected")
			// symmetric with persist's 1m cadence, but change-gated: the
			// store file only changes when something actually happens, so
			// idle nights upload nothing and the Neon compute stays asleep.
			// the 15m max-interval fallback still covers edge cases.
			ticker := time.NewTicker(1 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					s.saveWhatsAppSession(context.Background())
					return
				case <-ticker.C:
					s.saveWhatsAppSession(ctx)
				}
			}
		}()
	}

	if s.messenger != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !s.cfg.Bridge.BNPEnabled {
				log.Printf("bnp: worker disabled by config (bridge.bnp_enabled=false)")
				return
			}
			bnpWorker := bnp.NewWorker(s, nil)
			bnpWorker.Run(ctx)
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.serveHTTP(ctx)
	}()

	if s.cfg.Bridge.HealthWatch.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.watchHealth(ctx)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
	}()

	<-ctx.Done()
	wg.Wait()
	s.Close()
	err := <-errCh
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) saveWhatsAppSession(ctx context.Context) {
	if s.whatsapp == nil || s.neon == nil {
		return
	}
	st, err := os.Stat(s.waDBPath)
	if err != nil {
		return
	}
	if st.ModTime().Equal(s.waLastMtime) && st.Size() == s.waLastSize &&
		time.Since(s.waLastSavedAt) < 15*time.Minute {
		return
	}
	if err := s.whatsapp.SaveSession(ctx, s.waDBPath, s.neon.SaveWhatsAppSession); err != nil {
		log.Printf("bridge: failed to save whatsapp session: %v", err)
		return
	}
	s.waLastMtime = st.ModTime()
	s.waLastSize = st.Size()
	s.waLastSavedAt = time.Now()
	log.Printf("bridge: whatsapp session saved to neon")
}

func (s *Service) handleMessengerMessage(ctx context.Context, msg messenger.Incoming) {
	threadID := strconv.FormatInt(msg.ThreadID, 10)
	if !s.cfg.MessengerThreadAllowed(threadID) {
		log.Printf("bridge: messenger thread %s not in allowlist, skipping", threadID)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	var prompt string
	if strings.HasPrefix(text, "/ai") {
		prompt = strings.TrimSpace(text[3:])
	} else if s.messenger != nil && (msg.ReplyToUser == s.messenger.UID() || s.messenger.MentionsMe(msg)) {
		prompt = s.messenger.CleanMentions(msg)
	}
	if prompt == "" {
		return
	}

	log.Printf("bridge: messenger message from %d in thread %s", msg.SenderID, threadID)
	s.agentRun(ctx, "messenger", threadID, "", prompt)
}

func (s *Service) handleWhatsAppMessage(ctx context.Context, msg whatsapp.ParsedMessage) {
	if msg.FromMe {
		return
	}

	prompt := whatsappTriggerPrompt(msg)
	if prompt == "" {
		return
	}

	chatJID := msg.Chat.String()
	if !s.cfg.WhatsAppJIDAllowed(chatJID) {
		log.Printf("bridge: whatsapp message from %s in chat %s dropped (not in whatsapp.allowed_jids)", msg.SenderJID, chatJID)
		return
	}
	log.Printf("bridge: whatsapp message from %s in chat %s", msg.SenderJID, chatJID)
	s.agentRun(ctx, "whatsapp", chatJID, chatJID, prompt)
}

// whatsappTriggerPrompt mirrors messenger's trigger semantics: /ai prefix,
// a reply to one of our own messages, or a mention of our uid. Media-only
// replies/mentions stay silent (the messenger path also skips them).
func whatsappTriggerPrompt(msg whatsapp.ParsedMessage) string {
	text := msg.Text
	if text == "" {
		if msg.Media != nil {
			text = "[media: " + msg.Media.Type + "]"
		} else if msg.Poll != nil {
			text = "Poll: " + msg.Poll.Question
		}
	}

	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/ai") {
		return strings.TrimSpace(trimmed[3:])
	}
	if msg.IsReplyToUs || msg.MentionsMe {
		if msg.Text == "" {
			return ""
		}
		return trimmed
	}
	return ""
}

func (s *Service) agentRun(ctx context.Context, platform string, threadID string, jid string, prompt string) {
	key := platform + ":" + threadID

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
		log.Printf("bridge: [%s %s] %s %s", platform, threadID, ev.Kind, ev.Message)
	}

	if s.whatsapp != nil && platform == "whatsapp" && jid != "" {
		s.whatsapp.StartTyping(ctx, jid)
	}

	// murmur-style formatting: messenger shows a "thinking." animation while
	// the model works, then the final reply is edited into the same message
	// as "[model]\nresponse". whatsapp skips the animation and uses a single
	// "[model] response" line. The tag uses the catalog's short name (like
	// murmur's model alias), falling back to the full model id.
	modelName := s.cfg.ResolveLLMModel()
	if entry, ok := s.cfg.LLM.ActiveModelEntry(); ok && strings.TrimSpace(entry.Name) != "" {
		modelName = entry.Name
	}
	var msgID string
	var stopThinking chan struct{}
	if platform == "messenger" {
		msgID = s.send(platform, threadID, jid, "thinking.")
		if msgID != "" {
			stopThinking = make(chan struct{})
			go func() {
				dots := []string{"thinking.", "thinking..", "thinking..."}
				i := 0
				for edits := 0; edits < 4; edits++ {
					select {
					case <-stopThinking:
						return
					case <-time.After(500 * time.Millisecond):
						i = (i + 1) % len(dots)
						s.editMessage(threadID, msgID, dots[i])
					}
				}
			}()
		}
	}

	newHistory, err := s.runner.Run(ctx, history, prompt, conversation, emit)
	if stopThinking != nil {
		close(stopThinking)
	}

	if err != nil {
		log.Printf("bridge: agent run failed (%s %s): %v", platform, threadID, err)
		s.sendReply(platform, threadID, jid, msgID, "[chat error] "+err.Error())
		return
	}

	if s.whatsapp != nil && platform == "whatsapp" && jid != "" {
		s.whatsapp.StopTyping(ctx, jid)
	}

	reply := lastAssistantContent(newHistory)
	if reply == "" {
		log.Printf("bridge: agent returned empty reply (%s %s)", platform, threadID)
		s.sendReply(platform, threadID, jid, msgID, "[error] model returned empty response")
		return
	}

	s.mu.Lock()
	kept := agent.CompactHistoryForStorage(s.cfg, newHistory)
	s.sessions[key] = kept
	s.mu.Unlock()
	s.saveHistory()

	// Fork patch: symmetric with Discord — append the exchange to the shared
	// memory shard (guild-memory/<platform>/<thread>/) so WhatsApp/Messenger
	// conversations persist into the same half-day shards their prompt reads.
	// Best-effort, like the history write; the persist ticker syncs it.
	if memoryRoot, _ := agent.SharedConversationMemoryRoot(s.cfg, platform, threadID); memoryRoot != "" {
		if err := agent.AppendToMemoryShard(memoryRoot, agent.IdentityDisplayName(s.cfg), prompt, reply, time.Now()); err != nil {
			log.Printf("bridge: append memory shard failed (%s %s): %v", platform, threadID, err)
		}
	}

	s.sendReply(platform, threadID, jid, msgID, fmt.Sprintf("[%s] %s", modelName, reply))
}

// loadHistory restores the per-platform:thread session history written by
// saveHistory (a file inside the session dir, so internal/persist snapshot +
// restore carries it to fresh machines exactly like Discord's session files).
// Failures are logged and the bridge starts with empty sessions.
func (s *Service) loadHistory() {
	data, err := os.ReadFile(s.historyPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("bridge: failed to read history file %s: %v", s.historyPath, err)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.Unmarshal(data, &s.sessions); err != nil {
		log.Printf("bridge: failed to parse history file %s: %v (starting empty)", s.historyPath, err)
		s.sessions = make(map[string][]llm.Message)
		return
	}
	log.Printf("bridge: restored %d session histories from %s", len(s.sessions), s.historyPath)
}

// saveHistory writes the current sessions to the session dir (best-effort —
// a failed write never breaks the turn; the persist ticker or toucher syncs it).
func (s *Service) saveHistory() {
	s.mu.Lock()
	data, err := json.Marshal(s.sessions)
	s.mu.Unlock()
	if err != nil {
		log.Printf("bridge: marshal history failed: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.historyPath), 0o755); err != nil {
		log.Printf("bridge: mkdir for history file failed: %v", err)
		return
	}
	if err := os.WriteFile(s.historyPath, data, 0o644); err != nil {
		log.Printf("bridge: write history file failed: %v", err)
		return
	}
	if s.toucher != nil {
		s.toucher()
	}
}

func (s *Service) send(platform string, threadID string, jid string, text string) string {
	switch platform {
	case "messenger":
		if s.messenger == nil {
			log.Printf("bridge: messenger not enabled, cannot send to %s", threadID)
			return ""
		}
		if !s.cfg.MessengerThreadAllowed(threadID) {
			log.Printf("bridge: send to messenger thread %s blocked (not in messenger.allowed_thread_ids)", threadID)
			return ""
		}
		id, err := strconv.ParseInt(threadID, 10, 64)
		if err != nil {
			log.Printf("bridge: invalid messenger thread id %q", threadID)
			return ""
		}
		return s.messenger.SendText(context.Background(), id, text)
	case "whatsapp":
		if s.whatsapp == nil {
			log.Printf("bridge: whatsapp not enabled, cannot send to %s", jid)
			return ""
		}
		if !s.cfg.WhatsAppJIDAllowed(jid) {
			log.Printf("bridge: send to whatsapp jid %s blocked (not in whatsapp.allowed_jids)", jid)
			return ""
		}
		if err := s.whatsapp.SendText(context.Background(), jid, text); err != nil {
			log.Printf("bridge: whatsapp send failed: %v", err)
		}
		return ""
	}
	return ""
}

// editMessage replaces an existing messenger message, allowlist-gated like
// send. Used by the murmur-style thinking animation and the final reply.
func (s *Service) editMessage(threadID string, messageID string, text string) error {
	if s.messenger == nil {
		return fmt.Errorf("messenger not enabled")
	}
	if !s.cfg.MessengerThreadAllowed(threadID) {
		return fmt.Errorf("thread %s not in messenger.allowed_thread_ids", threadID)
	}
	return s.messenger.EditMessage(context.Background(), messageID, text)
}

// sendReply delivers a formatted message the murmur way: edit the pending
// thinking message on messenger when one exists, otherwise send a new one.
func (s *Service) sendReply(platform string, threadID string, jid string, msgID string, text string) {
	if platform == "messenger" && msgID != "" {
		if err := s.editMessage(threadID, msgID, text); err != nil {
			log.Printf("bridge: edit reply in %s failed: %v", threadID, err)
			s.send(platform, threadID, jid, text)
		}
		return
	}
	s.send(platform, threadID, jid, text)
}

func (s *Service) notify(platform string, threadID string, text string) {
	if platform == "" {
		platform = "messenger"
	}
	jid := ""
	if platform == "whatsapp" {
		jid = threadID
	}
	s.send(platform, threadID, jid, text)
}

// SendMessage implements bnp.MessengerSender: delivers an outbox item to the
// configured Messenger thread. Returns the message ID, or "" on failure.
func (s *Service) SendMessage(ctx context.Context, threadID int64, text string) (string, error) {
	if s.messenger == nil {
		return "", fmt.Errorf("messenger not enabled")
	}
	if !s.cfg.MessengerThreadAllowed(strconv.FormatInt(threadID, 10)) {
		return "", fmt.Errorf("thread %d not in messenger.allowed_thread_ids", threadID)
	}
	return s.messenger.SendText(ctx, threadID, text), nil
}

// EditMessage implements bnp.MessengerSender: replaces an existing Messenger
// message (edit_pending outbox items).
func (s *Service) EditMessage(ctx context.Context, threadID int64, messageID string, text string) error {
	if s.messenger == nil {
		log.Printf("bnp: messenger not enabled, cannot edit message %s", messageID)
		return fmt.Errorf("messenger not enabled")
	}
	if !s.cfg.MessengerThreadAllowed(strconv.FormatInt(threadID, 10)) {
		return fmt.Errorf("thread %d not in messenger.allowed_thread_ids", threadID)
	}
	return s.messenger.EditMessage(ctx, messageID, text)
}

func cloneMessages(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]llm.Message, len(messages))
	copy(cloned, messages)
	return cloned
}

func lastAssistantContent(history []llm.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		return content
	}
	return ""
}