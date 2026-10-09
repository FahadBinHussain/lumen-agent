package messenger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

// The retry schedule must stay short enough to survive a socket drop:
// messagix reconnects within a few seconds of a DGW reset (observed 2026-10-08:
// send failed 18:04:53, socket back at 18:05:02), so 4 attempts over ~9s is
// what catches a reply that would otherwise be silently lost.
func TestRetryLSTableSchedule(t *testing.T) {
	if got := len(sendRetryBackoffs) + 1; got != 4 {
		t.Fatalf("expected 4 total attempts, got %d", got)
	}
	var span time.Duration
	for _, d := range sendRetryBackoffs {
		span += d
	}
	if span < 5*time.Second {
		t.Fatalf("backoff span %v too short to cover a reconnect", span)
	}
}

func TestRetryLSTableSucceedsAfterTransientFailures(t *testing.T) {
	want := &table.LSTable{}
	calls := 0
	// Shrink backoff so the test runs fast; restore immediately after.
	orig := sendRetryBackoffs
	sendRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { sendRetryBackoffs = orig }()

	resp, err := retryLSTable(context.Background(), "send reply", func() (*table.LSTable, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("dgw: oneoffstream: data receive timed out")
		}
		return want, nil
	})
	if err != nil {
		t.Fatalf("expected success on attempt 3, got %v", err)
	}
	if resp != want {
		t.Fatalf("wrong response returned")
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

func TestRetryLSTableGivesUpLoudly(t *testing.T) {
	orig := sendRetryBackoffs
	sendRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { sendRetryBackoffs = orig }()

	calls := 0
	_, err := retryLSTable(context.Background(), "send reply", func() (*table.LSTable, error) {
		calls++
		return nil, errors.New("failed to write msg: use of closed network connection")
	})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "gave up after 4 attempts") {
		t.Fatalf("error must name the attempt count, got %v", err)
	}
	if calls != 4 {
		t.Fatalf("expected 4 attempts, got %d", calls)
	}
}

func TestRetryLSTableStopsOnCanceledContext(t *testing.T) {
	orig := sendRetryBackoffs
	sendRetryBackoffs = []time.Duration{time.Hour}
	defer func() { sendRetryBackoffs = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := retryLSTable(ctx, "send reply", func() (*table.LSTable, error) {
		calls++
		return nil, context.Canceled
	})
	if err == nil {
		t.Fatal("expected the canceled context error to be returned")
	}
	if calls != 1 {
		t.Fatalf("must not retry after cancel, got %d attempts", calls)
	}
}
