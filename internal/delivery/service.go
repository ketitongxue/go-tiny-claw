// Package delivery routes engine output through registered channel adapters.
package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

type Service struct{ channels *channel.Registry }

func NewService(channels *channel.Registry) *Service { return &Service{channels: channels} }

func (s *Service) Send(ctx context.Context, route channel.Route, msg channel.OutboundMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := route.Validate(); err != nil {
		return err
	}
	if s == nil || s.channels == nil {
		return errors.New("delivery channels not configured")
	}
	adapter, ok := s.channels.Get(route.Channel)
	if !ok {
		return fmt.Errorf("channel %q is not registered", route.Channel)
	}
	return adapter.Send(ctx, route, msg)
}
