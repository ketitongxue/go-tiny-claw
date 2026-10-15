// Package channel defines the platform-independent messaging boundary.
package channel

import "context"

type InboundHandler func(context.Context, InboundMessage) error

// Adapter translates platform events and payloads. Start serves until ctx is
// canceled and must finish platform cleanup before returning. Start calls are
// sequential and restartable; Send may run concurrently with Start/restarts.
// A handler error must leave the event unacknowledged for a later retry.
// Permanent errors stop only this adapter; other exits are supervised/retried.
type Adapter interface {
	ID() string
	Start(ctx context.Context, handler InboundHandler) error
	Send(ctx context.Context, target Route, msg OutboundMessage) error
}
