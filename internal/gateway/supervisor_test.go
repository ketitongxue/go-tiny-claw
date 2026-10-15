package gateway

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

type scriptedAdapter struct {
	*fakeAdapter
	start func(context.Context, channel.InboundHandler) error
}

func (a *scriptedAdapter) Start(ctx context.Context, h channel.InboundHandler) error {
	return a.start(ctx, h)
}

func supervisorGateway(t *testing.T, adapters ...channel.Adapter) *Gateway {
	t.Helper()
	registry := channel.NewRegistry()
	for _, a := range adapters {
		if err := registry.Register(a); err != nil {
			t.Fatal(err)
		}
	}
	svc := ctxpkg.NewSessionService(sessionstore.NewMemoryStore())
	dir := t.TempDir()
	sessions := DurableSessions{Service: svc, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, dir, provider.ModelInfo{ProviderID: "fake", ID: "test"}, "none")
	}}
	g, err := New(context.Background(), Config{Channels: registry, ResolveSession: sessions.Resolve,
		Factory: func(context.Context, *ctxpkg.Session, channel.Route, engine.Reporter, *approval.Service) (AgentRunner, error) {
			return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, r engine.Reporter) error {
				r.OnMessage(ctx, "still serving")
				return nil
			}), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	g.retry = adapterRetryPolicy{initial: time.Millisecond, max: 4 * time.Millisecond, stableAfter: time.Second}
	t.Cleanup(func() {
		if err := g.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return g
}

func assertStillServing(t *testing.T, g *Gateway, a *fakeAdapter) {
	t.Helper()
	msg := inbound("chat", "1", "hello")
	msg.Route.Channel = a.ID()
	ack := make(chan error, 1)
	a.inbound <- inboundDelivery{msg, ack}
	if err := receive(t, ack); err != nil {
		t.Fatal(err)
	}
	if out := receiveTaskReport(t, a); out.msg.Text != "still serving" {
		t.Fatal(out)
	}
	select {
	case <-a.stopped:
		t.Fatal("healthy adapter was stopped")
	default:
	}
}

func TestSupervisorRetriesWithoutStoppingHealthyChannel(t *testing.T) {
	for _, failingID := range []string{"telegram", "feishu"} {
		t.Run(failingID, func(t *testing.T) {
			healthyID := "feishu"
			if failingID == healthyID {
				healthyID = "telegram"
			}
			healthy := newFakeAdapter(healthyID)
			attempts := make(chan int32, 4)
			stopped := make(chan struct{})
			var calls atomic.Int32
			flaky := &scriptedAdapter{fakeAdapter: newFakeAdapter(failingID), start: func(ctx context.Context, _ channel.InboundHandler) error {
				n := calls.Add(1)
				attempts <- n
				if n == 1 {
					return errors.New("temporary network error")
				}
				if n == 2 {
					return nil // Unexpected clean exit is retryable too.
				}
				defer close(stopped)
				<-ctx.Done()
				return ctx.Err()
			}}
			g := supervisorGateway(t, healthy, flaky)
			done := make(chan error, 1)
			go func() { done <- g.Run() }()
			receive(t, healthy.started)
			for n := int32(1); n <= 3; n++ {
				if got := receive(t, attempts); got != n {
					t.Fatal("wrong restart sequence", got)
				}
			}
			assertStillServing(t, g, healthy)
			if err := g.Run(); err == nil || !strings.Contains(err.Error(), "already started") {
				t.Fatal("duplicate Run accepted", err)
			}
			g.Cancel()
			if err := receive(t, done); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			receive(t, healthy.stopped)
			receive(t, stopped)
			if calls.Load() != 3 {
				t.Fatal("restarted after cancellation")
			}
		})
	}
}

func TestSupervisorPermanentFailureIsIsolated(t *testing.T) {
	healthy := newFakeAdapter("feishu")
	failed := make(chan struct{})
	bad := &scriptedAdapter{fakeAdapter: newFakeAdapter("telegram"), start: func(context.Context, channel.InboundHandler) error {
		close(failed) // A retry would panic, making it observable.
		return channel.Permanent(errors.New("invalid token"))
	}}
	g := supervisorGateway(t, healthy, bad)
	done := make(chan error, 1)
	go func() { done <- g.Run() }()
	receive(t, healthy.started)
	receive(t, failed)
	assertStillServing(t, g, healthy)
	select {
	case err := <-done:
		t.Fatal("one permanent failure stopped the gateway", err)
	default:
	}
	g.Cancel()
	if err := receive(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSupervisorAllPermanentFailuresAreAggregated(t *testing.T) {
	first, second := errors.New("bad account"), errors.New("bad token")
	a := &scriptedAdapter{fakeAdapter: newFakeAdapter("feishu"), start: func(context.Context, channel.InboundHandler) error {
		return channel.Permanent(first)
	}}
	b := &scriptedAdapter{fakeAdapter: newFakeAdapter("telegram"), start: func(context.Context, channel.InboundHandler) error {
		return channel.Permanent(second)
	}}
	g := supervisorGateway(t, a, b)
	done := make(chan error, 1)
	go func() { done <- g.Run() }()
	err := receive(t, done)
	if !errors.Is(err, first) || !errors.Is(err, second) || !strings.Contains(err.Error(), "feishu") || !strings.Contains(err.Error(), "telegram") {
		t.Fatal("missing aggregated failure", err)
	}
}

func TestCancelInterruptsLongAdapterBackoff(t *testing.T) {
	var calls atomic.Int32
	a := &scriptedAdapter{fakeAdapter: newFakeAdapter("telegram"), start: func(context.Context, channel.InboundHandler) error {
		calls.Add(1)
		return errors.New("network unavailable")
	}}
	g := supervisorGateway(t, a)
	backingOff := make(chan struct{})
	g.retry = defaultAdapterRetryPolicy()
	g.retry.initial = 30 * time.Second
	g.retry.jitter = func(d time.Duration) time.Duration { close(backingOff); return d }
	done := make(chan error, 1)
	go func() { done <- g.Run() }()
	receive(t, backingOff)
	g.Cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
			t.Fatal(err, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("cancel waited for 30-second backoff")
	}
}

func TestAdapterBackoffSequenceAndStableReset(t *testing.T) {
	p := defaultAdapterRetryPolicy()
	var previous time.Duration
	for _, seconds := range []int{1, 2, 4, 8, 16, 30, 30, 30} {
		previous = p.next(previous, 0)
		if previous != time.Duration(seconds)*time.Second {
			t.Fatal(previous, seconds)
		}
		jittered := min(p.jitter(previous), p.max)
		if jittered < previous*9/10 || jittered > previous*11/10 || jittered > p.max {
			t.Fatal("jitter outside bounds", jittered)
		}
	}
	if next := p.next(previous, time.Minute); next != time.Second {
		t.Fatal("stable connection did not reset backoff", next)
	}
}
