package bridge

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// healthTarget is where a platform's health notifications go: another
// platform's test channel.
type healthTarget struct {
	notifyPlatform string // "messenger", "whatsapp", or "discord"
	threadID       string // messenger thread id, whatsapp jid, or discord channel id
}

type healthState struct {
	armed        bool
	deadSince    time.Time
	lastDead     bool
	alivePending bool // recovery happened inside min_notify; alive msg still owed
	lastNotify   time.Time
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
					if len(msg) >= 13 && msg[len(msg)-13:] == "is alive again" {
						go s.drainPending(context.Background())
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
			return ""
		}
		// whatsapp logged out is fatal (needs QR) — arm immediately so the
		// logged-out alert can fire even on a fresh boot that has never seen
		// a live connection in this run. transient dead still needs one alive
		// first (boot spam guard).
		if name == "whatsapp" && s.whatsapp != nil && !s.whatsapp.IsLoggedIn() {
			st.armed = true
			log.Printf("health-watch: %s armed (logged out)", name)
		} else {
			return ""
		}
	}

	if alive {
		st.deadSince = time.Time{}
		if st.lastDead || st.alivePending {
			if time.Since(st.lastNotify) >= minNotify {
				st.lastDead = false
				st.alivePending = false
				st.lastNotify = time.Now()
				return name + " is alive again"
			}
			// recovery inside the cooldown: stay armed on the alert so the
			// alive notification fires once the cooldown elapses instead of
			// being swallowed forever (the old bug: a quick recovery reset
			// lastDead and the alive message was silently lost).
			st.alivePending = true
			return ""
		}
		return ""
	}

	if st.deadSince.IsZero() {
		st.deadSince = time.Now()
	}
	if !st.lastDead && time.Since(st.deadSince) >= deadAfter && time.Since(st.lastNotify) >= minNotify {
		st.lastDead = true
		st.lastNotify = time.Now()
		if name == "whatsapp" && s.whatsapp != nil && !s.whatsapp.IsLoggedIn() {
			return name + " is logged out (needs phone re-pair via QR - not auto-recovering)"
		}
		if name == "whatsapp" {
			if diag := s.diagnoseWhatsAppDead(); diag != "" {
				log.Printf("health-watch: whatsapp dead diagnosis: %s", diag)
				return name + " is dead (" + diag + ")"
			}
		}
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

// diagnoseWhatsAppDead probes tailnet vs whatsapp to classify a whatsapp dead.
func (s *Service) diagnoseWhatsAppDead() string {
	proxyAddr := os.Getenv("WHATSAPP_PROXY_URL")
	if proxyAddr == "" {
		proxyAddr = os.Getenv("WHATSAPP_PROXY")
	}
	if proxyAddr == "" {
		return ""
	}
	u, err := url.Parse(proxyAddr)
	var proxyHost string
	if err == nil && u.Host != "" {
		proxyHost = u.Host
	} else {
		proxyHost = strings.TrimPrefix(proxyAddr, "socks5://")
		proxyHost = strings.TrimPrefix(proxyHost, "socks://")
		proxyHost = strings.TrimPrefix(proxyHost, "socks5h://")
	}
	if proxyHost == "" {
		proxyHost = "127.0.0.1:1055"
	}
	// 1. socks TCP liveness — tailscaled crash
	conn, err := net.DialTimeout("tcp", proxyHost, 2*time.Second)
	if err != nil {
		return "tailscale socks down — " + proxyHost + " not listening (tailscaled crashed, container restart needed)"
	}
	conn.Close()

	// 2. tailscale status --json — exit node reachability
	st, err := tailscaleStatus(4 * time.Second)
	if err != nil {
		log.Printf("health-watch: tailscale status failed: %v", err)
	} else {
		if st.BackendState != "" && st.BackendState != "Running" {
			return "tailscale not running — BackendState=" + st.BackendState + " (restart needed)"
		}
		if len(st.Health) > 0 {
			for _, h := range st.Health {
				lh := strings.ToLower(h)
				if strings.Contains(lh, "exit") || strings.Contains(lh, "offline") || strings.Contains(lh, "not running") {
					return "tailscale health: " + h
				}
			}
		}
		if st.ExitNodeStatus == nil {
			return "no exit node — container not routing via tail exit (check --exit-node=100.76.10.50 / TS_AUTHKEY)"
		}
		if !st.ExitNodeStatus.Online {
			host, ip := findExitNodePeer(st)
			detail := ""
			if host != "" {
				detail = host
			}
			if ip != "" {
				if detail != "" {
					detail += " " + ip
				} else {
					detail = ip
				}
			}
			if detail == "" {
				detail = "100.76.10.50 laptop-main"
			}
			return "exit node offline — " + detail + " not reachable (laptop VPN/sleep? Tailscale on that machine may be blocked; disable VPN or allow split-tunnel)"
		}
		if peer := findPeerByIP(st, "100.76.10.50"); peer != nil && !peer.Online {
			return "exit node host offline — laptop-main 100.76.10.50 peer offline (VPN may have disconnected Tailscale on that machine)"
		}
	}

	// 3. egress probe via SOCKS — VPN filtering / ISP block
	if err := probeViaSocks(proxyHost, "1.1.1.1:443", 4*time.Second); err != nil {
		return "tail exit probe failed — proxy up but egress blocked (VPN on laptop-main filtering tail traffic? try split-tunnel or disconnect VPN)"
	}
	if err := probeViaSocks(proxyHost, "web.whatsapp.com:443", 5*time.Second); err != nil {
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "i/o timeout") || strings.Contains(err.Error(), "deadline") {
			return "whatsapp egress timeout via tail exit — exit node may be throttled/offline, will retry"
		}
		return "whatsapp host unreachable via tail exit — " + err.Error()
	}
	return "tailscale ok — whatsapp websocket down (TLS/JA3 block or WhatsApp rate limit; auto-retrying)"
}

type tsStatus struct {
	BackendState   string             `json:"BackendState"`
	Health         []string           `json:"Health"`
	Self           *tsPeer            `json:"Self"`
	Peer           map[string]*tsPeer `json:"Peer"`
	ExitNodeStatus *tsExit            `json:"ExitNodeStatus"`
}

type tsPeer struct {
	HostName       string   `json:"HostName"`
	DNSName        string   `json:"DNSName"`
	TailscaleIPs   []string `json:"TailscaleIPs"`
	Online         bool     `json:"Online"`
	ExitNode       bool     `json:"ExitNode"`
	ExitNodeOption bool     `json:"ExitNodeOption"`
}

type tsExit struct {
	ID           string   `json:"ID"`
	Online       bool     `json:"Online"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

func tailscaleStatus(timeout time.Duration) (*tsStatus, error) {
	candidates := []string{"/app/tailscale", "tailscale"}
	var lastErr error
	for _, bin := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, bin, "status", "--json")
		out, err := cmd.Output()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		var st tsStatus
		if err := json.Unmarshal(out, &st); err != nil {
			lastErr = err
			continue
		}
		return &st, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, context.DeadlineExceeded
}

func findExitNodePeer(st *tsStatus) (host, ip string) {
	if st.Peer == nil {
		return "", ""
	}
	// prefer peer with ExitNode==true
	for _, p := range st.Peer {
		if p.ExitNode {
			host = p.HostName
			if len(p.TailscaleIPs) > 0 {
				ip = p.TailscaleIPs[0]
			}
			return host, ip
		}
	}
	// fallback: match ExitNodeStatus IPs
	if st.ExitNodeStatus != nil && len(st.ExitNodeStatus.TailscaleIPs) > 0 {
		target := st.ExitNodeStatus.TailscaleIPs[0]
		for _, p := range st.Peer {
			for _, pip := range p.TailscaleIPs {
				if pip == target {
					return p.HostName, pip
				}
			}
		}
		return "", target
	}
	return "", ""
}

func findPeerByIP(st *tsStatus, ip string) *tsPeer {
	if st.Peer == nil {
		return nil
	}
	for _, p := range st.Peer {
		for _, pip := range p.TailscaleIPs {
			if pip == ip {
				return p
			}
		}
	}
	return nil
}

func probeViaSocks(proxyHost, target string, timeout time.Duration) error {
	dialer := &net.Dialer{Timeout: timeout}
	auth := (*proxy.Auth)(nil)
	// proxyHost may contain user:pass but our proxy is open; strip it
	if u, err := url.Parse("socks5://" + proxyHost); err == nil && u.Host != "" {
		proxyHost = u.Host
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pass}
		}
	}
	socks, err := proxy.SOCKS5("tcp", proxyHost, auth, dialer)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := socks.(proxy.ContextDialer).DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
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
