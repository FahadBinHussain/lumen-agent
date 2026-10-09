package messenger

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newDispatchTestClient builds a minimal Client whose relay() path works
// (startTime + seen map are what relay's drop guards need).
func newDispatchTestClient(h Handler) *Client {
	return &Client{
		handler:   h,
		startTime: time.Now(),
		seen:      make(map[string]time.Time),
	}
}

func testIncoming(id string) Incoming {
	return Incoming{
		MessageID: id,
		ThreadID:  2637078310061988,
		SenderID:  100009263994215,
		Text:      "/ai hi",
		Timestamp: time.Now().UnixMilli(),
	}
}

// waitOrFail polls cond until it holds or the deadline passes.
func waitOrFail(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The crux: relay must return while the handler is still running. If dispatch
// were inline, relay could never return before release — which is exactly the
// self-deadlock that ate every reply on 2026-10-08/09 (handler occupies
// messagix's frame-handler goroutine, its own send waits for that goroutine).
func TestRelayDispatchesOffTheCallingGoroutine(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	c := newDispatchTestClient(func(ctx context.Context, msg Incoming) {
		close(entered)
		<-release
	})

	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		c.relay(context.Background(), testIncoming("m1"))
	}()

	<-entered // handler started and is now blocked
	select {
	case <-relayDone:
		// async dispatch: relay already returned while the handler runs
	case <-time.After(2 * time.Second):
		t.Fatal("relay blocked on the handler: still dispatched inline")
	}
	close(release)
}

// Messages must be handled one at a time in arrival order (same serial
// semantics the inline path had — session history depends on it).
func TestDispatchPreservesArrivalOrder(t *testing.T) {
	var mu sync.Mutex
	var got []string
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	c := newDispatchTestClient(func(ctx context.Context, msg Incoming) {
		mu.Lock()
		got = append(got, msg.MessageID)
		mu.Unlock()
		if msg.MessageID == "m1" {
			close(firstStarted)
			<-release
		}
	})

	c.relay(context.Background(), testIncoming("m1"))
	<-firstStarted
	c.relay(context.Background(), testIncoming("m2"))
	c.relay(context.Background(), testIncoming("m3"))
	close(release)

	waitOrFail(t, "all three messages dispatched", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 3
	})
	mu.Lock()
	defer mu.Unlock()
	if got[0] != "m1" || got[1] != "m2" || got[2] != "m3" {
		t.Fatalf("arrival order not preserved: got %v", got)
	}
}

// A panicking handler must not kill the dispatcher — a dead dispatcher would
// strand every later message in the queue (silent failure).
func TestDispatcherSurvivesHandlerPanic(t *testing.T) {
	var mu sync.Mutex
	var got []string
	c := newDispatchTestClient(func(ctx context.Context, msg Incoming) {
		mu.Lock()
		got = append(got, msg.MessageID)
		mu.Unlock()
		if msg.MessageID == "m1" {
			panic("boom")
		}
	})

	c.relay(context.Background(), testIncoming("m1"))
	c.relay(context.Background(), testIncoming("m2"))

	waitOrFail(t, "second message dispatched after a handler panic", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	})
	mu.Lock()
	defer mu.Unlock()
	if got[0] != "m1" || got[1] != "m2" {
		t.Fatalf("unexpected dispatch order after panic: %v", got)
	}
}

// The dispatched context is derived with context.WithoutCancel: cancelling the
// messagix event context (socket drop mid-run) must not kill the queued handler.
func TestDispatchedHandlerContextOutlivesEvent(t *testing.T) {
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	secondDone := make(chan struct{})
	var secondErr error
	c := newDispatchTestClient(func(ctx context.Context, msg Incoming) {
		if msg.MessageID == "m1" {
			close(firstStarted)
			<-release
			return
		}
		secondErr = ctx.Err()
		close(secondDone)
	})

	ctx, cancel := context.WithCancel(context.Background())
	c.relay(ctx, testIncoming("m1"))
	<-firstStarted
	c.relay(ctx, testIncoming("m2")) // queued behind the busy handler
	cancel()                          // event ctx dies while m2 is queued
	close(release)

	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second message never dispatched")
	}
	if secondErr != nil {
		t.Fatalf("dispatched handler ctx was canceled with the event ctx: %v", secondErr)
	}
}
