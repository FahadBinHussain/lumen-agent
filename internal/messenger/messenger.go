package messenger

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"element-orion/internal/cookies"
)

const maxMsgLen = 1900

type Incoming struct {
	MessageID    string
	ThreadID     int64
	SenderID     int64
	Text         string
	Timestamp    int64
	ReplySource  string
	ReplyToUser  int64
	MentionIDs   []int64
	MentionOffs  []int
	MentionLens  []int
}

type Handler func(ctx context.Context, msg Incoming)

type Client struct {
	client      *messagix.Client
	uid         int64
	platform    types.Platform
	handler     Handler
	cookiesPath string
	startTime   time.Time
}

func New(cookiesPath string, handler Handler) (*Client, error) {
	cookieMap, err := cookies.LoadFromFile(cookiesPath)
	if err != nil {
		return nil, fmt.Errorf("load cookies: %w", err)
	}

	c := cookies.ToMessagix(cookieMap, types.Messenger)
	if missing := cookies.GetMissing(c); len(missing) > 0 {
		return nil, fmt.Errorf("missing required cookies: %v", missing)
	}

	logger := zerolog.New(os.Stderr).With().Str("component", "messagix").Timestamp().Logger()
	client := messagix.NewClient(c, logger, &messagix.Config{
		MayConnectToDGW: false,
	})

	return &Client{
		client:      client,
		platform:    types.Messenger,
		handler:     handler,
		cookiesPath: cookiesPath,
		startTime:   time.Now(),
	}, nil
}

func (c *Client) SetHandler(handler Handler) {
	c.handler = handler
}

func (c *Client) UID() int64 {
	return c.uid
}

func (c *Client) Login(ctx context.Context) (int64, string, error) {
	userInfo, _, err := c.client.LoadMessagesPage(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("load messages page: %w", err)
	}
	c.uid = userInfo.GetFBID()
	return c.uid, userInfo.GetName(), nil
}

func (c *Client) Start(ctx context.Context) error {
	c.client.SetEventHandler(c.makeEventHandler(ctx))
	go func() {
		err := c.client.Connect(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("messenger: connection error: %v", err)
		}
	}()
	return nil
}

func (c *Client) ReloadCookies(ctx context.Context) error {
	cookieMap, err := cookies.LoadFromFile(c.cookiesPath)
	if err != nil {
		return fmt.Errorf("load cookies: %w", err)
	}

	cc := cookies.ToMessagix(cookieMap, c.platform)
	if missing := cookies.GetMissing(cc); len(missing) > 0 {
		return fmt.Errorf("missing cookies: %v", missing)
	}

	c.client.Disconnect()
	logger := zerolog.New(os.Stderr).With().Str("component", "messagix").Timestamp().Logger()
	c.client = messagix.NewClient(cc, logger, &messagix.Config{
		MayConnectToDGW: false,
	})

	userInfo, _, err := c.client.LoadMessagesPage(ctx)
	if err != nil {
		return fmt.Errorf("load messages page: %w", err)
	}
	c.uid = userInfo.GetFBID()
	log.Printf("messenger: cookies reloaded, logged in as %s (%d)", userInfo.GetName(), c.uid)

	return c.Start(ctx)
}

func (c *Client) Disconnect() {
	c.client.Disconnect()
}

func (c *Client) makeEventHandler(ctx context.Context) func(context.Context, any) {
	return func(evtCtx context.Context, evt any) {
		switch e := evt.(type) {
		case *messagix.Event_Ready:
			log.Printf("messenger: MQTT connected (code %s)", e.ConnectionCode)
			go func() {
				ticker := time.NewTicker(60 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						_, err := c.client.ExecuteTasks(ctx, &socket.ReportAppStateTask{
							AppState:  table.FOREGROUND,
							RequestID: fmt.Sprintf("keepalive-%d", time.Now().UnixMilli()),
						})
						if err != nil {
							log.Printf("messenger: foreground keepalive failed: %v", err)
						}
					}
				}
			}()

		case *messagix.Event_PublishResponse:
			upsertMessages, insertMessages := e.Table.WrapMessages()
			for _, msg := range insertMessages {
				if msg == nil || msg.LSInsertMessage == nil {
					continue
				}
				if msg.IsUnsent {
					continue
				}
				c.relay(evtCtx, Incoming{
					MessageID:   msg.MessageId,
					ThreadID:    msg.ThreadKey,
					SenderID:    msg.SenderId,
					Text:        msg.Text,
					Timestamp:   msg.TimestampMs,
					ReplySource: msg.ReplySourceId,
					ReplyToUser: msg.ReplyToUserId,
					MentionIDs:  parseMentionIDs(msg.MentionIds),
					MentionOffs: parseMentionInts(msg.MentionOffsets),
					MentionLens: parseMentionInts(msg.MentionLengths),
				})
			}

			for threadID, upsert := range upsertMessages {
				for _, msg := range upsert.Messages {
					if msg == nil || msg.LSInsertMessage == nil {
						continue
					}
					if msg.IsUnsent {
						continue
					}
					c.relay(evtCtx, Incoming{
						MessageID:   msg.MessageId,
						ThreadID:    threadID,
						SenderID:    msg.SenderId,
						Text:        msg.Text,
						Timestamp:   msg.TimestampMs,
						ReplySource: msg.ReplySourceId,
						ReplyToUser: msg.ReplyToUserId,
						MentionIDs:  parseMentionIDs(msg.MentionIds),
						MentionOffs: parseMentionInts(msg.MentionOffsets),
						MentionLens: parseMentionInts(msg.MentionLengths),
					})
				}
			}

		case *messagix.Event_SocketError:
			log.Printf("messenger: socket error (attempts %d): %v", e.ConnectionAttempts, e.Err)

		case *messagix.Event_PermanentError:
			log.Printf("messenger: permanent error: %v", e.Err)

		case *messagix.Event_Reconnected:
			log.Printf("messenger: MQTT reconnected")
		}
	}
}

func (c *Client) relay(ctx context.Context, msg Incoming) {
	if msg.SenderID == c.uid || msg.SenderID == 0 {
		return
	}
	ts := time.UnixMilli(msg.Timestamp)
	if !ts.IsZero() && ts.Before(c.startTime.Add(-5*time.Second)) {
		return
	}
	if c.handler != nil {
		c.handler(ctx, msg)
	}
}

func parseMentionIDs(raw string) []int64 {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []int64
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func parseMentionInts(raw string) []int {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []int
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if n, err := strconv.Atoi(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (c *Client) MentionsMe(msg Incoming) bool {
	for _, id := range msg.MentionIDs {
		if id == c.uid {
			return true
		}
	}
	return false
}

func (c *Client) CleanMentions(msg Incoming) string {
	text := msg.Text
	if len(msg.MentionIDs) == 0 || len(msg.MentionOffs) == 0 || len(msg.MentionLens) == 0 {
		return text
	}
	runes := []rune(text)
	var toRemove [][2]int
	for i, id := range msg.MentionIDs {
		if id != c.uid {
			continue
		}
		if i >= len(msg.MentionOffs) || i >= len(msg.MentionLens) {
			continue
		}
		start, end := msg.MentionOffs[i], msg.MentionOffs[i]+msg.MentionLens[i]
		if start >= 0 && end <= len(runes) {
			toRemove = append(toRemove, [2]int{start, end})
		}
	}
	for i := len(toRemove) - 1; i >= 0; i-- {
		start, end := toRemove[i][0], toRemove[i][1]
		runes = append(runes[:start], runes[end:]...)
	}
	return strings.TrimSpace(string(runes))
}

func (c *Client) SendText(ctx context.Context, threadID int64, text string) string {
	otid := time.Now().UnixMilli()

	if len(text) > maxMsgLen {
		chunks := splitMessage(text, maxMsgLen)
		var lastMsgID string
		for _, chunk := range chunks {
			otid = time.Now().UnixMilli()
			resp, err := c.client.ExecuteTasks(ctx, &socket.SendMessageTask{
				ThreadId:          threadID,
				Otid:              otid,
				Source:            table.MESSENGER_INBOX_IN_THREAD,
				SendType:          table.TEXT,
				Text:              chunk,
				SyncGroup:         1,
				InitiatingSource:  table.FACEBOOK_INBOX,
				SkipUrlPreviewGen: 1,
				MultiTabEnv:       0,
			})
			if err != nil {
				log.Printf("messenger: failed to send reply chunk: %v", err)
				return lastMsgID
			}
			if resp != nil {
				otidStr := fmt.Sprintf("%d", otid)
				for _, replace := range resp.LSReplaceOptimsiticMessage {
					if replace.OfflineThreadingId == otidStr {
						lastMsgID = replace.MessageId
						break
					}
				}
			}
		}
		return lastMsgID
	}

	resp, err := c.client.ExecuteTasks(ctx, &socket.SendMessageTask{
		ThreadId:          threadID,
		Otid:              otid,
		Source:            table.MESSENGER_INBOX_IN_THREAD,
		SendType:          table.TEXT,
		Text:              text,
		SyncGroup:         1,
		InitiatingSource:  table.FACEBOOK_INBOX,
		SkipUrlPreviewGen: 1,
		MultiTabEnv:       0,
	})
	if err != nil {
		log.Printf("messenger: failed to send reply: %v", err)
		return ""
	}
	var msgID string
	if resp != nil {
		otidStr := fmt.Sprintf("%d", otid)
		for _, replace := range resp.LSReplaceOptimsiticMessage {
			if replace.OfflineThreadingId == otidStr {
				msgID = replace.MessageId
				break
			}
		}
	}
	return msgID
}

func (c *Client) EditMessage(ctx context.Context, messageID string, text string) error {
	if messageID == "" {
		return nil
	}
	_, err := c.client.ExecuteTasks(ctx, &socket.EditMessageTask{
		MessageID: messageID,
		Text:      text,
	})
	if err != nil {
		return fmt.Errorf("edit message: %w", err)
	}
	return nil
}

func (c *Client) DeleteMessage(ctx context.Context, messageID string) error {
	_, err := c.client.ExecuteTasks(ctx, &socket.DeleteMessageTask{
		MessageId: messageID,
	})
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	return nil
}

func (c *Client) SendImage(ctx context.Context, threadID int64, imageData []byte, mimeType string) error {
	resp, err := c.client.SendMercuryUploadRequest(ctx, threadID, &messagix.MercuryUploadMedia{
		Filename:  "image.png",
		MimeType:  mimeType,
		MediaData: imageData,
	})
	if err != nil {
		c.SendText(ctx, threadID, "[image upload failed]")
		return err
	}
	attachmentID := resp.Payload.RealMetadata.GetFbId()
	if attachmentID == 0 {
		c.SendText(ctx, threadID, "[image upload returned no ID]")
		return fmt.Errorf("no attachment FBID returned from upload")
	}
	log.Printf("messenger: image uploaded (attachment %d)", attachmentID)

	otid := time.Now().UnixMilli()
	_, err = c.client.ExecuteTasks(ctx, &socket.SendMessageTask{
		ThreadId:          threadID,
		Otid:              otid,
		Source:            table.MESSENGER_INBOX_IN_THREAD,
		SendType:          table.MEDIA,
		AttachmentFBIds:   []int64{attachmentID},
		SyncGroup:         1,
		InitiatingSource:  table.FACEBOOK_INBOX,
		SkipUrlPreviewGen: 1,
		MultiTabEnv:       0,
	})
	if err != nil {
		return fmt.Errorf("send image message: %w", err)
	}
	return nil
}

// splitMessage breaks a long string into chunks, preferring to split at
// newlines or spaces near the max length boundary.
func splitMessage(text string, max int) []string {
	if len(text) <= max {
		return []string{text}
	}
	var chunks []string
	for len(text) > max {
		cut := max
		if idx := strings.LastIndex(text[:max], "\n"); idx > max/2 {
			cut = idx + 1
		} else if idx := strings.LastIndex(text[:max], " "); idx > max/2 {
			cut = idx + 1
		}
		chunks = append(chunks, strings.TrimSpace(text[:cut]))
		text = text[cut:]
	}
	if strings.TrimSpace(text) != "" {
		chunks = append(chunks, strings.TrimSpace(text))
	}
	return chunks
}