package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"element-orion/internal/whatsapp"
)

// whatsappStreamBuffer is the per-subscriber queue for the live message
// stream. A subscriber that falls this far behind drops messages instead of
// stalling the whatsapp handler — the stream is a live feed, not a queue.
const whatsappStreamBuffer = 32

// whatsappStreamHeartbeat keeps idle SSE connections (and the proxies in
// front of them) from timing out: a comment line every 25s.
const whatsappStreamHeartbeat = 25 * time.Second

// subscribeWhatsAppStream registers one live-stream subscriber and returns
// its channel plus an unsubscribe function. The channel is closed on
// unsubscribe.
func (s *Service) subscribeWhatsAppStream() (<-chan whatsapp.ParsedMessage, func()) {
	ch := make(chan whatsapp.ParsedMessage, whatsappStreamBuffer)
	s.waStreamMu.Lock()
	if s.waStreamSubs == nil {
		s.waStreamSubs = make(map[chan whatsapp.ParsedMessage]struct{})
	}
	s.waStreamSubs[ch] = struct{}{}
	s.waStreamMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.waStreamMu.Lock()
			delete(s.waStreamSubs, ch)
			s.waStreamMu.Unlock()
			close(ch)
		})
	}
}

// broadcastWhatsAppMessage fans one parsed message out to every live-stream
// subscriber. Sends never block: a full subscriber buffer drops the message
// for that subscriber only, so a dead or slow SSE client can never stall
// whatsapp handling or the agent reply path.
func (s *Service) broadcastWhatsAppMessage(msg whatsapp.ParsedMessage) {
	s.waStreamMu.Lock()
	defer s.waStreamMu.Unlock()
	for ch := range s.waStreamSubs {
		select {
		case ch <- msg:
		default:
			// subscriber is behind; drop for them, keep going.
		}
	}
}

// handleWhatsAppSend delivers text to one whatsapp chat through the
// allowlist gate (SendWhatsApp). Secret-gated like /api/whatsapp/groups.
// Body: {"jid": "<chat JID>", "text": "<message>"}. Returns
// {"status": "ok", "messageId": "<whatsapp message id>"}.
func (s *Service) handleWhatsAppSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	var req struct {
		JID  string `json:"jid"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.JID = strings.TrimSpace(req.JID)
	if req.JID == "" {
		http.Error(w, "jid is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	id, err := s.SendWhatsApp(ctx, req.JID, req.Text)
	if err != nil {
		log.Printf("bridge: whatsapp api send to %s failed: %v", req.JID, err)
		if strings.Contains(err.Error(), "not in whatsapp.allowed_jids") {
			http.Error(w, "send failed: "+err.Error(), http.StatusForbidden)
			return
		}
		http.Error(w, "send failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "messageId": id})
}

// handleWhatsAppThreads lists the chats the whatsapp device can reach:
// joined groups plus 1:1 chats with recent activity. Secret-gated like
// /api/whatsapp/groups.
func (s *Service) handleWhatsAppThreads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	threads, err := s.whatsapp.Threads(ctx)
	if err != nil {
		log.Printf("bridge: list whatsapp threads failed: %v", err)
		http.Error(w, "list threads failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]interface{}, 0, len(threads))
	for _, t := range threads {
		out = append(out, map[string]interface{}{
			"id":           t.JID,
			"name":         t.Name,
			"lastActivity": t.LastActivity,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"threads": out})
}

// handleWhatsAppStream is the live message feed: Server-Sent Events, one
// `event: message` per incoming whatsapp message (ParsedMessage, JSON),
// plus a `: heartbeat` comment every 25s so clients and proxies know the
// stream is still alive. Secret-gated like /api/whatsapp/groups; the
// secret goes in the Authorization (or X-HF-Authorization) header, since
// EventSource cannot set headers — curl and server-side clients can.
func (s *Service) handleWhatsAppStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ch, unsubscribe := s.subscribeWhatsAppStream()
	defer unsubscribe()

	heartbeat := time.NewTicker(whatsappStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				log.Printf("bridge: whatsapp stream encode failed: %v", err)
				continue
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}
