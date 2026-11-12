package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"element-orion/internal/whatsapp"
)

func TestWhatsAppStreamBroadcastDeliversToSubscribers(t *testing.T) {
	s := &Service{}
	ch1, unsub1 := s.subscribeWhatsAppStream()
	defer unsub1()
	ch2, unsub2 := s.subscribeWhatsAppStream()
	defer unsub2()

	msg := whatsapp.ParsedMessage{ID: "msg-1", Text: "hello stream", SenderJID: "123@s.whatsapp.net"}
	s.broadcastWhatsAppMessage(msg)

	for i, ch := range []<-chan whatsapp.ParsedMessage{ch1, ch2} {
		select {
		case got := <-ch:
			if got.ID != "msg-1" || got.Text != "hello stream" {
				t.Fatalf("subscriber %d got %+v, want msg-1/hello stream", i, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d got nothing", i)
		}
	}

	// unsubscribed channels are closed and stop receiving.
	unsub2()
	s.broadcastWhatsAppMessage(whatsapp.ParsedMessage{ID: "msg-2"})
	select {
	case _, ok := <-ch2:
		if ok {
			t.Fatal("unsubscribed channel received a message")
		}
	case <-time.After(time.Second):
		t.Fatal("unsubscribed channel was not closed")
	}
	select {
	case got := <-ch1:
		if got.ID != "msg-2" {
			t.Fatalf("remaining subscriber got %q, want msg-2", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("remaining subscriber got nothing after the other unsubscribed")
	}
}

func TestWhatsAppStreamBroadcastNeverBlocksOnSlowSubscriber(t *testing.T) {
	s := &Service{}
	_, unsubSlow := s.subscribeWhatsAppStream()
	defer unsubSlow()
	fast, unsubFast := s.subscribeWhatsAppStream()

	// drain the fast subscriber concurrently so it never fills up; the
	// slow one is never read, so its buffer fills and must be dropped
	// around, never blocked on. The drainer signals ready before the
	// first broadcast so its count is meaningful.
	const total = whatsappStreamBuffer + 5
	ready := make(chan struct{})
	counted := make(chan int, 1)
	go func() {
		close(ready)
		n := 0
		for range fast {
			n++
		}
		counted <- n
	}()
	<-ready

	done := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			s.broadcastWhatsAppMessage(whatsapp.ParsedMessage{ID: "burst"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast blocked on a slow subscriber")
	}
	unsubFast()
	select {
	case n := <-counted:
		// the drained subscriber gets (nearly) everything; the only
		// hard guarantee is it never stalls the broadcast, so the
		// floor is a full buffer's worth.
		if n < whatsappStreamBuffer {
			t.Fatalf("fast subscriber got %d messages, want at least %d", n, whatsappStreamBuffer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fast subscriber drain did not finish")
	}
}

func TestWhatsAppSendRequiresAuthAndValidatesInput(t *testing.T) {
	cfg := writeTestConfig(t, "  secret: hunter2\n  secret_env: \"\"\n")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	defer s.Close()

	body, _ := json.Marshal(map[string]string{"jid": "123@s.whatsapp.net", "text": "hi"})
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/api/whatsapp/send", bytes.NewReader(body))
	rec := newRecorder()
	s.handleWhatsAppSend(rec, req)
	if rec.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without secret, got %d", rec.status)
	}

	// authed, but whatsapp is disabled in the test config: a loud 400,
	// not a silent success.
	req, _ = http.NewRequest(http.MethodPost, "http://127.0.0.1/api/whatsapp/send", bytes.NewReader(body))
	req.Header.Set("X-HF-Authorization", "hunter2")
	rec = newRecorder()
	s.handleWhatsAppSend(rec, req)
	if rec.status != http.StatusBadRequest {
		t.Fatalf("expected 400 with whatsapp disabled, got %d: %s", rec.status, rec.body.String())
	}
}

func TestWhatsAppThreadsRequiresAuth(t *testing.T) {
	cfg := writeTestConfig(t, "  secret: hunter2\n  secret_env: \"\"\n")
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	defer s.Close()

	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/api/whatsapp/threads", nil)
	rec := newRecorder()
	s.handleWhatsAppThreads(rec, req)
	if rec.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 without secret, got %d", rec.status)
	}

	req, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/api/whatsapp/threads", nil)
	req.Header.Set("X-HF-Authorization", "hunter2")
	rec = newRecorder()
	s.handleWhatsAppThreads(rec, req)
	if rec.status != http.StatusBadRequest {
		t.Fatalf("expected 400 with whatsapp disabled, got %d: %s", rec.status, rec.body.String())
	}
}
