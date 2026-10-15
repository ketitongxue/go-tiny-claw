package channel

import (
	"errors"
	"net/url"
	"strings"
)

// Route addresses one conversation, not a cross-channel user identity.
type Route struct {
	Channel   string
	AccountID string
	ChatID    string
	ThreadID  string
}

func (r Route) Validate() error {
	if strings.TrimSpace(r.Channel) == "" || strings.TrimSpace(r.AccountID) == "" || strings.TrimSpace(r.ChatID) == "" {
		return errors.New("route requires channel, account and chat")
	}
	return nil
}

// BuildSessionKey is the sole conversation-key encoding. QueryEscape encodes
// delimiters, percent signs, slashes, backslashes and arbitrary UTF-8 bytes;
// fixed labels and an optional nonempty thread make the encoding unambiguous.
// Callers validate the Route before resolving a session.
func BuildSessionKey(r Route) string {
	key := "channel:" + url.QueryEscape(r.Channel) + ":account:" + url.QueryEscape(r.AccountID) + ":chat:" + url.QueryEscape(r.ChatID)
	if r.ThreadID != "" {
		key += ":thread:" + url.QueryEscape(r.ThreadID)
	}
	return key
}
