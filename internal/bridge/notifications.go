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
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
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
	// (routes carry their own targets). best-effort after validation, same
	// murmur 200 contract so pollers keep working.
	route := strings.TrimSpace(req.Route)
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
		if err := s.messenger.ReloadCookies(r.Context()); err != nil {
			http.Error(w, "Failed to reload cookies: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Cookies uploaded and bridge reloaded"})
}

func (s *Service) handleWhatsAppPair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// public like the QR endpoint: the linking code is transient and can
	// only be used from the account's own phone.
	if s.whatsapp == nil {
		http.Error(w, "whatsapp not enabled", http.StatusBadRequest)
		return
	}
	if s.whatsapp.IsLoggedIn() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "paired", "message": "device already linked"})
		return
	}
	var req struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Phone) == "" {
		req.Phone = strings.TrimSpace(os.Getenv("WHATSAPP_PAIR_PHONE"))
		if req.Phone == "" {
			req.Phone = strings.TrimSpace(os.Getenv("WHATSAPP_PHONE"))
		}
	}
	if req.Phone == "" {
		http.Error(w, "phone is required", http.StatusBadRequest)
		return
	}
	code, err := s.whatsapp.PairPhone(r.Context(), req.Phone)
	if err != nil {
		log.Printf("bridge: whatsapp pair phone failed: %v", err)
		http.Error(w, "pair failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "code", "code": code})
}

func (s *Service) handleWhatsAppQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// deliberately public: the pairing QR is transient (rotates every ~20s
	// and dies with the session), so exposing it is the standard whatsmeow
	// pattern and adds no lasting attack surface.
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
		w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>WhatsApp pairing</title>
<style>body{font-family:system-ui,sans-serif;background:#111;color:#eee;display:flex;flex-direction:column;align-items:center;gap:12px;padding:24px}h1{font-size:20px;margin:0}h2{font-size:16px;margin:24px 0 4px}#qrbox{background:#fff;padding:16px;border-radius:12px}ol{font-size:14px;color:#ccc}button{font-size:15px;padding:10px 20px;border-radius:8px;border:0;background:#2b8a3e;color:#fff;cursor:pointer}button:disabled{opacity:.5}#code{font-size:34px;letter-spacing:6px;font-weight:700;background:#222;padding:14px 24px;border-radius:10px;display:none;font-family:monospace}</style></head>
<body><h1>WhatsApp pairing &mdash; lumen</h1>
<div id="status" style="color:#8be08b">loading...</div>
<div id="qrbox"><img id="qr" width="512" height="512" alt="QR"></div>
<ol><li>Open WhatsApp on your phone</li><li>Settings &rarr; Linked devices &rarr; Link a device</li><li>Scan this QR &mdash; it refreshes automatically every 4 seconds</li></ol>
<h2>or link with phone number</h2>
<button id="pairbtn" onclick="genCode()">Generate linking code</button>
<div id="code"></div>
<ol><li>Press the button above</li><li>Open WhatsApp on the phone &rarr; Settings &rarr; Linked devices &rarr; <b>Link with phone number instead</b></li><li>Type the code shown above into the phone</li></ol>
<script>const img=document.getElementById('qr'),st=document.getElementById('status'),btn=document.getElementById('pairbtn'),code=document.getElementById('code');let have='';
async function tick(){try{const j=await(await fetch('?format=json')).json();if(j.status==='paired'){st.textContent='paired - device linked';btn.style.display='none';return}
if(j.status!=='qr'){st.textContent=(j.status||'waiting')+' - '+(j.message||'');return}
if(j.ref!==have){have=j.ref;img.src='?format=png&t='+Date.now();st.textContent='refreshed '+new Date().toLocaleTimeString()}else{st.textContent='waiting for next refresh...'}}
catch(e){st.textContent='error: '+e.message}}
async function genCode(){btn.disabled=true;code.style.display='none';st.textContent='requesting code...';try{const r=await fetch('/api/whatsapp/pair',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({})});const j=await r.json();if(j.status==='code'){code.textContent=j.code;code.style.display='block';st.textContent='enter this code on the phone'}else{st.textContent=(j.message||'pair failed');btn.disabled=false}}
catch(e){st.textContent='error: '+e.message;btn.disabled=false}}
tick();setInterval(tick,4000)</script></body></html>`))
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