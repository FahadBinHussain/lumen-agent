package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"element-orion/internal/cookies"
	"element-orion/internal/vision"
)

type notificationRequest struct {
	Source    string `json:"source"`
	ThreadID  string `json:"threadId"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	DedupeKey string `json:"dedupeKey"`
	URL       string `json:"url"`
	Platform  string `json:"platform"`
	Route     string `json:"route"`
}

// visionHealth reports the vision engine's state for /api/health: the runner's
// live describer snapshot when wired, and a loud misconfigured status when
// vision.enabled is set but no runner carries a describer (never a quiet
// "disabled" that hides the wiring gap).
func (s *Service) visionHealth() vision.Health {
	if s.runner != nil {
		h := s.runner.VisionHealth()
		if !h.Enabled && s.cfg.Vision.Enabled {
			h.Enabled = true
			h.Status = vision.StatusMisconfigured
			h.LastError = "vision.enabled is set but the agent runner has no describer wired"
		}
		return h
	}
	if s.cfg.Vision.Enabled {
		return vision.Health{
			Enabled:   true,
			Status:    vision.StatusMisconfigured,
			LastError: "vision.enabled is set but the bridge has no agent runner",
		}
	}
	return vision.Health{Enabled: false, Status: vision.StatusDisabled}
}

func (s *Service) serveHTTP(ctx context.Context) error {
	mux := http.NewServeMux()

	if s.cfg.Bridge.NotificationsEnabled {
		mux.HandleFunc(s.cfg.Bridge.NotificationsPath, s.handleAutomationNotification)
		mux.HandleFunc("/api/automation/notifications/pending", s.handlePendingList)
		mux.HandleFunc("/api/test/prompt", s.handleTestPrompt)
	}
	// always mount the cookie upload handler while the bridge is up: it is
	// ops-critical (browserless refresher + first-boot provisioning) and
	// writes the cookies file even when the messenger client isn't running
	// yet (reload is a no-op then). secret-gated when bridge.secret is set.
	mux.HandleFunc("/api/cookies/upload", s.handleCookieUpload)
	mux.HandleFunc("/api/whatsapp/qr", s.handleWhatsAppQR)
	mux.HandleFunc("/api/whatsapp/pair", s.handleWhatsAppPair)
	mux.HandleFunc("/api/whatsapp/session/upload", s.handleWhatsAppSessionUpload)
	mux.HandleFunc("/api/whatsapp/groups", s.handleWhatsAppGroups)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		// platform states ride along so external watchers (cookie-health
		// watchdog, uptime checks) can tell "web process alive" apart from
		// "mouth connected" — a bare 200 used to read healthy while
		// messenger was down for 17h (2026-10-04). always 200; monitors
		// that keyword-match still find "ok" in the body.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"platforms": map[string]bool{
				"messenger": s.isPlatformConnected("messenger"),
				"whatsapp":  s.isPlatformConnected("whatsapp"),
				"discord":   s.isPlatformConnected("discord"),
			},
			"vision": s.visionHealth(),
		})
	})

	server := &http.Server{
		Addr:              s.cfg.Bridge.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("bridge http listen: %w", err)
			return
		}
		errCh <- nil
	}()

	if s.cfg.Bridge.NotificationsEnabled {
		log.Printf("bridge: notifications server on %s%s", s.cfg.Bridge.ListenAddr, s.cfg.Bridge.NotificationsPath)
	} else {
		log.Printf("bridge: notifications endpoint disabled (bridge.notifications_enabled=false)")
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("shutdown bridge http: %w", err)
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

// scopedDedupeKey turns a per-item dedupe key into a per-DESTINATION key so
// one feed item fan-out (same key, several threads) can be delivered to every
// thread instead of only the first one. Format: "<key>|route:<name>" or
// "<key>|<platform>:<threadID>" (platform defaults to messenger, matching the
// handler). Empty key stays empty (the dedupe path stays off).
func scopedDedupeKey(key, route, platform, threadID string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if route != "" {
		return key + "|route:" + route
	}
	platform = strings.TrimSpace(strings.ToLower(platform))
	if platform == "" {
		platform = "messenger"
	}
	return key + "|" + platform + ":" + strings.TrimSpace(threadID)
}

func (s *Service) handleAutomationNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req notificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	text := strings.TrimSpace(req.Message)
	if text == "" {
		text = strings.TrimSpace(req.Title)
	} else if title := strings.TrimSpace(req.Title); title != "" {
		text = title + "\n\n" + text
	}
	if text == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	// route mode: fan out to every channel of a configured route instead of
	// a single platform/threadId. checked before the threadId requirement
	// (routes carry their own targets).
	route := strings.TrimSpace(req.Route)

	// Scope the dedupe key per DESTINATION before anything reads it. A poller
	// loops its thread_ids and POSTs the SAME key once per thread (free-games
	// = "30738305889116993,953525124128433"), so an unscoped key let the first
	// thread's delivery dedupe every later thread out of BOTH gates: the
	// notification_deliveries check here and the unique-indexed pending queue
	// (ON CONFLICT (dedupe_key) DO NOTHING). Result: the second free-games
	// group got nothing since the 2026-09-23 deliveries dedupe landed
	// (fixed 2026-10-08). Keeps same-thread retries deduped as before.
	req.DedupeKey = scopedDedupeKey(req.DedupeKey, route, req.Platform, req.ThreadID)

	// Pollers provide a dedupe key. Queue those notifications before trying to
	// send them: the Neon row is the durable source of truth, and drainPending
	// removes it only after the platform send succeeds. This gives warnings
	// at-least-once delivery without allowing duplicate queue rows.
	if strings.TrimSpace(req.DedupeKey) != "" {
		if s.neon == nil {
			http.Error(w, "deduped notification cannot be guaranteed: pending database unavailable", http.StatusServiceUnavailable)
			return
		}
		delivered, err := s.neon.IsDelivered(r.Context(), req.DedupeKey)
		if err != nil {
			http.Error(w, "could not check deduped notification: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if delivered {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "sent", "deduped": true})
			return
		}
		if route != "" {
			if _, ok := s.cfg.Bridge.Routes[route]; !ok {
				http.Error(w, "unknown route: "+route, http.StatusBadRequest)
				return
			}
		} else if strings.TrimSpace(req.ThreadID) == "" {
			http.Error(w, "threadId is required", http.StatusBadRequest)
			return
		}
		platform := strings.TrimSpace(strings.ToLower(req.Platform))
		if platform == "" {
			platform = "messenger"
		}
		if _, err := s.queuePending(r.Context(), req, platform, text); err != nil {
			http.Error(w, "could not persist deduped notification: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		// Try immediately when the platform is already connected; the row
		// remains available for the periodic retry if delivery fails.
		s.drainPending(r.Context())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     fmt.Sprintf("ntf_%d", time.Now().UnixMilli()),
			"status": "pending",
		})
		return
	}

	// Non-deduped/manual notifications retain the existing immediate behavior.
	if route != "" {
		if _, ok := s.cfg.Bridge.Routes[route]; !ok {
			http.Error(w, "unknown route: "+route, http.StatusBadRequest)
			return
		}
		if _, err := s.SendRoute(r.Context(), route, text); err != nil {
			log.Printf("bridge: route %s notification failed: %v", route, err)
			if s.savePending(r.Context(), req, text) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{"id": fmt.Sprintf("ntf_%d", time.Now().UnixMilli()), "status": "pending"})
				return
			}
		}
		log.Printf("bridge: automation notification (source=%s route=%s)", req.Source, route)
		notifID := fmt.Sprintf("ntf_%d", time.Now().UnixMilli())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     notifID,
			"status": "sent",
		})
		return
	}

	platform := strings.TrimSpace(strings.ToLower(req.Platform))
	if platform == "" {
		platform = "messenger"
	}
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		http.Error(w, "threadId is required", http.StatusBadRequest)
		return
	}

	// If the mouth is dead, queue instead of dropping (so we don't miss
	// notifications while Render sleeps or MQTT is down). The queue is in
	// Neon (pending_notifications), so it survives free-tier restarts.
	if !s.isPlatformConnected(platform) {
		if s.savePending(r.Context(), req, text) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"id": fmt.Sprintf("ntf_%d", time.Now().UnixMilli()), "status": "pending"})
			return
		}
	}

	log.Printf("bridge: automation notification (source=%s platform=%s thread=%s)", req.Source, platform, threadID)
	s.notify(platform, threadID, text)

	notifID := fmt.Sprintf("ntf_%d", time.Now().UnixMilli())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":     notifID,
		"status": "sent",
	})
}

func (s *Service) handlePendingList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.neon == nil {
		http.Error(w, "no db", http.StatusServiceUnavailable)
		return
	}
	pending, err := s.neon.ListPending(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"pending": pending, "count": len(pending)})
}

func (s *Service) handleCookieUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var cookieMap cookies.CookieMap
	if err := json.NewDecoder(r.Body).Decode(&cookieMap); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	data, err := json.MarshalIndent(cookieMap, "", "  ")
	if err != nil {
		http.Error(w, "Failed to marshal cookies: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(s.cfg.Messenger.CookiesPath, data, 0o644); err != nil {
		http.Error(w, "Failed to write cookies file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if s.messenger != nil {
		// Detached context: ReloadCookies -> Start launches the background
		// MQTT Connect under this ctx. With r.Context() the handshake was
		// ALWAYS context-canceled the moment this handler returned (the
		// cancel is swallowed as "context canceled", so no error ever
		// surfaced) — every cookie refresh silently left messenger
		// disconnected until the next deploy/reboot. Found 2026-09-07.
		if err := s.messenger.ReloadCookies(context.WithoutCancel(r.Context())); err != nil {
			http.Error(w, "Failed to reload cookies: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Cookies uploaded and bridge reloaded"})
}

func (s *Service) handleWhatsAppPair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWhatsAppPairError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !whatsappPairAuthorized(r) {
		writeWhatsAppPairError(w, "whatsapp pairing is not authorized", http.StatusUnauthorized)
		return
	}
	if s.whatsapp == nil {
		writeWhatsAppPairError(w, "whatsapp not enabled", http.StatusBadRequest)
		return
	}
	if s.whatsapp.IsLoggedIn() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "paired", "message": "device already linked"})
		return
	}
	var req struct{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeWhatsAppPairError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	phone := strings.TrimSpace(os.Getenv("WHATSAPP_PAIR_PHONE"))
	if phone == "" {
		phone = strings.TrimSpace(os.Getenv("WHATSAPP_PHONE"))
	}
	if phone == "" {
		writeWhatsAppPairError(w, "saved WhatsApp pairing phone is not configured", http.StatusBadRequest)
		return
	}
	code, err := s.whatsapp.PairPhone(r.Context(), phone)
	if err != nil {
		log.Printf("bridge: whatsapp pair phone failed: %v", err)
		writeWhatsAppPairError(w, "pair failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "code", "code": code})
}

func writeWhatsAppPairError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": message})
}

func whatsappPairAuthorized(r *http.Request) bool {
	want := strings.TrimSpace(os.Getenv("WHATSAPP_PAIR_TOKEN"))
	got := strings.TrimSpace(r.URL.Query().Get("token"))
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// whatsappPairPage is the private pairing page. States it must handle
// truthfully: qr live / waiting (whatsapp not connected right now) / paired /
// disabled / unauthorized. The QR box starts hidden and only shows once the
// PNG actually loads, so an unloaded <img> can never render as the
// broken-image icon (the "QR not coming" symptom when the tail-exit flaps).
const whatsappPairPage = `<!doctype html><html><head><meta charset="utf-8"><title>WhatsApp pairing</title>
<style>body{font-family:system-ui,sans-serif;background:#111;color:#eee;display:flex;flex-direction:column;align-items:center;gap:12px;padding:24px}h1{font-size:20px;margin:0}h2{font-size:16px;margin:24px 0 4px}#qrbox{background:#fff;padding:16px;border-radius:12px;display:none}ol{font-size:14px;color:#ccc}button{font-size:15px;padding:10px 20px;border-radius:8px;border:0;background:#2b8a3e;color:#fff;cursor:pointer}button:disabled{opacity:.5}#code{font-size:34px;letter-spacing:6px;font-weight:700;background:#222;padding:14px 24px;border-radius:10px;display:none;font-family:monospace}#status{color:#8be08b;min-height:1.2em}#status.warn{color:#ffd43b}#status.err{color:#ff6b6b}#ph{display:none;width:512px;min-height:512px;align-items:center;justify-content:center;text-align:center;color:#444;font-size:15px;background:#f2f2f2;border-radius:8px;padding:24px;box-sizing:border-box}</style></head>
<body><h1>WhatsApp pairing &mdash; lumen</h1>
<div id="status">checking...</div>
<div id="qrbox"><img id="qr" width="512" height="512" alt="QR"><div id="ph"></div></div>
<ol><li>Open WhatsApp on your phone</li><li>Settings &rarr; Linked devices &rarr; Link a device</li><li>Scan this QR &mdash; it refreshes automatically every 4 seconds</li></ol>
<h2>or link with phone number</h2>
<div style="font-size:13px;color:#aaa">This private page uses the saved server-side phone number.</div>
<button id="pairbtn" onclick="genCode()">Generate linking code</button>
<div id="code"></div>
<ol><li>Press the button above</li><li>Open WhatsApp on the phone &rarr; Settings &rarr; Linked devices &rarr; <b>Link with phone number instead</b></li><li>Type the code shown above into the phone</li></ol>
<script>const img=document.getElementById('qr'),st=document.getElementById('status'),btn=document.getElementById('pairbtn'),code=document.getElementById('code'),box=document.getElementById('qrbox'),ph=document.getElementById('ph'),params=new URLSearchParams(location.search),token=params.get('token')||'';let have='';
function say(c,t){st.className=c;st.textContent=t}
function placeholder(t){img.style.display='none';ph.style.display='flex';ph.textContent=t;box.style.display='block'}
async function tick(){try{const j=await(await fetch('?format=json&token='+encodeURIComponent(token))).json();
if(j.status==='paired'){have='';btn.style.display='none';placeholder('device already linked - nothing to scan');say('','paired - device linked');return}
if(j.status==='error'||j.status==='disabled'){have='';box.style.display='none';say('err',j.message||j.status);return}
if(j.status!=='qr'){have='';placeholder('waiting for a fresh QR - this page checks every 4 seconds and shows it as soon as whatsapp connects');say('warn',j.message||'waiting');return}
if(j.ref!==have){have=j.ref;img.onload=()=>{img.style.display='block';ph.style.display='none';box.style.display='block'};img.onerror=()=>{have='';placeholder('QR image failed to load - waiting for the next one')};img.src='?format=png&t='+Date.now()}
say('','QR live - refreshes automatically '+new Date().toLocaleTimeString())}
catch(e){say('err','error: '+e.message)}}
async function genCode(){btn.disabled=true;code.style.display='none';say('','requesting code using saved phone number...');try{const r=await fetch('/api/whatsapp/pair?token='+encodeURIComponent(token),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({})});const raw=await r.text();let j;try{j=JSON.parse(raw)}catch(_){throw new Error(raw||('HTTP '+r.status))}if(!r.ok){throw new Error(j.message||('HTTP '+r.status))}if(j.status==='code'){code.textContent=j.code;code.style.display='block';say('','enter this code on the phone')}else{say('err',j.message||'pair failed');btn.disabled=false}}
catch(e){say('err','error: '+e.message);btn.disabled=false}}
tick();setInterval(tick,4000)</script></body></html>`

func (s *Service) handleWhatsAppQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !whatsappPairAuthorized(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "whatsapp pairing is not authorized"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if s.whatsapp == nil {
		json.NewEncoder(w).Encode(map[string]string{"status": "disabled", "message": "whatsapp not enabled"})
		return
	}
	if s.whatsapp.IsLoggedIn() {
		json.NewEncoder(w).Encode(map[string]string{"status": "paired", "message": "device already linked"})
		return
	}
	qr := s.whatsapp.QRCode()
	if qr == "" {
		json.NewEncoder(w).Encode(map[string]string{"status": "waiting", "message": "no QR yet - check back in a few seconds"})
		return
	}
	if r.URL.Query().Get("format") == "json" {
		json.NewEncoder(w).Encode(map[string]string{"status": "qr", "ref": s.whatsapp.QRRef()})
		return
	}
	if r.URL.Query().Get("format") == "png" {
		png, err := qrcode.Encode(s.whatsapp.QRRef(), qrcode.Medium, 512)
		if err != nil {
			http.Error(w, "qr encode failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(png)
		return
	}
	if r.URL.Query().Get("format") == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(whatsappPairPage))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(qr))
}

func (s *Service) authenticated(r *http.Request) bool {
	secret, err := s.cfg.ResolveBridgeNotificationsSecret()
	if err != nil {
		log.Printf("bridge: auth check failed: %v", err)
		return false
	}
	if strings.TrimSpace(secret) == "" {
		return true
	}
	provided := strings.TrimSpace(r.Header.Get("X-HF-Authorization"))
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("Authorization"))
	}
	if strings.HasPrefix(strings.ToLower(provided), "bearer ") {
		provided = strings.TrimSpace(provided[7:])
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 1
}

// handleWhatsAppGroups lists the groups the whatsapp device has joined.
// Secret-gated like the session upload endpoint.
func (s *Service) handleWhatsAppGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.whatsapp == nil {
		http.Error(w, "whatsapp not enabled", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	groups, err := s.whatsapp.Groups(ctx)
	if err != nil {
		log.Printf("bridge: list whatsapp groups failed: %v", err)
		http.Error(w, "list groups failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]interface{}, 0, len(groups))
	for _, g := range groups {
		if g == nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":   g.JID.String(),
			"name": g.GroupName,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"groups": out})
}

// handleWhatsAppSessionUpload receives a raw whatsmeow.db from an external
// machine (e.g. a laptop that paired locally) and stores it in Neon so the
// next boot restores it. Secret-gated like the other bridge endpoints.
func (s *Service) handleWhatsAppSessionUpload(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if s.whatsapp == nil || s.neon == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil || len(data) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.neon.SaveWhatsAppSession(ctx, data, nil); err != nil {
		log.Printf("bridge: whatsapp session upload save failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	log.Printf("bridge: whatsapp session uploaded (%d bytes)", len(data))
	w.WriteHeader(http.StatusOK)
}
