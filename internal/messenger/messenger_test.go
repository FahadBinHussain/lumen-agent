package messenger

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

func TestSplitMessageUnicodeSafeAndLabeled(t *testing.T) {
	text := strings.Repeat("রোহিঙ্গাদের প্রত্যাবর্তন নিশ্চিতে আন্তর্জাতিক সম্প্রদায়ের সহযোগিতা চাইলেন প্রধানমন্ত্রী\n", 80)
	parts := splitMessage(text, maxMsgLen)
	if len(parts) < 2 {
		t.Fatal("expected a long message to split")
	}
	for i, part := range parts {
		if len(part) > maxMsgLen {
			t.Fatalf("part %d exceeds Messenger limit: %d bytes", i+1, len(part))
		}
		if !strings.HasPrefix(part, "[part ") {
			t.Fatalf("part %d is missing continuation label: %q", i+1, part[:min(len(part), 30)])
		}
		if !utf8.ValidString(part) {
			t.Fatalf("part %d is not valid UTF-8", i+1)
		}
	}
}


func TestReactionHandler(t *testing.T) {
	cl := &Client{}
	var received ReactionEvent
	cl.SetReactionHandler(func(ctx context.Context, react ReactionEvent) {
		received = react
	})

	if cl.reactionHandler == nil {
		t.Fatal("expected reactionHandler to be set")
	}

	cl.reactionHandler(context.Background(), ReactionEvent{
		ThreadID:  12345,
		MessageID: "mid-1",
		ActorID:   67890,
		Reaction:  "👍",
		Timestamp: 1000,
	})

	if received.ThreadID != 12345 || received.MessageID != "mid-1" || received.ActorID != 67890 || received.Reaction != "👍" {
		t.Fatalf("unexpected reaction event: %+v", received)
	}
}

func TestParsePlatform(t *testing.T) {
	cases := []struct {
		in      string
		want    types.Platform
		wantErr bool
	}{
		{"", types.Messenger, false},
		{"messenger", types.Messenger, false},
		{"  FACEBOOK  ", types.Facebook, false},
		{"instagram", types.Unset, true},
		{"unknown", types.Unset, true},
	}
	for _, tc := range cases {
		got, err := ParsePlatform(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParsePlatform(%q) expected error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParsePlatform(%q) unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParsePlatform(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNewWithPlatformAndUnauthenticated(t *testing.T) {
	cookiesPath := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(cookiesPath, []byte(`{"c_user":"1","xs":"x","datr":"d"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	fbClient, err := NewWithPlatform(cookiesPath, types.Facebook, nil)
	if err != nil {
		t.Fatalf("NewWithPlatform(Facebook) failed: %v", err)
	}
	if fbClient.Platform() != types.Facebook {
		t.Fatalf("Platform() = %v, want Facebook", fbClient.Platform())
	}
	fbClient.Disconnect()

	msgClient, err := NewWithPlatform(cookiesPath, types.Messenger, nil)
	if err != nil {
		t.Fatalf("NewWithPlatform(Messenger) failed: %v", err)
	}
	if msgClient.Platform() != types.Messenger {
		t.Fatalf("Platform() = %v, want Messenger", msgClient.Platform())
	}
	msgClient.Disconnect()

	if _, err := NewWithPlatform(cookiesPath, types.Unset, nil); err == nil {
		t.Fatal("expected error for invalid platform")
	}

	// Execute when client has cookies loaded cleanly at struct level but has not logged in yet
	// must fail immediately without burning retry backoffs.
	start := time.Now()
	_, err = fbClient.executeRetry(context.Background(), "send reply", &socket.SendMessageTask{ThreadId: 123, Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("expected fast not-logged-in error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("executeRetry took too long when unauthenticated: %v", elapsed)
	}

	unauth, err := NewUnauthenticatedWithPlatform(filepath.Join(t.TempDir(), "missing.json"), types.Facebook, nil)
	if err != nil {
		t.Fatalf("NewUnauthenticatedWithPlatform failed: %v", err)
	}
	if unauth.Platform() != types.Facebook || unauth.UID() != 0 || unauth.IsConnected() {
		t.Fatalf("unexpected unauthenticated client state: platform=%v uid=%d connected=%v", unauth.Platform(), unauth.UID(), unauth.IsConnected())
	}
	if _, _, err := unauth.Login(context.Background()); err == nil || !strings.Contains(err.Error(), "cookies not loaded") {
		t.Fatalf("expected cookies not loaded error from Login, got %v", err)
	}
	if err := unauth.Start(context.Background()); err == nil {
		t.Fatal("expected error from Start without client")
	}
	if err := unauth.SendImage(context.Background(), 123, []byte{1}, "image/png"); err == nil {
		t.Fatal("expected error from SendImage without client")
	}
	unauth.Disconnect()
}

func TestNewWithPlatformAndProxy(t *testing.T) {
	cookiesPath := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(cookiesPath, []byte(`{"c_user":"1","xs":"x","datr":"d"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	fbClient, err := NewWithPlatformAndProxy(cookiesPath, types.Facebook, "socks5://127.0.0.1:1055", nil)
	if err != nil {
		t.Fatalf("NewWithPlatformAndProxy failed: %v", err)
	}
	if fbClient.Proxy() != "socks5://127.0.0.1:1055" {
		t.Fatalf("Proxy() = %q, want socks5://127.0.0.1:1055", fbClient.Proxy())
	}
	fbClient.Disconnect()

	if _, err := NewWithPlatformAndProxy(cookiesPath, types.Facebook, "ftp://127.0.0.1:21", nil); err == nil {
		t.Fatal("expected error for unsupported proxy scheme")
	}

	unauth, err := NewUnauthenticatedWithPlatformAndProxy(filepath.Join(t.TempDir(), "missing.json"), types.Facebook, "socks5://127.0.0.1:1055", nil)
	if err != nil {
		t.Fatalf("NewUnauthenticatedWithPlatformAndProxy failed: %v", err)
	}
	if unauth.Proxy() != "socks5://127.0.0.1:1055" {
		t.Fatalf("unauth.Proxy() = %q, want socks5://127.0.0.1:1055", unauth.Proxy())
	}
	unauth.Disconnect()
}
