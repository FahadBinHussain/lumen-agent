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
	if s.messenger != nil {
		mux.HandleFunc("/api/cookies/upload", s.handleCookieUpload)
	}
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
		if strings.HasPrefix(strings.ToLower(provided), "bearer ") {
			provided = strings.TrimSpace(provided[7:])
		}
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 1
}