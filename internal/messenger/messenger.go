package messenger

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"element-orion/internal/cookies"
)

// Messenger's edit endpoint has shown a lower effective limit than normal
// sends. Keep a conservative ceiling so edited notifications are not silently
// truncated by Meta before the continuation messages arrive.
const maxMsgLen = 900 // bytes, not Unicode characters

type Incoming struct {
	MessageID   string
	ThreadID    int64
	SenderID    int64
	Text        string
	Timestamp   int64
	ReplySource string
	ReplyToUser int64
	MentionIDs  []int64
	MentionOffs []int
	MentionLens []int
}

type Handler func(ctx context.Context, msg Incoming)

// ThreadInfo is one inbox thread the account has synced: its thread key and
// the most recent name + activity seen for it.
type ThreadInfo struct {
	ThreadID     int64
	Name         string
	LastActivity time.Time
}

type Client struct {
	client      *messagix.Client
	uid         int64
	platform    types.Platform
	handler     Handler
	cookiesPath string
	startTime   time.Time
	seenMu      sync.Mutex
	seen        map[string]time.Time
	threadsMu   sync.Mutex
	threads     map[int64]ThreadInfo

	connMu    sync.Mutex
	connected bool

	// auto-reconnect (2026-10-04): a messagix permanent error used to be
	// terminal - the client logged it and gave up forever, so a single
	// refused reconnect (connection code 24) left messenger dead until an
	// external refresh (the 2026-10-03 17h outage). armReconnect keeps a
	// full reload+relog cycle going until the socket is back.
	reconnMu        sync.Mutex
	reconnScheduled bool
	reconnectDelay  time.Duration // first attempt after a permanent error
	retryDelay      time.Duration // backoff between failed attempts
	reloadFn        func(context.Context) error
	reloadMu        sync.Mutex // serializes ReloadCookies (external upload vs auto-reconnect)
	keepaliveOnce   sync.Once  // one keepalive ticker across reloads
}

// IsConnected reports whether the MQTT socket is currently up (Ready or
// Reconnected seen, no socket error since). Used by the bridge health watch.
func (c *Client) IsConnected() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.connected
}

func (c *Client) setConnected(v bool) {
	c.connMu.Lock()
	c.connected = v
	c.connMu.Unlock()
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
	client := messagix.NewClient(c, logger, &messagix.Config{})

	cl := &Client{
		client:         client,
		platform:       types.Messenger,
		handler:        handler,
		cookiesPath:    cookiesPath,
		startTime:      time.Now(),
		seen:           make(map[string]time.Time),
		threads:        make(map[int64]ThreadInfo),
		reconnectDelay: 60 * time.Second,
		retryDelay:     5 * time.Minute,
	}
	cl.reloadFn = cl.ReloadCookies
	return cl, nil
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
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	c.setConnected(false)
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
	c.client = messagix.NewClient(cc, logger, &messagix.Config{})

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

// armReconnect schedules one auto-reconnect attempt unless one is already
// pending or the socket is back up. The timer path (attemptReconnect) is the
// only thing that clears the pending flag, so events + keepalive ticks can
// call this freely without stacking attempts.
func (c *Client) armReconnect(reason string, delay time.Duration) {
	c.reconnMu.Lock()
	if c.reconnScheduled || c.IsConnected() {
		c.reconnMu.Unlock()
		return
	}
	c.reconnScheduled = true
	c.reconnMu.Unlock()
	log.Printf("messenger: auto-reconnect armed in %s (%s)", delay, reason)
	time.AfterFunc(delay, c.attemptReconnect)
}

// attemptReconnect does a full reload+relog cycle (same path as the cookie
// upload endpoint, but with the cookies already on disk) and re-arms itself
// until the socket is actually up. Never gives up: a refused reconnect used
// to be terminal (17h dead, 2026-10-03), now it retries every retryDelay
// while the external cookie watchdog can still push fresh cookies on top.
func (c *Client) attemptReconnect() {
	c.reconnMu.Lock()
	c.reconnScheduled = false
	c.reconnMu.Unlock()
	if c.IsConnected() {
		return
	}
	log.Printf("messenger: auto-reconnect: reloading cookies and relogging")
	// Background ctx on purpose: must not be canceled when the caller that
	// triggered the event returns (same trap as the old cookie-upload bug).
	if err := c.reloadFn(context.Background()); err != nil {
		log.Printf("messenger: auto-reconnect reload failed: %v (retrying in %s)", err, c.retryDelay)
		c.armReconnect("reload failed", c.retryDelay)
		return
	}
	// the MQTT connect after a reload is async - give it a moment before
	// deciding the reload didn't actually help.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !c.IsConnected() {
		time.Sleep(time.Second)
	}
	if c.IsConnected() {
		log.Printf("messenger: auto-reconnect succeeded")
		return
	}
	log.Printf("messenger: auto-reconnect relogged but socket still down (retrying in %s)", c.retryDelay)
	c.armReconnect("socket still down after relog", c.retryDelay)
}

func (c *Client) makeEventHandler(ctx context.Context) func(context.Context, any) {
	return func(evtCtx context.Context, evt any) {
		switch e := evt.(type) {
		case *messagix.ConnectedEvent:
			c.setConnected(true)
			log.Printf("messenger: MQTT connected")
			c.keepaliveOnce.Do(func() {
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
							// safety net: if the socket is still down when the
							// keepalive fires, arm the full reconnect (covers
							// deaths that produced no permanent-error event).
							if !c.IsConnected() {
								c.armReconnect("keepalive sees disconnected", c.retryDelay)
							}
						}
					}
				}()
			})

		case *table.LSTable:
			// Arrival observability (2026-10-08): no incoming message ever
			// produced a log line unless it triggered a reply, so "Meta never
			// pushed it" and "decode dropped it" and "handler ignored it" were
			// indistinguishable. Log every incoming table's summary. Since
			// mautrix-meta v0.2609, both live deltas and connect-time sync
			// responses arrive here as raw *table.LSTable.
			if e == nil {
				log.Printf("messenger: publish resp: nil table")
				break
			}
			up, ins := e.WrapMessages()
			log.Printf("messenger: publish resp: threads=%d insert_msgs=%d upsert_msgs=%d table=[%s]",
				len(e.LSDeleteThenInsertThread), len(ins), len(up),
				nonEmptyTableFields(e))
			for _, th := range e.LSDeleteThenInsertThread {
				if th == nil || th.GetThreadKey() == 0 {
					continue
				}
				info := ThreadInfo{
					ThreadID: th.GetThreadKey(),
					Name:     th.GetThreadName(),
				}
				if ts := th.LastActivityTimestampMs; ts > 0 {
					info.LastActivity = time.UnixMilli(ts)
				}
				c.upsertThread(info)
			}

			for _, msg := range ins {
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

			for threadID, upsert := range up {
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
			// Advance sync cursors carried in the table (mirrors upstream
			// connector); without this, Meta sees a client that never acks
			// its transactions.
			c.client.PostHandlePublishResponse(e)

		case *messagix.TransientDisconnectEvent:
			c.setConnected(false)
			log.Printf("messenger: socket error (attempts %d): %v", e.ConnectionAttempts, e.Err)

		case *messagix.PermanentErrorEvent:
			c.setConnected(false)
			log.Printf("messenger: permanent error: %v", e.Err)
			c.armReconnect("permanent error", c.reconnectDelay)

		case *messagix.ReconnectedEvent:
			c.setConnected(true)
			log.Printf("messenger: MQTT reconnected")

		default:
			// Never swallow silently: an unknown event type means inbound
			// messages could vanish without a trace.
			log.Printf("messenger: UNRECOGNIZED messagix event type %T", evt)
		}
	}
}

func (c *Client) upsertThread(info ThreadInfo) {
	c.threadsMu.Lock()
	defer c.threadsMu.Unlock()
	cur, ok := c.threads[info.ThreadID]
	if info.Name != "" {
		cur.Name = info.Name
	}
	if !info.LastActivity.IsZero() {
		cur.LastActivity = info.LastActivity
	}
	if !ok && cur.Name == "" && cur.LastActivity.IsZero() {
		cur = info
	}
	cur.ThreadID = info.ThreadID
	c.threads[info.ThreadID] = cur
}

// Threads returns the inbox threads synced so far, most recent first. The
// cache fills from MQTT sync data; a container boot starts sparse until
// traffic/sync arrives.
func (c *Client) Threads() []ThreadInfo {
	c.threadsMu.Lock()
	defer c.threadsMu.Unlock()
	out := make([]ThreadInfo, 0, len(c.threads))
	for _, t := range c.threads {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastActivity.After(out[j].LastActivity)
	})
	return out
}

// NudgeThreadSync asks the server to push inbox thread metadata (sync group 1
// = full thread info, matching mautrix-meta's connector).
func (c *Client) NudgeThreadSync(ctx context.Context) {
	if c.client == nil {
		return
	}
	_, err := c.client.ExecuteTasks(ctx, &socket.FetchThreadsTask{SyncGroup: 1})
	if err != nil {
		log.Printf("messenger: thread sync nudge failed: %v", err)
	}
}

func (c *Client) relay(ctx context.Context, msg Incoming) {
	// Arrival/drop logging (added 2026-10-08): every incoming messenger message
	// was invisible in Render logs unless it triggered a reply, which made
	// "bot not responding" impossible to diagnose. These three lines make the
	// path observable: arrival is logged in the bridge handler, drops here.
	if msg.SenderID == c.uid || msg.SenderID == 0 {
		log.Printf("messenger: relay drop: self or zero sender (sender=%d uid=%d thread=%d text=%.80q)", msg.SenderID, c.uid, msg.ThreadID, msg.Text)
		return
	}
	ts := time.UnixMilli(msg.Timestamp)
	if !ts.IsZero() && ts.Before(c.startTime.Add(-5*time.Second)) {
		log.Printf("messenger: relay drop: stale timestamp %s (client started %s, thread=%d text=%.80q)", ts.Format(time.RFC3339), c.startTime.Format(time.RFC3339), msg.ThreadID, msg.Text)
		return
	}
	// Meta redelivers messages that were queued during a socket drop (the
	// original send timestamp stays after boot, so the time guard above does
	// NOT catch them — they arrive again on reconnect and would ghost-reply).
	// Dedup on message id, keeping recent ids only (prune on growth).
	if msg.MessageID != "" {
		c.seenMu.Lock()
		if _, dup := c.seen[msg.MessageID]; dup {
			c.seenMu.Unlock()
			log.Printf("messenger: skip redelivered message %s", msg.MessageID)
			return
		}
		if len(c.seen) > 500 {
			cutoff := time.Now().Add(-24 * time.Hour)
			for id, t := range c.seen {
				if t.Before(cutoff) {
					delete(c.seen, id)
				}
			}
		}
		c.seen[msg.MessageID] = time.Now()
		c.seenMu.Unlock()
	}
	if c.handler != nil {
		c.handler(ctx, msg)
	}
}

// nonEmptyTableFields names every non-empty LSTable field with its row count.
// WrapMessages only reads a handful of message fields; when Meta delivers
// message rows through a newer op (e.g. LSDeleteThenInsertMessage) the wrap
// count stays 0 and the message silently vanishes — this dump names the field
// that actually carries it (added 2026-10-08 for the "bot ignores /ai hi"
// investigation).
func nonEmptyTableFields(t *table.LSTable) string {
	if t == nil {
		return "nil"
	}
	v := reflect.ValueOf(t).Elem()
	typ := v.Type()
	parts := make([]string, 0, 8)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Slice && f.Len() > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", typ.Field(i).Name, f.Len()))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
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

// sendRetryBackoffs is the pause between attempts when a messagix task hits a
// transient socket failure (the DGW one-off stream times out or resets when
// Meta drops the connection mid-send; the socket reconnects within a few
// seconds, so a later attempt lands).
var sendRetryBackoffs = []time.Duration{1 * time.Second, 3 * time.Second, 5 * time.Second}

// retryLSTable runs op until it succeeds or the backoff schedule is exhausted
// (4 attempts total). The SAME op result is kept — callers pass a closure over
// one task instance so the otid stays stable across retries and Meta dedupes a
// send that actually landed even though its response timed out. Every retry is
// logged loudly; only the final failure is returned.
func retryLSTable(ctx context.Context, what string, op func() (*table.LSTable, error)) (*table.LSTable, error) {
	for attempt := 0; ; attempt++ {
		resp, err := op()
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		if attempt >= len(sendRetryBackoffs) {
			return nil, fmt.Errorf("gave up after %d attempts: %w", attempt+1, err)
		}
		log.Printf("messenger: %s failed (attempt %d/%d): %v — retrying in %v",
			what, attempt+1, len(sendRetryBackoffs)+1, err, sendRetryBackoffs[attempt])
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("canceled during retry: %w", err)
		case <-time.After(sendRetryBackoffs[attempt]):
		}
	}
}

// executeRetry is the messagix-task wrapper around retryLSTable: one task,
// retried on transient socket errors.
func (c *Client) executeRetry(ctx context.Context, what string, task socket.Task) (*table.LSTable, error) {
	return retryLSTable(ctx, what, func() (*table.LSTable, error) {
		return c.client.ExecuteTasks(ctx, task)
	})
}

func (c *Client) SendText(ctx context.Context, threadID int64, text string) string {
	otid := time.Now().UnixMilli()

	if utf8.RuneCountInString(text) > maxMsgLen {
		chunks := splitMessage(text, maxMsgLen)
		var lastMsgID string
		for _, chunk := range chunks {
			otid = time.Now().UnixMilli()
			resp, err := c.executeRetry(ctx, "send reply chunk", &socket.SendMessageTask{
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
			if lastMsgID == "" {
				n := 0
				if resp != nil {
					n = len(resp.LSReplaceOptimsiticMessage)
				}
				log.Printf("messenger: send chunk returned no message id (otid=%d, replaces=%d) — Meta may have rejected the message", otid, n)
			}
		}
		return lastMsgID
	}

	resp, err := c.executeRetry(ctx, "send reply", &socket.SendMessageTask{
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
	if msgID == "" {
		n := 0
		if resp != nil {
			n = len(resp.LSReplaceOptimsiticMessage)
		}
		log.Printf("messenger: send returned no message id (otid=%d, replaces=%d) — Meta may have rejected the message", otid, n)
	}
	return msgID
}

func (c *Client) EditMessage(ctx context.Context, messageID string, text string) error {
	return c.EditMessageWithContinuation(ctx, 0, messageID, text)
}

// EditMessageWithContinuation edits the existing message with the first
// chunk and sends any remaining chunks as new messages in the same thread.
// This is needed for edited notifications because platform edit APIs cannot
// turn one message into multiple messages.
func (c *Client) EditMessageWithContinuation(ctx context.Context, threadID int64, messageID string, text string) error {
	if messageID == "" {
		return nil
	}
	chunks := splitMessage(text, maxMsgLen)
	_, err := c.executeRetry(ctx, "edit message", &socket.EditMessageTask{
		MessageID: messageID,
		Text:      chunks[0],
	})
	if err != nil {
		return fmt.Errorf("edit message: %w", err)
	}
	for _, chunk := range chunks[1:] {
		if threadID == 0 {
			return fmt.Errorf("long edit requires messenger thread id")
		}
		if id := c.SendText(ctx, threadID, chunk); id == "" {
			return fmt.Errorf("send edited message continuation failed")
		}
	}
	return nil
}

func (c *Client) DeleteMessage(ctx context.Context, messageID string) error {
	_, err := c.executeRetry(ctx, "delete message", &socket.DeleteMessageTask{
		MessageId: messageID,
	})
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	return nil
}

func (c *Client) SendImage(ctx context.Context, threadID int64, imageData []byte, mimeType string) error {
	resp, err := c.client.GetHTTP().SendMercuryUploadRequest(ctx, threadID, &httpclient.MercuryUploadMedia{
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
	_, err = c.executeRetry(ctx, "send image message", &socket.SendMessageTask{
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

// splitMessage breaks a long string into Unicode-safe, labeled chunks,
// preferring to split at newlines or spaces near the max length boundary.
func splitMessage(text string, max int) []string {
	if len(text) <= max {
		return []string{text}
	}
	// Reserve room for the continuation label. This keeps every final
	// Messenger message below the platform limit.
	bodyMax := max - 32
	if bodyMax < 1 {
		bodyMax = max
	}
	chunks := splitMessageBytes(text, bodyMax)
	for i := range chunks {
		chunks[i] = fmt.Sprintf("[part %d/%d]\n%s", i+1, len(chunks), chunks[i])
	}
	return chunks
}

func splitMessageBytes(text string, max int) []string {
	runes := []rune(text)
	var chunks []string
	for len(runes) > 0 {
		cut := 0
		bytes := 0
		for cut < len(runes) {
			n := utf8.RuneLen(runes[cut])
			if bytes+n > max {
				break
			}
			bytes += n
			cut++
		}
		if cut == len(runes) {
			chunks = append(chunks, strings.TrimSpace(string(runes)))
			break
		}
		for i := cut - 1; i > cut/2; i-- {
			if runes[i] == '\n' || runes[i] == ' ' {
				cut = i + 1
				break
			}
		}
		chunks = append(chunks, strings.TrimSpace(string(runes[:cut])))
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
	}
	return chunks
}
