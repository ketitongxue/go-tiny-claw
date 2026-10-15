// Package telegram implements a text-only Telegram Bot API channel.
package telegram

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type messageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *user  `json:"user,omitempty"`
}

type message struct {
	MessageID      int64           `json:"message_id"`
	From           *user           `json:"from,omitempty"`
	Chat           chat            `json:"chat"`
	Text           string          `json:"text"`
	Entities       []messageEntity `json:"entities,omitempty"`
	ReplyToMessage *message        `json:"reply_to_message,omitempty"`
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message,omitempty"`
}

type webhookInfo struct {
	URL string `json:"url"`
}
