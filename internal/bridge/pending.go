package bridge

import (
	"context"
	"log"
	"strconv"
	"time"

	"element-orion/internal/neon"
)

// isPlatformConnected reports whether the mouth for a platform is currently
// up. It mirrors healthwatch's IsConnected checks so we only queue transient
// "not connected" failures, not permanent config errors like "not in
// allowed_thread_ids" (which never recover without a deploy).
func (s *Service) isPlatformConnected(platform string) bool {
	switch platform {
	case "messenger":
		return s.messenger != nil && s.messenger.IsConnected()
	case "whatsapp":
		return s.whatsapp != nil && s.whatsapp.IsConnected()
	case "discord":
		return s.discord != nil && s.discord.IsConnected()
	default:
		return s.messenger != nil && s.messenger.IsConnected()
	}
}

// savePending persists a notification that failed because the mouth was dead.
// It dedupes on dedupe_key so the same neon warning doesn't enqueue twice
// (the poller's dedupe already does this, but the queue adds a second guard
// for manual re-POSTs). Returns true if actually queued.
func (s *Service) savePending(ctx context.Context, req notificationRequest, text string) bool {
	if s.neon == nil {
		log.Printf("bridge: pending: no db, dropping notification source=%s thread=%s", req.Source, req.ThreadID)
		return false
	}
	// Only queue transient "mouth dead" failures — permanent errors like
	// "not in allowed_thread_ids" should not be retried.
	platform := req.Platform
	if platform == "" {
		platform = "messenger"
	}
	if s.isPlatformConnected(platform) {
		return false
	}
	p := neon.PendingNotification{
		Platform:  platform,
		ThreadID:  req.ThreadID,
		Route:     req.Route,
		Title:     req.Title,
		Message:   text,
		DedupeKey: req.DedupeKey,
		Source:    req.Source,
		URL:       req.URL,
	}
	if _, err := s.neon.SavePending(ctx, p); err != nil {
		log.Printf("bridge: pending save failed: %v", err)
		return false
	}
	log.Printf("bridge: pending queued (source=%s platform=%s thread=%s dedupe=%s)", req.Source, platform, req.ThreadID, req.DedupeKey)
	return true
}

// drainPending retries every queued notification whose platform is now
// connected. It is called from health-watch on dead→alive and from a
// periodic ticker as a safety net. Best-effort: a still-dead mouth leaves
// the row for the next drain; a successful send deletes it.
func (s *Service) drainPending(ctx context.Context) {
	if s.neon == nil {
		return
	}
	pending, err := s.neon.ListPending(ctx)
	if err != nil {
		log.Printf("bridge: pending list failed: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	log.Printf("bridge: draining %d pending notifications", len(pending))
	for _, p := range pending {
		// Re-check mouth is still up for this row's platform.
		if !s.isPlatformConnected(p.Platform) {
			continue
		}
		var sendErr error
		text := p.Message
		if text == "" {
			text = p.Title
		} else if p.Title != "" {
			text = p.Title + "\n\n" + p.Message
		}
		if p.Route != "" {
			_, sendErr = s.SendRoute(ctx, p.Route, text)
		} else {
			switch p.Platform {
			case "whatsapp":
				_, sendErr = s.SendWhatsApp(ctx, p.ThreadID, text)
			case "discord":
				_, sendErr = s.sendDiscord(p.ThreadID, text)
			default:
				tid, perr := strconv.ParseInt(p.ThreadID, 10, 64)
				if perr != nil {
					log.Printf("bridge: pending %d bad thread id %q, dropping", p.ID, p.ThreadID)
					_ = s.neon.DeletePending(ctx, p.ID)
					continue
				}
				_, sendErr = s.SendMessage(ctx, tid, text)
			}
		}
		if sendErr != nil {
			_ = s.neon.IncPendingAttempts(ctx, p.ID)
			log.Printf("bridge: pending %d still failing: %v", p.ID, sendErr)
			continue
		}
		if err := s.neon.DeletePending(ctx, p.ID); err != nil {
			log.Printf("bridge: pending delete %d failed: %v", p.ID, err)
		} else {
			log.Printf("bridge: pending %d delivered and removed", p.ID)
		}
		// Gentle pacing so we don't burst the rate limit on a large queue.
		time.Sleep(400 * time.Millisecond)
	}
}
