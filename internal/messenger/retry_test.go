package messenger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

// The schedule must ride out a forced-reconnect cycle: messagix normally
// comes back in 1-4s (ForceReconnect → connect loop), and readiness waits
// burn sendReadyTimeout when it doesn't. The 2026-10-09 01:51Z incident had
// the socket dead for 34s while blind 9s retries burned out 1s before the
// socket returned — the forced nudge + readiness wait is what closes that.
func TestRetryLSTableSchedule(t *testing.T) {
	if got := len(sendRetryBackoffs) + 1; got != 5 {
		t.Fatalf("expected 5 total attempts, got %d", got)
	}
	if sendReadyTimeout < 10*time.Second {
		t.Fatalf("readiness wait %v too short to cover a reconnect", sendReadyTimeout)
	}
	var span time.Duration
	for _, d := range sendRetryBackoffs {
		span += d
	}
	if span < 20*time.Second {
		t.Fatalf("backoff span %v too short as a fallback", span)
	}
}

// shrinkTimers makes every pacing wait in retryLSTable ~instant for tests.
func shrinkTimers(t *testing.T) {
	t.Helper()
	origBackoffs, origGrace, origReady := sendRetryBackoffs, sendReconnectGrace, sendReadyTimeout
	sendRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	sendReconnectGrace = time.Millisecond
	sendReadyTimeout = time.Millisecond
	t.Cleanup(func() {
		sendRetryBackoffs, sendReconnectGrace, sendReadyTimeout = origBackoffs, origGrace, origReady
	})
}

func TestRetryLSTableNudgesAndWaitsUntilSuccess(t *testing.T) {
	shrinkTimers(t)
	want := &table.LSTable{}
	calls, nudges, readyWaits := 0, 0, 0

	resp, err := retryLSTable(context.Background(), "send reply",
		func() (*table.LSTable, error) {
			calls++
			if calls < 3 {
				return nil, errors.New("dgw: oneoffstream: data receive timed out")
			}
			return want, nil
		},
		func(error) { nudges++ },
		func() error { readyWaits++; return nil })
	if err != nil {
		t.Fatalf("expected success on attempt 3, got %v", err)
	}
	if resp != want {
		t.Fatalf("wrong response returned")
	}
	if calls != 3 || nudges != 2 || readyWaits != 2 {
		t.Fatalf("calls=%d nudges=%d readyWaits=%d, want 3/2/2", calls, nudges, readyWaits)
	}
}

func TestRetryLSTableBacksOffWhenSocketStaysUnready(t *testing.T) {
	shrinkTimers(t)
	calls, nudges := 0, 0
	notReady := errors.New("timeout waiting for canSendMessages")

	_, err := retryLSTable(context.Background(), "send reply",
		func() (*table.LSTable, error) {
			calls++
			return nil, errors.New("failed to write msg: use of closed network connection")
		},
		func(error) { nudges++ },
		func() error { return notReady })
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "gave up after 5 attempts") {
		t.Fatalf("error must name the attempt count, got %v", err)
	}
	if calls != 5 || nudges != 4 {
		t.Fatalf("calls=%d nudges=%d, want 5/4 — every failure except the last must force a reconnect", calls, nudges)
	}
}

func TestRetryLSTableStopsOnCanceledContext(t *testing.T) {
	shrinkTimers(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls, nudges := 0, 0
	_, err := retryLSTable(ctx, "send reply",
		func() (*table.LSTable, error) {
			calls++
			return nil, context.Canceled
		},
		func(error) { nudges++ },
		func() error { return nil })
	if err == nil {
		t.Fatal("expected the canceled context error to be returned")
	}
	if calls != 1 || nudges != 0 {
		t.Fatalf("calls=%d nudges=%d, want 1/0 — no reconnect kick after cancel", calls, nudges)
	}
}
