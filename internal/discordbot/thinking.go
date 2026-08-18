package discordbot

import (
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// thinkingAnim is the fork's "thinking." placeholder for the discord
// conversation path (2026-08-18): a message is posted up front, animated
// with in-place edits while the model works, then replaced by the final
// reply with a single edit. Discord message edits have no practical cap, so
// the animation runs until the run finishes instead of stopping after a few
// frames (messenger keeps its capped animation for parity with Meta's
// limits). Falls back to the normal reply path when the final content is
// multi-part (would exceed the 2000-char single-message limit) or an edit
// fails.
type thinkingAnim struct {
	channelID string
	messageID string
	stop      chan struct{}
	once      sync.Once
}

// thinkingFrames drives the placeholder through progressively longer dots.
// The interval stays above Discord's message-edit rate limit (~5/5s).
var thinkingFrames = []string{"thinking", "thinking.", "thinking..", "thinking...", "thinking...."}

// startThinking posts the placeholder (same reply/reference semantics as
// sendReply) and starts the animation. Returns nil when the platform is not
// wired or the placeholder could not be posted (the run proceeds without it).
// Only user conversations animate — heartbeat/dream/background skip it.
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
	}
	go func() {
		i := 0
		for {
			select {
			case <-anim.stop:
				return
			case <-time.After(time.Second):
				i = (i + 1) % len(thinkingFrames)
				if _, err := s.discord.ChannelMessageEdit(anim.channelID, anim.messageID, thinkingFrames[i]); err != nil {
					return
				}
			}
		}
	}()
	return anim
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