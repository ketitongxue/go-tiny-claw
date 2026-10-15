package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
)

func TestAdmissionValidationGroupRuleAndControlConsumption(t *testing.T) {
	g, _, _ := testGateway(t, func(context.Context, *ctxpkg.Session, channel.Route, engine.Reporter, *approval.Service) (AgentRunner, error) {
		t.Error("ignored inbound created a runner")
		return nil, nil
	})
	valid := inbound("a", "1", "hello")
	for _, variant := range []string{"route", "id", "type", "unknown"} {
		msg := valid
		switch variant {
		case "route":
			msg.Route.ChatID = ""
		case "id":
			msg.ID = ""
		case "type":
			msg.ChatType = "unexpected"
		case "unknown":
			msg.Route.Channel = "unknown"
		}
		if err := g.Receive(context.Background(), msg); err == nil {
			t.Fatal("invalid inbound acknowledged", variant)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Receive(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled callback admitted", err)
	}
	group := valid
	group.ChatType = channel.ChatGroup
	admit(t, g, group)
	for _, text := range []string{"approve task", "reject task", "approve", "reject too many fields"} {
		msg := inbound("a", text, text)
		admit(t, g, msg)
	}
	if g.ConversationCount() != 0 {
		t.Fatal("group chatter or control command created a session")
	}
}

func TestAcceptedWorkOutlivesCallbackButObservesGatewayCancellation(t *testing.T) {
	started := make(chan context.Context, 1)
	stopped := make(chan struct{}, 1)
	g, _, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, _ engine.Reporter) error {
			started <- ctx
			<-ctx.Done()
			stopped <- struct{}{}
			return ctx.Err()
		}), nil
	})
	callback, cancelCallback := context.WithCancel(context.Background())
	if err := g.Receive(callback, inbound("a", "1", "run")); err != nil {
		t.Fatal(err)
	}
	cancelCallback()
	runCtx := receive(t, started)
	if runCtx.Err() != nil {
		t.Fatal("callback lifetime canceled accepted work")
	}
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	receive(t, stopped)
	if !errors.Is(runCtx.Err(), context.Canceled) {
		t.Fatal("gateway cancellation not propagated")
	}
}

func TestRuntimeFactoryErrorRetriesWithoutReresolvingSession(t *testing.T) {
	calls := 0
	sessions := make(chan *ctxpkg.Session, 2)
	ran := make(chan struct{}, 1)
	g, a, _ := testGateway(t, func(_ context.Context, s *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		calls++
		sessions <- s
		if calls == 1 {
			return nil, errors.New("model test unavailable")
		}
		return runnerFunc(func(context.Context, *ctxpkg.Session, engine.Reporter) error { ran <- struct{}{}; return nil }), nil
	})
	admit(t, g, inbound("a", "1", "first"))
	first := receive(t, sessions)
	out := receive(t, a.sent)
	if out.msg.Kind != channel.OutboundError {
		t.Fatal("factory error not delivered", out)
	}
	admit(t, g, inbound("a", "2", "retry"))
	second := receive(t, sessions)
	receive(t, ran)
	if first != second || len(second.GetWorkingMemory(0)) != 1 || second.GetWorkingMemory(0)[0].Content != "retry" {
		t.Fatal("factory retry lost session/old behavior changed")
	}
}
