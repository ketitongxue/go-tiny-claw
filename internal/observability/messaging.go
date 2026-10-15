package observability

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// MessagingAttributes contains diagnostic dimensions, never chat/user addresses.
// Observability is platform-neutral; adapters' raw events do not enter traces.
type MessagingAttributes struct {
	Channel   string
	AccountID string
	ChatType  string
}

type messagingKey struct{}

func WithMessagingAttributes(ctx context.Context, attrs MessagingAttributes) context.Context {
	return context.WithValue(ctx, messagingKey{}, attrs)
}

func MessagingFromContext(ctx context.Context) (MessagingAttributes, bool) {
	attrs, ok := ctx.Value(messagingKey{}).(MessagingAttributes)
	return attrs, ok
}

// SessionReference is a deterministic pseudonym for messaging trace/log IDs.
// Durable session files still contain sensitive original IDs and conversation facts.
func SessionReference(id string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(id))) }
