package messenger

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
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
	MessageID    string
	ThreadID     int64
	SenderID     int64
	Text         string
	Timestamp    int64
	ReplySource  string
	ReplyToUser  int64
	ReplySnippet string
	ReplyText    string
	MentionIDs   []int64
	MentionOffs  []int
	MentionLens  []int
}

type Handler func(ctx context.Context, msg Incoming)

type ReactionEvent struct {
	ThreadID  int64
	MessageID string
	ActorID   int64
	Reaction  string // unicode emoji, or empty string if reaction removed
	Timestamp int64
}

type ReactionHandler func(ctx context.Context, react ReactionEvent)

// dispatchItem is one inbound message parked for the dispatcher goroutine.
type dispatchItem struct {
	ctx context.Context
	msg Incoming
}

// ThreadInfo is one inbox thread the account has synced: its thread key and
// the most recent name + activity seen for it.
type ThreadInfo struct {
	ThreadID     int64
	Name         string
	LastActivity time.Time
}

type Client struct {
	clientMu        sync.RWMutex
	client          *messagix.Client
	uid             atomic.Int64
	platform        types.Platform
	handler         Handler
	reactionHandler ReactionHandler
	cookiesPath     string
	startTime   time.Time
	seenMu      sync.Mutex
	seen        map[string]time.Time
	threadsMu   sync.Mutex
	threads     map[int64]ThreadInfo

	connMu    sync.Mutex
	connected bool
	connEpoch atomic.Int64 // increments on each ConnectedEvent / ReconnectedEvent

	// authMu guards terminal credential/account halts (DGW 4003 Unauthorized,
	// Meta checkpoint/suspended/challenge/consent/1357053, invalid cookies).
	// When non-empty, all auto-reconnects and outbound tasks stop loudly until
	// fresh cookies are uploaded via ReloadCookies.
	authMu    sync.RWMutex
	authError string

	forceReconnMu   sync.Mutex
	lastForceReconn time.Time

	// Inbound dispatch (2026-10-09): messagix v0.2609 feeds every *table.LSTable
	// event through ONE frame-handler goroutine (dgw readLoop -> incoming chan
	// (cap 64) -> syncManager.HandleEvent). Running the bridge handler — and
	// with it the agent run and its SendText — on that goroutine self-deadlocks:
	// the send waits for a data frame only the goroutine we are blocking can
	// deliver. relay() therefore parks handler calls in dispatchQ and this
	// single ordered goroutine drains them off the messagix dispatch path.
	dispatchOnce sync.Once
	dispatchMu   sync.Mutex
	dispatchCond *sync.Cond
	dispatchQ    []dispatchItem

	// auto-reconnect: only runs for non-terminal transient failures; terminal
	// credential/account halts stop immediately so Meta is never hammered with
	// repeated /messages or handshake attempts on flagged cookies.
	reconnMu        sync.Mutex
	reconnScheduled bool
	reconnTimer     *time.Timer
	reconnFailures  int
	reconnectDelay  time.Duration // first attempt after a non-auth permanent error
	retryDelay      time.Duration // base backoff between non-terminal failed attempts
	reloadFn        func(context.Context) error
	reloadMu        sync.Mutex // serializes ReloadCookies (external upload vs auto-reconnect)
}

var (
	errInvalidCookies     = errors.New("invalid or missing cookies")
	maxReconnectBackoff   = 30 * time.Minute
	minForceReconnectGap  = 30 * time.Second
	sendEpochPollInterval = 100 * time.Millisecond
)

// IsConnected reports whether the DGW socket is currently up (Connected or
// Reconnected seen, no socket error since). Used by the bridge health watch.
func (c *Client) IsConnected() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.connected
}

func (c *Client) setConnected(v bool) {
	c.connMu.Lock()
	c.connected = v
	if v {
		c.connEpoch.Add(1)
	}
	c.connMu.Unlock()
}

func (c *Client) connectedEpoch() int64 {
	return c.connEpoch.Load()
}

// AuthError returns the terminal credential/account error that halted
// Messenger (4003 Unauthorized, checkpoint, suspension, invalid cookies), or ""
// when Messenger is not halted.
func (c *Client) AuthError() string {
	if c == nil {
		return ""
	}
	c.authMu.RLock()
	defer c.authMu.RUnlock()
	return c.authError
}

// IsAuthHalted reports whether auto-reconnect and sends are halted for a
// terminal credential or Meta account error.
func (c *Client) IsAuthHalted() bool {
	return c.AuthError() != ""
}

func (c *Client) haltAuth(reason string) {
	c.authMu.Lock()
	c.authError = reason
	c.authMu.Unlock()
	c.cancelPendingReconnect()
}

func (c *Client) clearAuthHalt() {
	c.authMu.Lock()
	c.authError = ""
	c.authMu.Unlock()
}

func (c *Client) cancelPendingReconnect() {
	c.reconnMu.Lock()
	c.reconnScheduled = false
	if c.reconnTimer != nil {
		c.reconnTimer.Stop()
		c.reconnTimer = nil
	}
	c.reconnFailures = 0
	c.reconnMu.Unlock()
}

func (c *Client) allowForceReconnect() bool {
	c.forceReconnMu.Lock()
	defer c.forceReconnMu.Unlock()
	if !c.lastForceReconn.IsZero() && time.Since(c.lastForceReconn) < minForceReconnectGap {
		return false
	}
	c.lastForceReconn = time.Now()
	return true
}

func isTerminalAuthError(err error) bool {
	if err == nil {
		return false
	}
	if websocket.CloseStatus(err) == dgw.CloseStatusUnauthorized {
		return true
	}
	if httpclient.IsPermanentRequestError(err) {
		return true
	}
	if errors.Is(err, errInvalidCookies) || errors.Is(err, os.ErrNotExist) || errors.Is(err, httpclient.ErrVersionIDNotFound) {
		return true
	}
	var lsErr *types.ErrorResponse
	if errors.As(err, &lsErr) && lsErr.ErrorCode == 1357053 {
		return true
	}
	return false
}

func isAlreadyClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, dgw.ErrSocketNotOpen) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "closed network connection") ||
		strings.Contains(msg, "socket is not open")
}

// ParsePlatform maps config strings ("messenger", "facebook", or empty for
// default "messenger") to mautrix-meta's types.Platform. Rejects unknown values loudly.
func ParsePlatform(raw string) (types.Platform, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "messenger":
		return types.Messenger, nil
	case "facebook":
		return types.Facebook, nil
	default:
		return types.Unset, fmt.Errorf("invalid messenger platform %q: must be \"messenger\" or \"facebook\"", raw)
	}
}

func validatePlatform(platform types.Platform) error {
	if platform != types.Messenger && platform != types.Facebook {
		return fmt.Errorf("unsupported messenger platform %q: must be messenger or facebook", platform.String())
	}
	return nil
}

func New(cookiesPath string, handler Handler) (*Client, error) {
	return NewWithPlatform(cookiesPath, types.Messenger, handler)
}

func NewWithPlatform(cookiesPath string, platform types.Platform, handler Handler) (*Client, error) {
	if err := validatePlatform(platform); err != nil {
		return nil, err
	}
	cookieMap, err := cookies.LoadFromFile(cookiesPath)
	if err != nil {
		return nil, fmt.Errorf("load cookies: %w", err)
	}

	c := cookies.ToMessagix(cookieMap, platform)
	if missing := cookies.GetMissing(c); len(missing) > 0 {
		return nil, fmt.Errorf("missing required cookies: %v", missing)
	}

	logger := zerolog.New(os.Stderr).With().Str("component", "messagix").Timestamp().Logger()
	client := messagix.NewClient(c, logger, &messagix.Config{})

	cl := newClientShell(client, cookiesPath, platform, handler)
	cl.startDispatcher()
	return cl, nil
}

// NewUnauthenticatedWithPlatform creates a Client ready for ReloadCookies
// without requiring valid cookies at boot (used so POST /api/cookies/upload
// can heal an initially unprovisioned or scrubbed bridge without a deploy crash).
func NewUnauthenticatedWithPlatform(cookiesPath string, platform types.Platform, handler Handler) (*Client, error) {
	if err := validatePlatform(platform); err != nil {
		return nil, err
	}
	cl := newClientShell(nil, cookiesPath, platform, handler)
	cl.startDispatcher()
	return cl, nil
}

func newClientShell(client *messagix.Client, cookiesPath string, platform types.Platform, handler Handler) *Client {
	cl := &Client{
		client:         client,
		platform:       platform,
		handler:        handler,
		cookiesPath:    cookiesPath,
		startTime:      time.Now(),
		seen:           make(map[string]time.Time),
		threads:        make(map[int64]ThreadInfo),
		reconnectDelay: 60 * time.Second,
		retryDelay:     5 * time.Minute,
	}
	cl.reloadFn = cl.ReloadCookies
	return cl
}

func (c *Client) currentClient() *messagix.Client {
	c.clientMu.RLock()
	defer c.clientMu.RUnlock()
	return c.client
}

func (c *Client) setClient(mc *messagix.Client) {
	c.clientMu.Lock()
	c.client = mc
	c.clientMu.Unlock()
}

func (c *Client) Platform() types.Platform {
	return c.platform
}

func (c *Client) SetHandler(handler Handler) {
	c.handler = handler
}

func (c *Client) SetReactionHandler(handler ReactionHandler) {
	c.reactionHandler = handler
}

func (c *Client) UID() int64 {
	return c.uid.Load()
}

func (c *Client) Login(ctx context.Context) (int64, string, error) {
	mc := c.currentClient()
	if mc == nil {
		return 0, "", fmt.Errorf("messenger cookies not loaded (upload fresh cookies)")
	}
	userInfo, _, err := mc.LoadMessagesPage(ctx)
	if err != nil {
		if isTerminalAuthError(err) {
			c.haltAuth(err.Error())
			log.Printf("messenger: AUTH HALTED on login (platform=%s): %v — auto-reconnect disabled until fresh cookies are uploaded", c.platform, err)
		}
		return 0, "", fmt.Errorf("load messages page: %w", err)
	}
	uid := userInfo.GetFBID()
	if uid == 0 {
		err := fmt.Errorf("load messages page: %w", httpclient.ErrUserIDIsZero)
		c.haltAuth(err.Error())
		return 0, "", err
	}
	c.clearAuthHalt()
	c.uid.Store(uid)
	return uid, userInfo.GetName(), nil
}

func (c *Client) Start(ctx context.Context) error {
	mc := c.currentClient()
	if mc == nil {
		return fmt.Errorf("messenger client not initialized (upload fresh cookies)")
	}
	mc.SetEventHandler(c.makeEventHandler(mc))
	go func() {
		err := mc.Connect(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("messenger: connection error: %v", err)
		}
	}()
	return nil
}

func (c *Client) ReloadCookies(ctx context.Context) error {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	if err := validatePlatform(c.platform); err != nil {
		return err
	}
	c.clearAuthHalt()
	c.cancelPendingReconnect()
	c.setConnected(false)

	cookieMap, err := cookies.LoadFromFile(c.cookiesPath)
	if err != nil {
		wrapped := fmt.Errorf("load cookies: %w (%w)", errInvalidCookies, err)
		c.haltAuth(wrapped.Error())
		c.setClient(nil)
		return wrapped
	}

	cc := cookies.ToMessagix(cookieMap, c.platform)
	if missing := cookies.GetMissing(cc); len(missing) > 0 {
		err := fmt.Errorf("%w: missing %v", errInvalidCookies, missing)
		c.haltAuth(err.Error())
		c.setClient(nil)
		return err
	}

	if old := c.currentClient(); old != nil {
		old.Disconnect()
	}
	c.uid.Store(0)
	c.setClient(nil)
	logger := zerolog.New(os.Stderr).With().Str("component", "messagix").Timestamp().Logger()
	newClient := messagix.NewClient(cc, logger, &messagix.Config{})

	userInfo, _, err := newClient.LoadMessagesPage(ctx)
	if err != nil {
		if isTerminalAuthError(err) {
			c.haltAuth(err.Error())
			log.Printf("messenger: AUTH HALTED during reload (platform=%s): %v — auto-reconnect disabled until fresh cookies are uploaded", c.platform, err)
		}
		return fmt.Errorf("load messages page: %w", err)
	}
	uid := userInfo.GetFBID()
	if uid == 0 {
		err := fmt.Errorf("load messages page: %w", httpclient.ErrUserIDIsZero)
		c.haltAuth(err.Error())
		return err
	}
	c.setClient(newClient)
	c.uid.Store(uid)
	log.Printf("messenger: cookies reloaded (platform=%s), logged in as %s (%d)", c.platform, userInfo.GetName(), uid)

	return c.Start(ctx)
}

func (c *Client) Disconnect() {
	c.cancelPendingReconnect()
	if mc := c.currentClient(); mc != nil {
		mc.Disconnect()
	}
}

// armReconnect schedules one auto-reconnect attempt unless one is already
// pending, the socket is back up, or auth is halted on a terminal error.
func (c *Client) armReconnect(reason string, delay time.Duration) {
	if c.IsAuthHalted() {
		log.Printf("messenger: auto-reconnect suppressed (%s): auth halted (%s)", reason, c.AuthError())
		return
	}
	c.reconnMu.Lock()
	if c.reconnScheduled || c.IsConnected() {
		c.reconnMu.Unlock()
		return
	}
	c.reconnScheduled = true
	c.reconnTimer = time.AfterFunc(delay, c.attemptReconnect)
	c.reconnMu.Unlock()
	log.Printf("messenger: auto-reconnect armed in %s (%s)", delay, reason)
}

func (c *Client) nextReconnectBackoff(failures int) time.Duration {
	base := c.retryDelay
	if base <= 0 {
		base = 5 * time.Minute
	}
	if failures <= 1 {
		return base
	}
	shift := failures - 1
	if shift > 3 {
		shift = 3
	}
	next := base << shift
	if base <= maxReconnectBackoff && next > maxReconnectBackoff {
		next = maxReconnectBackoff
	}
	return next
}

// attemptReconnect runs a single reload+relog cycle for non-terminal errors
// and backs off exponentially. Any terminal credential or Meta account error
// (4003 Unauthorized, checkpoint, suspension, invalidated token, 1357053)
// immediately halts auto-reconnect so Meta is never hammered with dead cookies.
func (c *Client) attemptReconnect() {
	c.reconnMu.Lock()
	c.reconnScheduled = false
	c.reconnTimer = nil
	c.reconnMu.Unlock()
	if c.IsConnected() || c.IsAuthHalted() {
		return
	}
	log.Printf("messenger: auto-reconnect: reloading cookies and relogging")
	// Background ctx on purpose: must not be canceled when the caller that
	// triggered the event returns (same trap as the old cookie-upload bug).
	if err := c.reloadFn(context.Background()); err != nil {
		if isTerminalAuthError(err) || c.IsAuthHalted() {
			if !c.IsAuthHalted() {
				c.haltAuth(err.Error())
			}
			log.Printf("messenger: AUTH HALTED — auto-reconnect stopping permanently on terminal error: %v", err)
			return
		}
		c.reconnMu.Lock()
		c.reconnFailures++
		nextDelay := c.nextReconnectBackoff(c.reconnFailures)
		c.reconnMu.Unlock()
		log.Printf("messenger: auto-reconnect reload failed: %v (retrying in %s)", err, nextDelay)
		c.armReconnect("reload failed", nextDelay)
		return
	}
	// the DGW connect after a reload is async - give it a moment before
	// deciding the reload didn't actually help.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !c.IsConnected() && !c.IsAuthHalted() {
		time.Sleep(time.Second)
	}
	if c.IsAuthHalted() {
		log.Printf("messenger: auto-reconnect aborted — auth halted during handshake (%s)", c.AuthError())
		return
	}
	if c.IsConnected() {
		c.reconnMu.Lock()
		c.reconnFailures = 0
		c.reconnMu.Unlock()
		log.Printf("messenger: auto-reconnect succeeded")
		return
	}
	c.reconnMu.Lock()
	c.reconnFailures++
	nextDelay := c.nextReconnectBackoff(c.reconnFailures)
	c.reconnMu.Unlock()
	log.Printf("messenger: auto-reconnect relogged but socket still down (retrying in %s)", nextDelay)
	c.armReconnect("socket still down after relog", nextDelay)
}

func (c *Client) makeEventHandler(mc *messagix.Client) func(context.Context, any) {
	return func(evtCtx context.Context, evt any) {
		switch e := evt.(type) {
		case *messagix.ConnectedEvent:
			c.clearAuthHalt()
			c.cancelPendingReconnect()
			c.setConnected(true)
			log.Printf("messenger: DGW connected")

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
			log.Printf("messenger: publish resp: threads=%d insert_msgs=%d upsert_msgs=%d reactions=%d delete_reactions=%d table=[%s]",
				len(e.LSDeleteThenInsertThread), len(ins), len(up),
				len(e.LSUpsertReaction), len(e.LSDeleteReaction),
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
					MessageID:    msg.MessageId,
					ThreadID:     msg.ThreadKey,
					SenderID:     msg.SenderId,
					Text:         msg.Text,
					Timestamp:    msg.TimestampMs,
					ReplySource:  msg.ReplySourceId,
					ReplyToUser:  msg.ReplyToUserId,
					ReplySnippet: msg.ReplySnippet,
					ReplyText:    msg.ReplyMessageText,
					MentionIDs:   parseMentionIDs(msg.MentionIds),
					MentionOffs:  parseMentionInts(msg.MentionOffsets),
					MentionLens:  parseMentionInts(msg.MentionLengths),
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
						MessageID:    msg.MessageId,
						ThreadID:     threadID,
						SenderID:     msg.SenderId,
						Text:         msg.Text,
						Timestamp:    msg.TimestampMs,
						ReplySource:  msg.ReplySourceId,
						ReplyToUser:  msg.ReplyToUserId,
						ReplySnippet: msg.ReplySnippet,
						ReplyText:    msg.ReplyMessageText,
						MentionIDs:   parseMentionIDs(msg.MentionIds),
						MentionOffs:  parseMentionInts(msg.MentionOffsets),
						MentionLens:  parseMentionInts(msg.MentionLengths),
					})
				}
			}

			for _, r := range e.LSUpsertReaction {
				if r == nil || r.MessageId == "" {
					continue
				}
				log.Printf("messenger: reaction upsert: actor=%d thread=%d msg=%s reaction=%q",
					r.ActorId, r.ThreadKey, r.MessageId, r.Reaction)
				if c.reactionHandler != nil {
					c.reactionHandler(evtCtx, ReactionEvent{
						ThreadID:  r.ThreadKey,
						MessageID: r.MessageId,
						ActorID:   r.ActorId,
						Reaction:  r.Reaction,
						Timestamp: r.TimestampMs,
					})
				}
			}

			for _, r := range e.LSDeleteReaction {
				if r == nil || r.MessageId == "" {
					continue
				}
				log.Printf("messenger: reaction delete: actor=%d thread=%d msg=%s",
					r.ActorId, r.ThreadKey, r.MessageId)
				if c.reactionHandler != nil {
					c.reactionHandler(evtCtx, ReactionEvent{
						ThreadID:  r.ThreadKey,
						MessageID: r.MessageId,
						ActorID:   r.ActorId,
						Reaction:  "",
						Timestamp: time.Now().UnixMilli(),
					})
				}
			}

			// Advance sync cursors carried in the table (mirrors upstream
			// connector); without this, Meta sees a client that never acks
			// its transactions.
			if mc != nil {
				mc.PostHandlePublishResponse(e)
			}

		case *messagix.TransientDisconnectEvent:
			c.setConnected(false)
			log.Printf("messenger: socket error (attempts %d): %v", e.ConnectionAttempts, e.Err)

		case *messagix.PermanentErrorEvent:
			c.setConnected(false)
			log.Printf("messenger: permanent error: %v", e.Err)
			if websocket.CloseStatus(e.Err) == dgw.CloseStatusUnauthorized || isTerminalAuthError(e.Err) {
				c.haltAuth(e.Err.Error())
				log.Printf("messenger: AUTH HALTED (%v) — auto-reconnect stopped to prevent Meta ban/lock escalation; upload fresh cookies after resolving account state", e.Err)
			} else {
				c.armReconnect("permanent error", c.reconnectDelay)
			}

		case *messagix.ReconnectedEvent:
			c.clearAuthHalt()
			c.cancelPendingReconnect()
			c.setConnected(true)
			log.Printf("messenger: DGW reconnected")

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
	if c == nil || c.IsAuthHalted() {
		return
	}
	mc := c.currentClient()
	if mc == nil || (c.UID() == 0 && !c.IsConnected()) {
		return
	}
	_, err := mc.ExecuteTasks(ctx, &socket.FetchThreadsTask{SyncGroup: 1})
	if err != nil {
		log.Printf("messenger: thread sync nudge failed: %v", err)
	}
}

func (c *Client) relay(ctx context.Context, msg Incoming) {
	// Arrival/drop logging (added 2026-10-08): every incoming messenger message
	// was invisible in Render logs unless it triggered a reply, which made
	// "bot not responding" impossible to diagnose. These three lines make the
	// path observable: arrival is logged in the bridge handler, drops here.
	uid := c.UID()
	if msg.SenderID == uid || msg.SenderID == 0 {
		log.Printf("messenger: relay drop: self or zero sender (sender=%d uid=%d thread=%d text=%.80q)", msg.SenderID, uid, msg.ThreadID, msg.Text)
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
		c.enqueueDispatch(ctx, msg)
	}
}

// startDispatcher boots the single ordered handler goroutine once (idempotent
// — called from New and from every enqueue).
func (c *Client) startDispatcher() {
	c.dispatchOnce.Do(func() {
		c.dispatchCond = sync.NewCond(&c.dispatchMu)
		go c.dispatchLoop()
	})
}

// enqueueDispatch parks a message for dispatchLoop instead of running the
// handler inline. WHY (root cause of the lost replies on 2026-10-08/09): the
// handler ran on messagix's single frame-handler goroutine, so the agent run's
// own SendText waited for a data frame that only the goroutine we were blocking
// could deliver — a structural 5s `data receive timed out`, and worse,
// ForceReconnect couldn't reconnect either (readLoop's wg.Wait waits for the
// stuck frame-handler goroutine), so every retry burned against a stale
// canSendMessages flag until the handler finally returned (the 02:29:29-51 log
// blackout). The dispatcher runs handlers off that goroutine, in arrival order,
// with a context that outlives the event dispatch (WithoutCancel — same trap as
// the cookie-upload bug).
func (c *Client) enqueueDispatch(ctx context.Context, msg Incoming) {
	c.startDispatcher()
	c.dispatchMu.Lock()
	c.dispatchQ = append(c.dispatchQ, dispatchItem{ctx: context.WithoutCancel(ctx), msg: msg})
	queued := len(c.dispatchQ)
	c.dispatchCond.Signal()
	c.dispatchMu.Unlock()
	if queued > 1 {
		log.Printf("messenger: dispatcher busy (%d messages waiting), thread %d takes its turn",
			queued-1, msg.ThreadID)
	}
}

// dispatchLoop drains queued messages one at a time in arrival order — serial
// per client, exactly the ordering the inline path had.
func (c *Client) dispatchLoop() {
	for {
		c.dispatchMu.Lock()
		for len(c.dispatchQ) == 0 {
			c.dispatchCond.Wait()
		}
		item := c.dispatchQ[0]
		c.dispatchQ = c.dispatchQ[1:]
		c.dispatchMu.Unlock()
		c.runDispatched(item)
	}
}

// runDispatched calls the handler; a panic must not kill the dispatcher — a
// dead dispatcher would strand every later message in the queue (silent
// failure), so it is recovered and logged loudly here.
func (c *Client) runDispatched(item dispatchItem) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("messenger: PANIC in message handler — dispatcher survives and keeps draining: %v\n%s",
				r, debug.Stack())
		}
	}()
	if c.handler != nil {
		c.handler(item.ctx, item.msg)
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
	uid := c.UID()
	for _, id := range msg.MentionIDs {
		if id == uid {
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
	uid := c.UID()
	runes := []rune(text)
	var toRemove [][2]int
	for i, id := range msg.MentionIDs {
		if id != uid {
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

// sendRetryBackoffs paces retries AFTER the forced reconnect has had its
// readiness wait time out — only then do we sleep this long before trying
// again. Five attempts total.
var sendRetryBackoffs = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}

// sendReadyTimeout bounds WaitUntilCanSendMessages between attempts: the
// socket normally comes back in 1-4s after ForceReconnect. (Vars, not consts,
// so the test suite can shrink them.)
var sendReadyTimeout = 15 * time.Second

// sendReconnectGrace gives the connection loop a beat to observe the forced
// close (and clear canSendMessages) before we wait on readiness — otherwise
// the wait returns instantly against the stale flag.
var sendReconnectGrace = 1 * time.Second

// retryLSTable runs op until it succeeds or the schedule is exhausted (5
// attempts total). The SAME task closure is reused so the otid stays stable
// across retries and Meta dedupes a send that actually landed even though its
// response timed out. On every failure it nudges the transport (ForceReconnect
// — a genuinely dead socket otherwise waits out the read pump, see the
// 2026-10-09 01:51Z incident where 4 blind retries burned out 1s before the
// socket came back at 01:52:13), waits for send readiness, then retries.
// NOTE: the original `data receive timed out` failures were NEVER Meta
// stalling — they were our own self-deadlock (handler running on messagix's
// frame-handler goroutine, see enqueueDispatch); with async dispatch attempt 1
// should just succeed, and this retry only ever covers real transport
// failures, which are now recoverable because the frame goroutine is free to
// notice the forced close. Every step is logged loudly; only the final failure
// is returned.
func retryLSTable(
	ctx context.Context,
	what string,
	op func() (*table.LSTable, error),
	nudge func(error),
	waitReady func() error,
) (*table.LSTable, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, err := op()
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		if isTerminalAuthError(err) {
			return nil, fmt.Errorf("terminal auth error (not retrying): %w", err)
		}
		lastErr = err
		if attempt >= len(sendRetryBackoffs) {
			return nil, fmt.Errorf("gave up after %d attempts: %w", attempt+1, lastErr)
		}
		log.Printf("messenger: %s failed (attempt %d/%d): %v — reconnecting socket, then retrying",
			what, attempt+1, len(sendRetryBackoffs)+1, err)
		nudge(err)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("canceled during retry: %w", lastErr)
		case <-time.After(sendReconnectGrace):
		}
		if werr := waitReady(); werr != nil {
			if isTerminalAuthError(werr) {
				return nil, fmt.Errorf("terminal auth error during retry wait: %w", werr)
			}
			log.Printf("messenger: %s: socket not ready: %v — backing off %v", what, werr, sendRetryBackoffs[attempt])
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("canceled during retry: %w", lastErr)
			case <-time.After(sendRetryBackoffs[attempt]):
			}
		} else {
			log.Printf("messenger: %s: socket ready, retrying", what)
		}
	}
}

func (c *Client) waitForSocketReady(ctx context.Context, timeout time.Duration, requireMinEpoch int64, forcedReconnect bool, lastErr error) error {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if authErr := c.AuthError(); authErr != "" {
			return fmt.Errorf("%w: %s", errInvalidCookies, authErr)
		}
		mc := c.currentClient()
		if mc == nil {
			return fmt.Errorf("messenger client not initialized (upload fresh cookies)")
		}
		if c.IsConnected() && mc.IsConnected() && c.connectedEpoch() >= requireMinEpoch {
			rem := time.Until(deadline)
			if rem <= 0 {
				rem = time.Millisecond
			}
			return mc.WaitUntilCanSendMessages(ctx, rem)
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			if forcedReconnect {
				return fmt.Errorf("timeout waiting for fresh reconnect after %v (last err: %v)", timeout, lastErr)
			}
			return fmt.Errorf("timeout waiting for socket readiness after %v", timeout)
		}
		sleep := sendEpochPollInterval
		if sleep > rem {
			sleep = rem
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
}

// executeRetry is the messagix-task wrapper around retryLSTable: one task,
// retried safely with at most one cooldown-gated ForceReconnect and real epoch
// waiting so retries cannot slam Meta's WebSocket with rapid open/close bursts.
func (c *Client) executeRetry(ctx context.Context, what string, task socket.Task) (*table.LSTable, error) {
	if c == nil {
		return nil, fmt.Errorf("messenger client not initialized (upload fresh cookies)")
	}
	if authErr := c.AuthError(); authErr != "" {
		return nil, fmt.Errorf("messenger auth halted (%s) — upload fresh cookies after resolving", authErr)
	}
	if c.currentClient() == nil {
		return nil, fmt.Errorf("messenger client not initialized (upload fresh cookies)")
	}
	if c.UID() == 0 && !c.IsConnected() {
		return nil, fmt.Errorf("messenger not logged in (upload fresh cookies)")
	}

	var nudgeMu sync.Mutex
	nudged := false
	forcedReconnectThisCall := false
	var targetEpoch int64
	var nudgeErr error

	return retryLSTable(ctx, what,
		func() (*table.LSTable, error) {
			if authErr := c.AuthError(); authErr != "" {
				return nil, fmt.Errorf("%w: %s", errInvalidCookies, authErr)
			}
			mc := c.currentClient()
			if mc == nil {
				return nil, fmt.Errorf("messenger client not initialized (upload fresh cookies)")
			}
			return mc.ExecuteTasks(ctx, task)
		},
		func(err error) {
			nudgeMu.Lock()
			defer nudgeMu.Unlock()
			nudgeErr = err
			epochBefore := c.connectedEpoch()
			targetEpoch = epochBefore
			forcedReconnectThisCall = false

			mc := c.currentClient()
			if mc == nil || c.IsAuthHalted() {
				return
			}
			if nudged || isAlreadyClosedConnErr(err) || !mc.IsConnected() || !c.allowForceReconnect() {
				log.Printf("messenger: %s: skipping ForceReconnect (nudged=%v, socket_up=%v, err=%v)",
					what, nudged, mc.IsConnected(), err)
				return
			}
			nudged = true
			forcedReconnectThisCall = true
			targetEpoch = epochBefore + 1
			log.Printf("messenger: %s: forcing single socket reconnect (epoch=%d)", what, epochBefore)
			mc.ForceReconnect()
		},
		func() error {
			nudgeMu.Lock()
			forced := forcedReconnectThisCall
			reqEpoch := targetEpoch
			lastErr := nudgeErr
			nudgeMu.Unlock()

			if !forced && c.IsConnected() {
				return fmt.Errorf("reconnect cooldown active; backing off before retry")
			}
			return c.waitForSocketReady(ctx, sendReadyTimeout, reqEpoch, forced, lastErr)
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
				log.Printf("messenger: send chunk returned no message id (otid=%d, replaces=%d, tables=%s) — using synthetic otid", otid, n, nonEmptyTableFields(resp))
				lastMsgID = fmt.Sprintf("otid_%d", otid)
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
		log.Printf("messenger: send returned no message id (otid=%d, replaces=%d, tables=%s) — using synthetic otid", otid, n, nonEmptyTableFields(resp))
		msgID = fmt.Sprintf("otid_%d", otid)
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

func (c *Client) SendReaction(ctx context.Context, threadID int64, messageID string, reaction string) error {
	_, err := c.executeRetry(ctx, "send reaction", &socket.SendReactionTask{
		ThreadKey:       threadID,
		TimestampMs:     time.Now().UnixMilli(),
		MessageID:       messageID,
		Reaction:        reaction,
		ActorID:         c.UID(),
		SyncGroup:       1,
		SendAttribution: table.MESSENGER_INBOX_IN_THREAD,
	})
	if err != nil {
		return fmt.Errorf("send reaction: %w", err)
	}
	return nil
}

func (c *Client) SendImage(ctx context.Context, threadID int64, imageData []byte, mimeType string) error {
	if c == nil {
		return fmt.Errorf("messenger client not initialized (upload fresh cookies)")
	}
	if authErr := c.AuthError(); authErr != "" {
		return fmt.Errorf("messenger auth halted (%s) — upload fresh cookies after resolving", authErr)
	}
	mc := c.currentClient()
	if mc == nil {
		return fmt.Errorf("messenger client not initialized (upload fresh cookies)")
	}
	if c.UID() == 0 && !c.IsConnected() {
		return fmt.Errorf("messenger not logged in (upload fresh cookies)")
	}
	resp, err := mc.GetHTTP().SendMercuryUploadRequest(ctx, threadID, &httpclient.MercuryUploadMedia{
		Filename:  "image.png",
		MimeType:  mimeType,
		MediaData: imageData,
	})
	if err != nil {
		if isTerminalAuthError(err) {
			c.haltAuth(err.Error())
			return fmt.Errorf("messenger auth halted (%s): %w", err.Error(), err)
		}
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
