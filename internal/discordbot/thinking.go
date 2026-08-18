package discordbot

import (
	"strings"
	"sync"
	"time"

	"element-orion/internal/agent"
	"github.com/bwmarrin/discordgo"
)

// streamSignal is one renderer input: a token delta to append to the live
// preview, or a reset marker (a new model call starting after a tool call).
type streamSignal struct {
	delta string
	reset bool
	tool  string
}

// thinkingAnim is the fork's "thinking." placeholder for the discord
// conversation path (2026-08-18): a message is posted up front, then edited
// with the model's REAL token stream while it works (discord message edits
// have no practical cap), and finally replaced by the complete reply with a
// single edit. messenger keeps its capped dots animation for parity with
// Meta's limits. Falls back to the normal reply path when the final content
// is multi-part (would exceed the 2000-char single-message limit) or an edit
// fails.
type thinkingAnim struct {
	channelID string
	messageID string
	stop      chan struct{}
	once      sync.Once
	signals   chan streamSignal
}

// startThinking posts the placeholder (same reply/reference semantics as
// sendReply) and starts the live token renderer. Returns nil when the
// platform is not wired or the placeholder could not be posted (the run
// proceeds without it). Only user conversations animate — heartbeat/dream/
// background skip it.
func (s *Service) startThinking(prompt inboundPrompt) *thinkingAnim {
	if prompt.Kind == promptKindHeartbeat || prompt.Kind == promptKindDream || prompt.Kind == promptKindBackground {
		return nil
	}

	var reference *discordgo.MessageReference
	if s.shouldReplyToPrompt(prompt) {
		reference = &discordgo.MessageReference{
			MessageID: prompt.MessageID,
			ChannelID: prompt.ChannelID,
			GuildID:   prompt.GuildID,
		}
	}
	msg, err := s.discord.ChannelMessageSendComplex(prompt.ChannelID, &discordgo.MessageSend{
		Content:   "thinking.",
		Reference: reference,
		AllowedMentions: &discordgo.MessageAllowedMentions{
			RepliedUser: false,
		},
	})
	if err != nil {
		return nil
	}

	anim := &thinkingAnim{
		channelID: prompt.ChannelID,
		messageID: msg.ID,
		stop:      make(chan struct{}),
		signals:   make(chan streamSignal, 64),
	}
	go anim.render(s)
	return anim
}

// feed forwards an agent event into the renderer. Non-blocking: a full
// buffer drops deltas, never stalls the run.
func (a *thinkingAnim) feed(ev agent.Event) {
	if a == nil {
		return
	}
	var sig streamSignal
	switch ev.Kind {
	case agent.EventStreamDelta:
		sig.delta = ev.Message
	case agent.EventToolStarted:
		sig.reset = true
		sig.tool = ev.ToolName
	default:
		return
	}
	select {
	case a.signals <- sig:
	default:
	}
}

// render is the real TUI-style animation: it edits the placeholder with the
// actual token stream, throttled to ~1 edit/s (discord's message-edit rate
// limit is ~5/5s) and truncated to stay well under the 2000-char limit. Tool
// calls reset the preview to "using <tool>...".
func (a *thinkingAnim) render(s *Service) {
	const previewMax = 1800
	const throttle = time.Second

	var preview strings.Builder
	var lastEdit time.Time
	justReset := false

	flush := func() bool {
		if preview.Len() == 0 {
			return true
		}
		text := preview.String()
		if len(text) > previewMax {
			text = text[:previewMax] + "..."
		}
		if _, err := s.discord.ChannelMessageEdit(a.channelID, a.messageID, text); err != nil {
			return false
		}
		lastEdit = time.Now()
		return true
	}

	ticker := time.NewTicker(throttle)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			return
		case sig, ok := <-a.signals:
			if !ok {
				return
			}
			if sig.reset {
				preview.Reset()
				justReset = true
				if sig.tool != "" {
					preview.WriteString("using " + sig.tool + "...")
				}
				if !flush() {
					return
				}
				continue
			}
			if justReset {
				preview.Reset()
				justReset = false
			}
			preview.WriteString(sig.delta)
			if time.Since(lastEdit) >= throttle && !flush() {
				return
			}
		case <-ticker.C:
			if !flush() {
				return
			}
		}
	}
}

// finish stops the animation and replaces the placeholder with the final
// content when it fits a single message. Returns true when the content was
// delivered in place (the caller must NOT send the reply again); on
// multi-part content or a failed edit the placeholder is deleted and false
// is returned so the caller falls back to the normal sendReply path.
func (a *thinkingAnim) finish(s *Service, content string) bool {
	if a == nil {
		return false
	}
	a.once.Do(func() { close(a.stop) })

	if !thinkingReplaceable(content) {
		_ = s.discord.ChannelMessageDelete(a.channelID, a.messageID)
		return false
	}
	if _, err := s.discord.ChannelMessageEdit(a.channelID, a.messageID, content); err != nil {
		_ = s.discord.ChannelMessageDelete(a.channelID, a.messageID)
		return false
	}
	return true
}

// thinkingReplaceable reports whether content can replace the placeholder in
// a single in-place edit: it must split into exactly one discord message
// (multi-part replies keep their normal sendReply fan-out).
func thinkingReplaceable(content string) bool {
	return len(splitOutgoingMessages(content)) == 1
}

// discard removes the placeholder without delivering anything (silent turns).
func (a *thinkingAnim) discard(s *Service) {
	if a == nil {
		return
	}
	a.once.Do(func() { close(a.stop) })
	_ = s.discord.ChannelMessageDelete(a.channelID, a.messageID)
}

// sendReplyWithAnim routes content through the thinking placeholder when one
// is active; single-part content is edited in place, anything else falls
// back to the normal multi-message reply path.
func (s *Service) sendReplyWithAnim(prompt inboundPrompt, anim *thinkingAnim, content string) error {
	if anim != nil && anim.finish(s, content) {
		return nil
	}
	return s.sendReply(prompt, content)
}