package messenger

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
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
