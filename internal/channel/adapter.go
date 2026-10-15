// Package channel defines the platform-independent messaging boundary.
package channel

import "context"

type InboundHandler func(context.Context, InboundMessage) error

// Adapter translates platform events and payloads. Start serves until ctx is
// canceled and must finish platform cleanup before returning. A handler error
// must be returned to the platform so an unaccepted message can be retried.
type Adapter interface {
	ID() string
	Start(ctx context.Context, handler InboundHandler) error
	Send(ctx context.Context, target Route, msg OutboundMessage) error
}
