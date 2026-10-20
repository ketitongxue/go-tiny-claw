package feishu

// Regression fixtures assemble the real Gateway around the real Feishu adapter.
// No conversation, session, queue or approval logic is implemented in the adapter.

import (
	"context"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/access"
	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

type agentRunner = gateway.AgentRunner

type testRuntime struct {
	*Adapter
	gateway  *gateway.Gateway
	platform *testConnectionAdapter
	workDir  string
	cancel   context.CancelFunc
	workers  runtimeWaiter
}

func (b *testRuntime) Close() { b.cancel(); b.gateway.Cancel() }

type runtimeWaiter struct {
	gateway *gateway.Gateway
	t       *testing.T
}

func (w runtimeWaiter) Wait() {
	if err := w.gateway.Shutdown(); err != nil {
		w.t.Error(err)
	}
}

// This replaces only the live websocket with a controllable connection.
// Sending and event parsing still use the actual adapter/SDK.
type testConnectionAdapter struct {
	*Adapter
	connection longConnection
}

func (a *testConnectionAdapter) Start(ctx context.Context, handler channel.InboundHandler) error {
	a.mu.Lock()
	a.ctx, a.handler = ctx, handler
	a.mu.Unlock()
	if a.connection == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return a.serveConnection(ctx, a.connection)
}

func feishuRoute(chat string) channel.Route {
	return channel.Route{Channel: "feishu", AccountID: "default", ChatID: chat}
}
func legacyID(route channel.Route) string {
	if route.Channel == "feishu" && route.AccountID == "default" && route.ThreadID == "" {
		return route.ChatID
	}
	return ""
}

func newTestRuntime(t *testing.T, dir string, resolver gateway.SessionResolver, factory gateway.RuntimeFactory, approvals *approval.ApprovalManager, authorizers ...*access.Authorizer) *testRuntime {
	t.Helper()
	a, err := NewAdapter()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	registry := channel.NewRegistry()
	platform := &testConnectionAdapter{Adapter: a}
	if err := registry.Register(platform); err != nil {
		cancel()
		t.Fatal(err)
	}
	// Legacy adapter regressions explicitly exercise verbose rendering. Compact
	// presentation is covered at the channel-independent Gateway boundary.
	var authorizer *access.Authorizer
	if len(authorizers) != 0 {
		authorizer = authorizers[0]
	}
	g, err := gateway.New(ctx, gateway.Config{Channels: registry, ResolveSession: resolver, Factory: factory,
		Approvals: approvals, Authorizer: authorizer, ReportingPolicy: delivery.ReportingPolicy{Mode: delivery.ReportModeVerbose}})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	a.ctx, a.handler = ctx, g.Receive
	b := &testRuntime{Adapter: a, gateway: g, platform: platform, workDir: dir, cancel: cancel, workers: runtimeWaiter{g, t}}
	t.Cleanup(func() { b.Close(); b.workers.Wait() })
	return b
}

func testBot(t *testing.T, factory func(*ctxpkg.Session, engine.Reporter) agentRunner, managers ...*approval.ApprovalManager) *testRuntime {
	t.Helper()
	t.Setenv("FEISHU_APP_ID", "cli_test")
	t.Setenv("FEISHU_APP_SECRET", "test-secret")
	dir := t.TempDir()
	svc := ctxpkg.NewSessionService(sessionstore.NewMemoryStore())
	sessions := gateway.DurableSessions{Service: svc, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, dir, provider.ModelInfo{}, "")
	}}
	manager := approval.NewApprovalManager()
	if len(managers) != 0 {
		manager = managers[0]
	}
	return newTestRuntime(t, dir, sessions.Resolve, func(_ context.Context, s *ctxpkg.Session, _ channel.Route, r engine.Reporter, _ *approval.Service) (gateway.AgentRunner, error) {
		return factory(s, r), nil
	}, manager)
}

type approvalNotifierFunc func(context.Context, approval.Request) error

func (f approvalNotifierFunc) RequestApproval(ctx context.Context, req approval.Request) error {
	return f(ctx, req)
}
