package whatsapp

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	_ "modernc.org/sqlite"
)

type WhatsmeowClient struct {
	client      *whatsmeow.Client
	deviceStore *sqlstore.Container
	logger      zerolog.Logger
	handler     MessageHandler
	connected   bool

	mu          sync.Mutex
	qrCode      string
	qrRef       string
	reconnecting bool

	sentMu  sync.Mutex
	sentIDs map[string]time.Time
}

func NewWhatsmeowClient(dbPath string, proxyAddr string, logger zerolog.Logger, handler MessageHandler) (*WhatsmeowClient, error) {
	log := waLog.Zerolog(logger.With().Str("component", "whatsmeow").Logger())

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	deviceStore := sqlstore.NewWithDB(db, "sqlite", log)
	if err := deviceStore.Upgrade(context.Background()); err != nil {
		return nil, fmt.Errorf("upgrade device store: %w", err)
	}

	devices, err := deviceStore.GetAllDevices(context.Background())
	if err != nil {
		return nil, fmt.Errorf("get devices: %w", err)
	}
	var device *store.Device
	if len(devices) > 0 {
		device = devices[0]
	} else {
		device = deviceStore.NewDevice()
	}

	client := whatsmeow.NewClient(device, log)
	client.QRClientType = whatsmeow.PairClientChrome

	wsHTTPClient := NewChromeHTTPClient(proxyAddr)
	client.SetWebsocketHTTPClient(wsHTTPClient)
	client.SetPreLoginHTTPClient(wsHTTPClient)
	client.SetMediaHTTPClient(wsHTTPClient)
	logger.Info().Msg("Chrome TLS fingerprint impersonation enabled (bypass JA3)")

	w := &WhatsmeowClient{
		client:      client,
		deviceStore: deviceStore,
		logger:      logger,
		handler:     handler,
		sentIDs:     map[string]time.Time{},
	}

	if proxyAddr != "" {
		if err := client.SetProxyAddress(proxyAddr); err != nil {
			logger.Warn().Err(err).Str("proxy", proxyAddr).Msg("Failed to set WhatsApp proxy")
		} else {
			logger.Info().Str("proxy", proxyAddr).Msg("WhatsApp proxy configured (E2EE only)")
		}
	}

	client.AddEventHandler(w.handleEvent)

	return w, nil
}

func (w *WhatsmeowClient) handleEvent(evt interface{}) {
	switch e := evt.(type) {
	case *events.Message:
		w.handleMessage(e)
	case *events.Connected:
		w.mu.Lock()
		w.connected = true
		w.mu.Unlock()
		w.setQR("")
		w.logger.Info().Msg("WhatsApp connected")
	case *events.Disconnected:
		w.mu.Lock()
		w.connected = false
		w.mu.Unlock()
		w.setQR("")
		w.logger.Warn().Msg("WhatsApp disconnected")
		if !w.IsLoggedIn() {
			w.scheduleReconnect()
		}
	case *events.LoggedOut:
		w.mu.Lock()
		w.connected = false
		w.mu.Unlock()
		w.setQR("")
		w.logger.Error().Msg("WhatsApp logged out")
	case *events.QR:
		w.setQR("")
		for _, code := range e.Codes {
			var buf bytes.Buffer
			qrterminal.GenerateHalfBlock(code, qrterminal.L, &buf)
			w.setQR(buf.String())
			w.setQRRef(code)
		}
		w.logger.Info().Msg("WhatsApp QR code received - scan with your phone (GET /api/whatsapp/qr)")
	}
}

func (w *WhatsmeowClient) setQR(qr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.qrCode = qr
	if qr == "" {
		w.qrRef = ""
	}
}

func (w *WhatsmeowClient) setQRRef(ref string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.qrRef = ref
}

func (w *WhatsmeowClient) QRCode() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.qrCode
}

func (w *WhatsmeowClient) QRRef() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.qrRef
}

func (w *WhatsmeowClient) handleMessage(evt *events.Message) {
	if evt.Info.IsFromMe {
		return
	}

	text := evt.Message.GetConversation()
	if text == "" {
		if evt.Message.ExtendedTextMessage != nil {
			text = evt.Message.ExtendedTextMessage.GetText()
		}
	}

	if text == "" {
		return
	}

	chat := w.resolvePN(context.Background(), evt.Info.Chat)

	chatJID := ChatJID{
		User:   chat.User,
		Server: chat.Server,
	}

	var replyToID, replyToSender string
	var isReplyToUs, mentionsMe bool
	var mentionedJIDs []string
	if ext := evt.Message.GetExtendedTextMessage(); ext != nil {
		if ctx := ext.GetContextInfo(); ctx != nil {
			replyToID = ctx.GetStanzaID()
			replyToSender = ctx.GetParticipant()
			if replyToID != "" && ctx.GetQuotedMessage() != nil && w.isRecentSent(replyToID) {
				isReplyToUs = true
			}
			if own := w.ownJID(); own != "" {
				for _, jid := range ctx.GetMentionedJID() {
					if jid == own {
						mentionsMe = true
					}
					mentionedJIDs = append(mentionedJIDs, jid)
				}
			}
		}
	}

	msg := ParsedMessage{
		Chat:             chatJID,
		ID:               evt.Info.ID,
		SenderJID:        evt.Info.Sender.User + "@s.whatsapp.net",
		Timestamp:        evt.Info.Timestamp,
		FromMe:           evt.Info.IsFromMe,
		Text:             text,
		PushName:         evt.Info.PushName,
		ReplyToID:        replyToID,
		ReplyToSenderJID: replyToSender,
		IsReplyToUs:      isReplyToUs,
		MentionsMe:       mentionsMe,
		MentionedJIDs:    mentionedJIDs,
		IsGroup:          evt.Info.IsGroup,
	}

	w.logger.Info().
		Str("chat", chatJID.String()).
		Str("text", truncate(text, 50)).
		Msg("WhatsApp message received via whatsmeow")

	if w.handler != nil {
		w.handler(context.Background(), msg)
	}
}

func (w *WhatsmeowClient) Connect(ctx context.Context) error {
	if w.client.Store.ID == nil {
		qrChan, _ := w.client.GetQRChannel(ctx)
		go func() {
			for evt := range qrChan {
				if evt.Event == "code" {
					w.logger.Info().Msg("Scan QR code to link WhatsApp")
				}
			}
			w.logger.Warn().Msg("whatsapp: QR channel closed")
			if !w.IsLoggedIn() {
				w.scheduleReconnect()
			}
		}()
	}

	err := w.client.Connect()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	return nil
}

func (w *WhatsmeowClient) scheduleReconnect() {
	w.mu.Lock()
	if w.reconnecting {
		w.mu.Unlock()
		return
	}
	w.reconnecting = true
	w.mu.Unlock()

	go func() {
		time.Sleep(15 * time.Second)
		w.logger.Info().Msg("whatsapp: not logged in, reconnecting for a fresh QR session")
		err := w.Connect(context.Background())
		if err != nil {
			w.logger.Error().Err(err).Msg("whatsapp reconnect failed")
		}
		w.mu.Lock()
		w.reconnecting = false
		w.mu.Unlock()
	}()
}

func (w *WhatsmeowClient) PairPhone(ctx context.Context, phone string) (string, error) {
	code, err := w.client.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Windows)")
	if err != nil {
		return "", fmt.Errorf("pair phone: %w", err)
	}
	return code, nil
}

// resolvePN maps a LID (privacy-number) chat JID to its phone-number JID
// using whatsmeow's LID store, so allowlists and session keys can use the
// stable PN form. Returns the input unchanged when the JID is not a LID or
// the mapping is not yet known.
func (w *WhatsmeowClient) resolvePN(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if pn, err := w.client.Store.LIDs.GetPNForLID(ctx, jid); err == nil && pn.User != "" && pn.Server == types.DefaultUserServer {
		return pn
	}
	return jid
}

func (w *WhatsmeowClient) Disconnect() {
	if w.client != nil {
		w.client.Disconnect()
	}
}

func (w *WhatsmeowClient) SendText(ctx context.Context, to string, text string) error {
	jid, err := types.ParseJID(to)
	if err != nil {
		return fmt.Errorf("parse JID: %w", err)
	}

	jid = w.resolvePN(ctx, jid)

	msg := &waE2E.Message{
		Conversation: &text,
	}

	src, err := w.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	w.recordSent(src.ID)

	w.logger.Info().Str("to", to).Str("text", truncate(text, 50)).Msg("WhatsApp message sent via whatsmeow")
	return nil
}

// ownJID returns our own phone-number JID as a string, or "" before login.
func (w *WhatsmeowClient) ownJID() string {
	w.sentMu.Lock()
	defer w.sentMu.Unlock()
	if w.client == nil || w.client.Store == nil || w.client.Store.ID == nil {
		return ""
	}
	return types.NewJID(w.client.Store.ID.User, types.DefaultUserServer).String()
}

// CleanMentions removes the "@<name>" token(s) for OUR mention from the text.
// WhatsApp renders mentions as literal text plus a MentionedJID annotation, so
// the jid is resolved to its contact name (contact store: full name, then push
// name) and matching tokens are deleted. Mirrors messenger's CleanMentions,
// which also only strips our own mention. Unresolvable names keep the text
// unchanged.
func (w *WhatsmeowClient) CleanMentions(text string, mentionedJIDs []string) string {
	if strings.TrimSpace(text) == "" || len(mentionedJIDs) == 0 {
		return text
	}
	own := w.ownJID()
	if own == "" {
		return text
	}

	names := make([]string, 0, len(mentionedJIDs))
	for _, jidStr := range mentionedJIDs {
		if jidStr != own {
			continue
		}
		jid, err := types.ParseJID(jidStr)
		if err != nil {
			continue
		}
		if name := w.contactName(jid); name != "" {
			names = append(names, name)
		}
	}
	return cleanMentionsFromNames(text, names)
}

// cleanMentionsFromNames removes literal "@name" tokens from text. Pure and
// testable; the client resolves jids to names before calling it.
func cleanMentionsFromNames(text string, names []string) string {
	if len(names) == 0 {
		return text
	}
	for _, name := range names {
		text = strings.ReplaceAll(text, "@"+name, "")
	}
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	return strings.TrimSpace(text)
}

// contactName resolves a jid to a display name using the whatsmeow contact
// store (synced from the phone). Falls back to the push name, then "".
func (w *WhatsmeowClient) contactName(jid types.JID) string {
	if w.client == nil || w.client.Store == nil || w.client.Store.Contacts == nil {
		return ""
	}
	info, err := w.client.Store.Contacts.GetContact(context.Background(), jid)
	if err != nil || !info.Found {
		return ""
	}
	if name := strings.TrimSpace(info.FullName); name != "" {
		return name
	}
	return strings.TrimSpace(info.PushName)
}

// recordSent remembers outbound message IDs so incoming quotes can be
// recognized as replies to us.
func (w *WhatsmeowClient) recordSent(id string) {
	if id == "" {
		return
	}
	w.sentMu.Lock()
	defer w.sentMu.Unlock()
	w.sentIDs[id] = time.Now()
	if len(w.sentIDs) > 500 {
		cutoff := time.Now().Add(-24 * time.Hour)
		for k, v := range w.sentIDs {
			if v.Before(cutoff) {
				delete(w.sentIDs, k)
			}
		}
	}
}

func (w *WhatsmeowClient) isRecentSent(id string) bool {
	w.sentMu.Lock()
	defer w.sentMu.Unlock()
	_, ok := w.sentIDs[id]
	return ok
}

func (w *WhatsmeowClient) IsConnected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.connected
}

func (w *WhatsmeowClient) StartTyping(ctx context.Context, to string) {
	jid, err := types.ParseJID(to)
	if err != nil {
		return
	}
	if err := w.client.SendChatPresence(ctx, jid, types.ChatPresenceComposing, types.ChatPresenceMediaText); err != nil {
		w.logger.Debug().Str("to", to).Msg("failed to set composing presence")
	}
}

func (w *WhatsmeowClient) StopTyping(ctx context.Context, to string) {
	jid, err := types.ParseJID(to)
	if err != nil {
		return
	}
	if err := w.client.SendChatPresence(ctx, jid, types.ChatPresencePaused, types.ChatPresenceMediaText); err != nil {
		w.logger.Debug().Str("to", to).Msg("failed to clear composing presence")
	}
}

func (w *WhatsmeowClient) IsLoggedIn() bool {
	return w.client.Store.ID != nil
}

func (w *WhatsmeowClient) SaveSession(ctx context.Context, dbPath string, save func(ctx context.Context, sessionData, wacliData []byte) error) error {
	if !w.IsLoggedIn() {
		return nil
	}
	sessionData, err := os.ReadFile(dbPath)
	if err != nil {
		return fmt.Errorf("read whatsmeow db: %w", err)
	}
	if save != nil {
		return save(ctx, sessionData, nil)
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}