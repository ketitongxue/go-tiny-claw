package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

// Immutable after Run starts. Tests shorten timings without changing the
// production retry policy or introducing process-wide clocks/sleep hooks.
type adapterRetryPolicy struct {
	initial, max, stableAfter time.Duration
	jitter                    func(time.Duration) time.Duration
}

func defaultAdapterRetryPolicy() adapterRetryPolicy {
	return adapterRetryPolicy{initial: time.Second, max: 30 * time.Second, stableAfter: time.Minute,
		jitter: func(d time.Duration) time.Duration {
			return time.Duration(float64(d) * (0.9 + rand.Float64()*0.2))
		}}
}

func (p adapterRetryPolicy) next(previous, uptime time.Duration) time.Duration {
	if previous == 0 || uptime >= p.stableAfter {
		return p.initial
	}
	return min(previous*2, p.max)
}

func (g *Gateway) supervise(a channel.Adapter) error {
	var backoff time.Duration
	for {
		if err := g.ctx.Err(); err != nil {
			return err
		}
		log.Printf("[Gateway] channel %s starting", a.ID())
		started := time.Now()
		err := a.Start(g.ctx, g.Receive)
		if g.ctx.Err() != nil {
			return g.ctx.Err()
		}
		if channel.IsPermanent(err) {
			log.Printf("[Gateway] channel %s failed permanently: %v", a.ID(), err)
			return fmt.Errorf("channel %s failed permanently: %w", a.ID(), err)
		}
		if err == nil {
			err = errors.New("adapter stopped unexpectedly")
		}
		backoff = g.retry.next(backoff, time.Since(started))
		delay := backoff
		if g.retry.jitter != nil {
			delay = min(g.retry.jitter(delay), g.retry.max)
		}
		// Optional platform flood-control hint; never retry earlier than allowed.
		var limited interface{ RetryAfter() time.Duration }
		if errors.As(err, &limited) {
			delay = max(delay, limited.RetryAfter())
		}
		log.Printf("[Gateway] channel %s stopped: %v; restarting in %s", a.ID(), err, delay)
		if err := waitForRetry(g.ctx, delay); err != nil {
			return err
		}
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
