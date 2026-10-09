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
