package gateway

import (
	"context"
	"errors"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

type SessionResolver func(context.Context, channel.Route) (*ctxpkg.Session, error)

// DurableSessions resolves canonical IDs before consulting an explicitly
// injected legacy mapping. Only ErrNotFound permits fallback or creation;
// corruption, writer contention and cancellation must never fork a session.
type DurableSessions struct {
	Service  *ctxpkg.SessionService
	Create   func(context.Context, string) (*ctxpkg.Session, error)
	LegacyID func(channel.Route) string
}

func (s DurableSessions) Resolve(ctx context.Context, route channel.Route) (*ctxpkg.Session, error) {
	if err := route.Validate(); err != nil {
		return nil, err
	}
	if s.Service == nil || s.Create == nil {
		return nil, errors.New("durable session service required")
	}
	key := channel.BuildSessionKey(route)
	sess, err := s.Service.Resume(ctx, key)
	if !errors.Is(err, sessionstore.ErrNotFound) {
		return sess, err
	}
	if s.LegacyID != nil {
		if legacy := s.LegacyID(route); legacy != "" && legacy != key {
			sess, err = s.Service.Resume(ctx, legacy)
			if !errors.Is(err, sessionstore.ErrNotFound) {
				return sess, err
			}
		}
	}
	return s.Create(ctx, key)
}
