package bridge

import (
	"testing"
	"time"
)

func TestCheckHealthTransitions(t *testing.T) {
	s := &Service{}
	target := healthTarget{notifyPlatform: "messenger", threadID: "2637078310061988"}
	deadAfter := 50 * time.Millisecond
	minNotify := 150 * time.Millisecond

	st := &healthState{}

	// unarmed + dead: nothing, stays unarmed (boot spam guard)
	if msg := s.checkHealth("whatsapp", target, st, false, deadAfter, minNotify); msg != "" {
		t.Fatalf("unarmed dead sent %q", msg)
	}
	if st.armed {
		t.Fatal("should not arm while disconnected")
	}

	// first connect: arms silently
	if msg := s.checkHealth("whatsapp", target, st, true, deadAfter, minNotify); msg != "" {
		t.Fatalf("first connect sent %q", msg)
	}
	if !st.armed {
		t.Fatal("should arm on first connect")
	}

	// brief dead blip below dead_after: silent
	if msg := s.checkHealth("whatsapp", target, st, false, deadAfter, minNotify); msg != "" {
		t.Fatalf("brief blip sent %q", msg)
	}
	if msg := s.checkHealth("whatsapp", target, st, true, deadAfter, minNotify); msg != "" {
		t.Fatalf("recovery after blip sent %q", msg)
	}

	// persistent dead past dead_after: first sample starts the timer, the
	// next one past dead_after notifies exactly once
	if msg := s.checkHealth("whatsapp", target, st, false, deadAfter, minNotify); msg != "" {
		t.Fatalf("first dead sample sent %q", msg)
	}
	time.Sleep(deadAfter + 10*time.Millisecond)
	msg := s.checkHealth("whatsapp", target, st, false, deadAfter, minNotify)
	if msg == "" {
		t.Fatal("expected dead notification")
	}
	if msg2 := s.checkHealth("whatsapp", target, st, false, deadAfter, minNotify); msg2 != "" {
		t.Fatalf("second dead sample re-sent %q", msg2)
	}

	// recovery (cooldown elapsed): alive again notification
	time.Sleep(minNotify + 20*time.Millisecond)
	msg = s.checkHealth("whatsapp", target, st, true, deadAfter, minNotify)
	if msg == "" {
		t.Fatal("expected alive notification")
	}
	if msg2 := s.checkHealth("whatsapp", target, st, true, deadAfter, minNotify); msg2 != "" {
		t.Fatalf("steady alive re-sent %q", msg2)
	}
}

func TestCheckHealthCooldown(t *testing.T) {
	s := &Service{}
	target := healthTarget{notifyPlatform: "whatsapp", threadID: "8801911104251@s.whatsapp.net"}
	deadAfter := time.Millisecond
	minNotify := time.Hour

	st := &healthState{}

	s.checkHealth("messenger", target, st, true, deadAfter, minNotify) // arm
	s.checkHealth("messenger", target, st, false, deadAfter, minNotify) // start dead timer
	time.Sleep(2 * time.Millisecond)

	if msg := s.checkHealth("messenger", target, st, false, deadAfter, minNotify); msg == "" {
		t.Fatal("expected dead notification")
	}
	// flapping back alive within min_notify_interval: silent
	if msg := s.checkHealth("messenger", target, st, true, deadAfter, minNotify); msg != "" {
		t.Fatalf("flap alive within cooldown sent %q", msg)
	}
	// and dead again within cooldown: silent too
	if msg := s.checkHealth("messenger", target, st, false, deadAfter, minNotify); msg != "" {
		t.Fatalf("flap dead within cooldown sent %q", msg)
	}
}
