package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
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
}

func (s *Service) serveHTTP(ctx context.Context) error {
	mux := http.NewServeMux()

	if s.cfg.Bridge.NotificationsEnabled {
		mux.HandleFunc(s.cfg.Bridge.NotificationsPath, s.handleAutomationNotification)
	}
	// always mount the cookie upload handler while the bridge is up: it is
	// ops-critical (browserless refresher + first-boot provisioning) and
	// writes the cookies file even when the messenger client isn't running
	// yet (reload is a no-op then). secret-gated when bridge.secret is set.
	mux.HandleFunc("/api/cookies/upload", s.handleCookieUpload)
	mux.HandleFunc("/api/whatsapp/qr", s.handleWhatsAppQR)
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

	platform := strings.TrimSpace(strings.ToLower(req.Platform))
	if platform == "" {
		platform = "messenger"
	}
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		http.Error(w, "threadId is required", http.StatusBadRequest)
		return
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
		w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>WhatsApp pairing QR</title>
<style>body{font-family:system-ui,sans-serif;background:#111;color:#eee;display:flex;flex-direction:column;align-items:center;gap:12px;padding:24px}h1{font-size:20px;margin:0}#qrbox{background:#fff;padding:16px;border-radius:12px}ol{font-size:14px;color:#ccc}</style></head>
<body><h1>WhatsApp pairing QR &mdash; lumen</h1><div id="status" style="color:#8be08b">loading...</div>
<div id="qrbox"><img id="qr" width="512" height="512" alt="QR"></div>
<ol><li>Open WhatsApp on <b>+8801911104251</b></li><li>Settings &rarr; Linked devices &rarr; Link a device</li><li>Scan this QR &mdash; it refreshes automatically every 4 seconds</li></ol>
<script>const img=document.getElementById('qr'),st=document.getElementById('status');let have='';
async function tick(){try{const j=await(await fetch('?format=json')).json();if(j.status==='paired'){st.textContent='paired - device linked';return}
if(j.status!=='qr'){st.textContent=(j.status||'waiting')+' - '+(j.message||'');return}
if(j.ref!==have){have=j.ref;img.src='?format=png&t='+Date.now();st.textContent='refreshed '+new Date().toLocaleTimeString()}else{st.textContent='waiting for next refresh...'}}
catch(e){st.textContent='error: '+e.message}}
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