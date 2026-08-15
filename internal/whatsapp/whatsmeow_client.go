package whatsapp

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"

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

	mu     sync.Mutex
	qrCode string
	qrRef  string
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
		w.connected = true
		w.setQR("")
		w.logger.Info().Msg("WhatsApp connected")
	case *events.Disconnected:
		w.connected = false
		w.logger.Warn().Msg("WhatsApp disconnected")
	case *events.LoggedOut:
		w.connected = false
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

	chatJID := ChatJID{
		User:   evt.Info.Chat.User,
		Server: evt.Info.Chat.Server,
	}

	msg := ParsedMessage{
		Chat:      chatJID,
		ID:        evt.Info.ID,
		SenderJID: evt.Info.Sender.User + "@s.whatsapp.net",
		Timestamp: evt.Info.Timestamp,
		FromMe:    evt.Info.IsFromMe,
		Text:      text,
		PushName:  evt.Info.PushName,
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
		}()
	}

	err := w.client.Connect()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	return nil
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

	msg := &waE2E.Message{
		Conversation: &text,
	}

	_, err = w.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	w.logger.Info().Str("to", to).Str("text", truncate(text, 50)).Msg("WhatsApp message sent via whatsmeow")
	return nil
}

func (w *WhatsmeowClient) IsConnected() bool {
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