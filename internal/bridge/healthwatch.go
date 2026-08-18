package bridge

import (
	"context"
	"log"
	"time"
)

// healthTarget is where a platform's health notifications go: another
// platform's test channel.
type healthTarget struct {
	notifyPlatform string // "messenger", "whatsapp", or "discord"
	threadID       string // messenger thread id, whatsapp jid, or discord channel id
}

type healthState struct {
	armed      bool
	deadSince  time.Time
	lastDead   bool
	lastNotify time.Time
}

// watchHealth sends cross-platform notifications when a platform dies or
// comes back: whatsapp state changes go to the messenger test thread (and the
// discord channel when configured), messenger state changes go to the
// whatsapp test jid (and the discord channel), discord state changes go to
// both. It runs inside the container so it keeps working when the laptop (and
// the whatsapp tailnet route through it) is down — messenger egresses from
// Render directly, so the whatsapp-dead alert always has a working channel.
//
// No notification fires until the platform has been connected at least once
// (avoids boot/deploy spam), a dead state must persist for dead_after before
// it counts (brief reconnect blips, including the intentional messenger
// cookie reload, stay silent), and min_notify_interval caps burst spam when
// the path flaps.
func (s *Service) watchHealth(ctx context.Context) {
	hw := s.cfg.Bridge.HealthWatch
	interval := parseDuration(hw.Interval, 15*time.Second)
	deadAfter := parseDuration(hw.DeadAfter, 45*time.Second)
	minNotify := parseDuration(hw.MinNotifyInterval, 2*time.Minute)

	targets := map[string][]healthTarget{}
	if hw.MessengerThreadID != "" && s.messenger != nil {
		targets["whatsapp"] = append(targets["whatsapp"], healthTarget{notifyPlatform: "messenger", threadID: hw.MessengerThreadID})
	}
	if hw.WhatsAppJID != "" && s.whatsapp != nil {
		targets["messenger"] = append(targets["messenger"], healthTarget{notifyPlatform: "whatsapp", threadID: hw.WhatsAppJID})
	}
	if s.discord != nil {
		if hw.MessengerThreadID != "" && s.messenger != nil {
			targets["discord"] = append(targets["discord"], healthTarget{notifyPlatform: "messenger", threadID: hw.MessengerThreadID})
		}
		if hw.WhatsAppJID != "" && s.whatsapp != nil {
			targets["discord"] = append(targets["discord"], healthTarget{notifyPlatform: "whatsapp", threadID: hw.WhatsAppJID})
		}
		if hw.DiscordChannelID != "" {
			targets["whatsapp"] = append(targets["whatsapp"], healthTarget{notifyPlatform: "discord", threadID: hw.DiscordChannelID})
			targets["messenger"] = append(targets["messenger"], healthTarget{notifyPlatform: "discord", threadID: hw.DiscordChannelID})
		}
	}
	if len(targets) == 0 {
		log.Printf("health-watch: no targets (set bridge.health_watch.messenger_thread_id / whatsapp_jid / discord_channel_id)")
		return
	}

	states := map[string]*healthState{}
	for name := range targets {
		states[name] = &healthState{}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("health-watch: enabled (interval %s, dead_after %s, min_notify %s)", interval, deadAfter, minNotify)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for name, platformTargets := range targets {
				if msg := s.checkHealth(name, states[name], s.platformAlive(name), deadAfter, minNotify); msg != "" {
					for _, target := range platformTargets {
						s.notifyHealth(target, msg)
					}
				}
			}
		}
	}
}

// checkHealth advances the state machine for one platform and returns the
// notification message to send, or "" when nothing should fire.
func (s *Service) checkHealth(name string, st *healthState, alive bool, deadAfter time.Duration, minNotify time.Duration) string {
	if !st.armed {
		if alive {
			st.armed = true
			log.Printf("health-watch: %s armed (first connect)", name)
		}
		return ""
	}

	if alive {
		st.deadSince = time.Time{}
		if st.lastDead && time.Since(st.lastNotify) >= minNotify {
			st.lastDead = false
			st.lastNotify = time.Now()
			return name + " is alive again"
		}
		st.lastDead = false
		return ""
	}

	if st.deadSince.IsZero() {
		st.deadSince = time.Now()
	}
	if !st.lastDead && time.Since(st.deadSince) >= deadAfter && time.Since(st.lastNotify) >= minNotify {
		st.lastDead = true
		st.lastNotify = time.Now()
		return name + " is dead (auto-retrying, will come back on its own)"
	}
	return ""
}

func (s *Service) platformAlive(name string) bool {
	switch name {
	case "whatsapp":
		return s.whatsapp != nil && s.whatsapp.IsConnected()
	case "messenger":
		return s.messenger != nil && s.messenger.IsConnected()
	case "discord":
		return s.discord != nil && s.discord.IsConnected()
	}
	return false
}

func (s *Service) notifyHealth(target healthTarget, text string) {
	// send() is allowlist-gated (messenger.allowed_thread_ids /
	// whatsapp.allowed_jids) — alerts to non-listed channels get dropped with
	// a log line, same as the notifications endpoint.
	s.notify(target.notifyPlatform, target.threadID, text)
	log.Printf("health-watch: notified %s %s: %s", target.notifyPlatform, target.threadID, text)
}

func parseDuration(v string, def time.Duration) time.Duration {
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
