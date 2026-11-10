package messenger

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

func TestAutoReconnectStopsWhenConnected(t *testing.T) {
	c := &Client{
		reconnectDelay: 10 * time.Millisecond,
		retryDelay:     time.Hour, // would be visible if wrongly re-armed
	}
	var calls int32
	c.reloadFn = func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	c.setConnected(true)
	c.armReconnect("test", 10*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("connected client must not reload, got %d calls", n)
	}
}

func TestAutoReconnectSingleFlight(t *testing.T) {
	c := &Client{
		reconnectDelay: 30 * time.Millisecond,
		retryDelay:     time.Hour, // failed attempt re-arms this far out
	}
	var calls int32
	c.reloadFn = func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("boom")
	}
	c.armReconnect("first", 30*time.Millisecond)
	c.armReconnect("second", 30*time.Millisecond) // must be deduped
	c.armReconnect("third", 30*time.Millisecond)  // must be deduped
	time.Sleep(150 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("expected exactly 1 attempt while armed, got %d", n)
	}
}

func TestAutoReconnectRetriesUntilSuccess(t *testing.T) {
	c := &Client{
		reconnectDelay: 10 * time.Millisecond,
		retryDelay:     20 * time.Millisecond,
	}
	var calls int32
	c.reloadFn = func(context.Context) error {
		if atomic.AddInt32(&calls, 1) < 3 {
			return errors.New("still down")
		}
		// third attempt "reconnects"
		c.setConnected(true)
		return nil
	}
	c.armReconnect("test", 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !c.IsConnected() {
		time.Sleep(10 * time.Millisecond)
	}
	if !c.IsConnected() {
		t.Fatalf("never reconnected, calls=%d", atomic.LoadInt32(&calls))
	}
	// once connected, further arms must not fire attempts
	before := atomic.LoadInt32(&calls)
	c.armReconnect("after success", 10*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	if after := atomic.LoadInt32(&calls); after != before {
		t.Fatalf("armed after success fired %d extra attempts", after-before)
	}
}

func TestAutoReconnectHaltsOnTerminalReloadErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"checkpoint", fmt.Errorf("load messages page: %w", httpclient.RedirectedError{Type: httpclient.ErrCheckpointRequired, URL: "https://www.facebook.com/checkpoint/"})},
		{"suspended", fmt.Errorf("load messages page: %w", httpclient.RedirectedError{Type: httpclient.ErrAccountSuspended, URL: "https://www.facebook.com/accounts/suspended/"})},
		{"token_invalidated", httpclient.ErrTokenInvalidatedRedirect},
		{"ls_1357053_disabled", &types.ErrorResponse{ErrorCode: 1357053, ErrorDescription: "account disabled"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClientShell(nil, "unused.json", types.Facebook, nil)
			c.reconnectDelay = 10 * time.Millisecond
			c.retryDelay = 10 * time.Millisecond
			defer c.cancelPendingReconnect()

			var calls int32
			c.reloadFn = func(context.Context) error {
				atomic.AddInt32(&calls, 1)
				return tc.err
			}

			c.armReconnect("test", 10*time.Millisecond)
			time.Sleep(100 * time.Millisecond)

			if n := atomic.LoadInt32(&calls); n != 1 {
				t.Fatalf("expected exactly 1 attempt before halting on %s, got %d", tc.name, n)
			}
			if !c.IsAuthHalted() {
				t.Fatalf("expected IsAuthHalted=true for %s", tc.name)
			}

			// Subsequent armReconnect calls must be suppressed while halted.
			c.armReconnect("second try", 10*time.Millisecond)
			time.Sleep(50 * time.Millisecond)
			if n := atomic.LoadInt32(&calls); n != 1 {
				t.Fatalf("suppressed reconnect still fired reload calls=%d", n)
			}
		})
	}
}

func TestPermanentError4003HaltsWithoutReload(t *testing.T) {
	c := newClientShell(nil, "unused.json", types.Facebook, nil)
	c.reconnectDelay = 10 * time.Millisecond
	c.retryDelay = 10 * time.Millisecond
	defer c.cancelPendingReconnect()

	var calls int32
	c.reloadFn = func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}

	c.setConnected(true)
	handler := c.makeEventHandler(nil)
	handler(context.Background(), &messagix.PermanentErrorEvent{
		Err: websocket.CloseError{Code: dgw.CloseStatusUnauthorized, Reason: "Unauthorized 401"},
	})

	time.Sleep(80 * time.Millisecond)
	if c.IsConnected() {
		t.Fatal("expected connected=false after PermanentErrorEvent")
	}
	if !c.IsAuthHalted() {
		t.Fatal("expected IsAuthHalted=true after DGW 4003 Unauthorized")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("expected 0 reload calls after DGW 4003, got %d", n)
	}

	// ConnectedEvent clears halt and increments connection epoch.
	epochBefore := c.connectedEpoch()
	handler(context.Background(), &messagix.ConnectedEvent{})
	if c.IsAuthHalted() || !c.IsConnected() || c.connectedEpoch() <= epochBefore {
		t.Fatalf("expected ConnectedEvent to clear halt and advance epoch: halted=%v connected=%v epochBefore=%d after=%d",
			c.IsAuthHalted(), c.IsConnected(), epochBefore, c.connectedEpoch())
	}
}
