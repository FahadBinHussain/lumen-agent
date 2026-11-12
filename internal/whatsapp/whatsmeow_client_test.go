package whatsapp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestSplitTextUnicodeSafeAndLabeled(t *testing.T) {
	parts := splitText(strings.Repeat("বাংলা release line with a link https://example.com\n", 120), whatsappMaxTextBytes)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(parts))
	}
	for i, part := range parts {
		if len(part) > whatsappMaxTextBytes {
			t.Fatalf("part %d exceeds limit: %d bytes", i+1, len(part))
		}
		if !strings.HasPrefix(part, "[part ") {
			t.Fatalf("part %d is missing continuation label: %q", i+1, part[:min(len(part), 20)])
		}
		if !utf8.ValidString(part) {
			t.Fatalf("part %d is invalid UTF-8", i+1)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestCleanMentionsFromNames(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		names []string
		want  string
	}{
		{"no names keeps text", "hello @Kite how are you", nil, "hello @Kite how are you"},
		{"strips own mention token", "hello @Kite how are you", []string{"Kite"}, "hello how are you"},
		{"strips mention at start", "@Kite check this", []string{"Kite"}, "check this"},
		{"mention only becomes empty", "@Kite", []string{"Kite"}, ""},
		{"multiple names", "hi @Kite and @Ratul", []string{"Kite", "Ratul"}, "hi and"},
		{"name mismatch leaves text", "hello @Kite", []string{"Someone Else"}, "hello @Kite"},
		{"empty text stays empty", "", []string{"Kite"}, ""},
	}
	for _, tc := range cases {
		if got := cleanMentionsFromNames(tc.text, tc.names); got != tc.want {
			t.Errorf("%s: cleanMentionsFromNames(%q, %v) = %q, want %q", tc.name, tc.text, tc.names, got, tc.want)
		}
	}
}

// TestScheduleReconnectRetriesUntilConnectSucceeds pins the 2026-10-09 bug:
// one failed reconnect after a logout used to kill the chain for good (QR page
// stuck on "waiting" forever). A failed connect must be retried; only a
// successful one may release the single-flight flag.
func TestScheduleReconnectRetriesUntilConnectSucceeds(t *testing.T) {
	old := reconnectRetryDelay
	reconnectRetryDelay = time.Millisecond
	defer func() { reconnectRetryDelay = old }()

	var calls int32
	w := &WhatsmeowClient{
		logger: zerolog.Nop(),
		connectFn: func(context.Context) error {
			if atomic.AddInt32(&calls, 1) < 3 {
				return errors.New("socks connect tcp 127.0.0.1:1055->web.whatsapp.com:443: general SOCKS server failure")
			}
			return nil
		},
	}

	w.scheduleReconnect()

	deadline := time.Now().Add(3 * time.Second)
	for {
		w.mu.Lock()
		running := w.reconnecting
		w.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reconnect loop never finished: connect calls=%d", atomic.LoadInt32(&calls))
		}
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("expected at least 3 connect attempts (failures retried), got %d", got)
	}
}

// newLoggedInDeadClient builds a client in the 2026-11-12 silent-death
// state: logged in (device ID set) but the websocket never connected, so
// the real socket state is down. No database, no network.
func newLoggedInDeadClient(connectFn func(context.Context) error) *WhatsmeowClient {
	device := &store.Device{ID: &types.JID{User: "8801911104251", Server: types.DefaultUserServer}}
	cli := whatsmeow.NewClient(device, waLog.Noop)
	return &WhatsmeowClient{
		client:    cli,
		logger:    zerolog.Nop(),
		connectFn: connectFn,
	}
}

// TestIsConnectedReflectsRealSocketState pins the 2026-11-12 bug: the
// socket died with no Disconnected event, so the event-driven flag stayed
// true and /api/health lied for 4+ hours. IsConnected must read the real
// socket state, not the flag — a Connected event with a dead socket is
// still disconnected.
func TestIsConnectedReflectsRealSocketState(t *testing.T) {
	w := newLoggedInDeadClient(func(context.Context) error { return nil })
	if !w.IsLoggedIn() {
		t.Fatal("test setup broken: expected logged in")
	}
	w.handleEvent(&events.Connected{})
	if w.IsConnected() {
		t.Fatal("IsConnected() is true with a dead socket: it must reflect the real socket state, not the event flag")
	}
	if w.SocketConnected() {
		t.Fatal("SocketConnected() is true with no socket")
	}
	// The Threads wart rides along: a dead socket must error, not answer
	// an empty list that looks like "no chats".
	if _, err := w.Threads(context.Background()); err == nil {
		t.Fatal("Threads() with a dead socket returned no error")
	}
}

// TestWatchdogForcesReconnectOnDeadSocket pins the supervisor: a logged-in
// client with a dead socket must get a reconnect attempt within a couple
// of watchdog ticks, and the watchdog must keep retrying while the socket
// stays down (the stub connect never actually connects).
func TestWatchdogForcesReconnectOnDeadSocket(t *testing.T) {
	oldInterval, oldChecks := watchdogInterval, watchdogDeadChecks
	watchdogInterval, watchdogDeadChecks = 5*time.Millisecond, 1
	defer func() { watchdogInterval, watchdogDeadChecks = oldInterval, oldChecks }()

	var calls int32
	w := newLoggedInDeadClient(func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Watchdog(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&calls) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("watchdog never attempted a reconnect for a dead socket")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestStreamReplacedSchedulesReconnect pins the silent-death event:
// StreamReplaced arrives with no Disconnected event and whatsmeow does
// not retry, so the app must drive the reconnect itself — including
// while still logged in (the old scheduleReconnect loop broke out
// immediately when logged in).
func TestStreamReplacedSchedulesReconnect(t *testing.T) {
	old := reconnectRetryDelay
	reconnectRetryDelay = time.Millisecond
	defer func() { reconnectRetryDelay = old }()

	var calls int32
	w := newLoggedInDeadClient(func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})
	w.handleEvent(&events.StreamReplaced{})

	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&calls) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("StreamReplaced did not schedule a reconnect")
		}
		time.Sleep(time.Millisecond)
	}
}
