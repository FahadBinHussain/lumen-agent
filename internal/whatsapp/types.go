package whatsapp

import (
	"context"
	"time"
)

type ParsedMessage struct {
	Chat             ChatJID   `json:"Chat"`
	ID               string    `json:"ID"`
	SenderJID        string    `json:"SenderJID"`
	Timestamp        time.Time `json:"Timestamp"`
	FromMe           bool      `json:"FromMe"`
	Text             string    `json:"Text"`
	PushName         string    `json:"PushName"`
	ReplyToID        string    `json:"ReplyToID"`
	ReplyToSenderJID string    `json:"ReplyToSenderJID"`
	ReplyToDisplay   string    `json:"ReplyToDisplay"`
	IsReplyToUs      bool      `json:"IsReplyToUs"`
	MentionsMe       bool      `json:"MentionsMe"`
	MentionedJIDs    []string  `json:"MentionedJIDs"`
	IsGroup          bool      `json:"IsGroup"`
	ReactionToID     string    `json:"ReactionToID"`
	ReactionEmoji    string    `json:"ReactionEmoji"`
	IsForwarded      bool      `json:"IsForwarded"`
	Edited           bool      `json:"Edited"`
	Revoked          bool      `json:"Revoked"`
	Media            *Media    `json:"Media"`
	Poll             *Poll     `json:"Poll"`
}

type ChatJID struct {
	User   string `json:"user"`
	Server string `json:"server"`
}

func (c ChatJID) String() string {
	if c.Server != "" && c.Server != "s.whatsapp.net" {
		return c.User + "@" + c.Server
	}
	return c.User + "@s.whatsapp.net"
}

type Media struct {
	Type     string `json:"Type"`
	Caption  string `json:"Caption"`
	Filename string `json:"Filename"`
	MimeType string `json:"MimeType"`
}

type Poll struct {
	Question        string   `json:"Question"`
	Options         []string `json:"Options"`
	SelectableCount uint32   `json:"SelectableCount"`
}

type MessageHandler func(ctx context.Context, msg ParsedMessage)