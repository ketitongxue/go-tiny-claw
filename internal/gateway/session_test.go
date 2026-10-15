package gateway

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func durableResolver(svc *ctxpkg.SessionService, dir, model string) DurableSessions {
	return DurableSessions{Service: svc, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, dir, provider.ModelInfo{ProviderID: "fake", ID: model}, "none")
	}, LegacyID: func(r channel.Route) string {
		// Compatibility is opt-in at assembly, restricted to the old default route.
		if r.Channel == "test" && r.AccountID == "default" && r.ThreadID == "" {
			return r.ChatID
		}
		return ""
	}}
}

func TestLegacyResumeApprovalAndRestartKeepActualIDModelHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	store, err := sessionstore.NewFileStore(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	oldSvc := ctxpkg.NewSessionService(store)
	old, err := oldSvc.Create(ctx, "old-chat", dir, provider.ModelInfo{ProviderID: "fake", ID: "old-model"}, "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Append(ctx, schema.Message{Role: schema.RoleUser, Content: "ABC123"}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		// Use a fresh store/service, as a process restart would.
		store, err := sessionstore.NewFileStore(sessionDir)
		if err != nil {
			t.Fatal(err)
		}
		resolver := durableResolver(ctxpkg.NewSessionService(store), dir, "new-default")
		a := newFakeAdapter("test")
		registry := channel.NewRegistry()
		if err := registry.Register(a); err != nil {
			t.Fatal(err)
		}
		sessions := make(chan *ctxpkg.Session, 1)
		results := make(chan bool, 1)
		g, err := New(ctx, Config{Channels: registry, ResolveSession: resolver.Resolve, Approvals: approval.NewApprovalManager(),
			Factory: func(_ context.Context, s *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (AgentRunner, error) {
				sessions <- s
				return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, _ engine.Reporter) error {
					allowed, _ := approvals.Check(ctx, schema.ToolCall{ID: "task", Name: "bash", Arguments: []byte(`{"command":"sudo ls"}`)})
					results <- allowed
					return nil
				}), nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = g.Shutdown() })
		msg := inbound("old-chat", fmt.Sprint(i), "continue")
		admit(t, g, msg)
		s := receive(t, sessions)
		if s.ID != "old-chat" || s.ModelRef().String() != "fake/old-model" || s.GetWorkingMemory(0)[0].Content != "ABC123" {
			t.Fatal("legacy session forked or lost model/history")
		}
		receive(t, a.sent)
		admit(t, g, inbound("old-chat", "approval", "approve task"))
		if !receive(t, results) {
			t.Fatal("router used canonical instead of actual legacy approval ID")
		}
		if err := g.Shutdown(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(ctx, channel.BuildSessionKey(msg.Route)); !errors.Is(err, sessionstore.ErrNotFound) {
			t.Fatal("legacy session was rewritten", err)
		}
		snap, err := store.Load(ctx, "old-chat")
		if err != nil || len(snap.History) != i+2 {
			t.Fatal("legacy continuity changed", err)
		}
	}
}

func TestCanonicalPreferredNewSessionsAndRouteIsolation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := sessionstore.NewMemoryStore()
	svc := ctxpkg.NewSessionService(store)
	resolver := durableResolver(svc, dir, "default")
	route := inbound("chat", "", "").Route
	key := channel.BuildSessionKey(route)
	for _, id := range []string{"chat", key} {
		s, err := svc.Create(ctx, id, dir, provider.ModelInfo{ProviderID: "fake", ID: id}, "none")
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}
	s, err := resolver.Resolve(ctx, route)
	if err != nil || s.ID != key {
		t.Fatal("legacy overrode canonical", err)
	}
	_ = s.Close()
	// Different channels/accounts/threads never inherit the default legacy chat.
	for _, variant := range []channel.Route{
		{Channel: "test", AccountID: "other", ChatID: "chat"},
		{Channel: "other", AccountID: "default", ChatID: "chat"},
		{Channel: "test", AccountID: "default", ChatID: "chat", ThreadID: "thread"},
		{Channel: "test", AccountID: "default", ChatID: "new-chat"},
	} {
		s, err := resolver.Resolve(ctx, variant)
		if err != nil {
			t.Fatal(err)
		}
		if s.ID != channel.BuildSessionKey(variant) || s.ModelRef().ModelID != "default" || len(s.GetWorkingMemory(0)) != 0 {
			t.Fatal("new route inherited another session")
		}
		_ = s.Close()
	}
}

func TestSessionFallbackOnlyOnNotFound(t *testing.T) {
	for _, target := range []string{"canonical", "legacy"} {
		for _, failure := range []string{"writer", "corrupt"} {
			t.Run(target+"/"+failure, func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				sessionDir := filepath.Join(t.TempDir(), "sessions")
				store, err := sessionstore.NewFileStore(sessionDir)
				if err != nil {
					t.Fatal(err)
				}
				route := inbound("chat", "", "").Route
				id := route.ChatID
				if target == "canonical" {
					id = channel.BuildSessionKey(route)
				}
				var want error
				if failure == "writer" {
					other, err := sessionstore.NewFileStore(sessionDir)
					if err != nil {
						t.Fatal(err)
					}
					lock, err := other.AcquireWriter(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					defer lock.Release()
					want = sessionstore.ErrInUse
				} else {
					path := filepath.Join(sessionDir, fmt.Sprintf("%x.jsonl", sha256.Sum256([]byte(id))))
					if err := os.WriteFile(path, []byte("invalid\ninvalid\n"), 0600); err != nil {
						t.Fatal(err)
					}
					want = sessionstore.ErrCorrupted
				}
				resolver := durableResolver(ctxpkg.NewSessionService(store), dir, "default")
				resolver.Create = func(context.Context, string) (*ctxpkg.Session, error) {
					t.Error("created after non-NotFound error")
					return nil, nil
				}
				if _, err := resolver.Resolve(ctx, route); !errors.Is(err, want) {
					t.Fatalf("error changed: %v, want %v", err, want)
				}
			})
		}
	}
}

func TestSessionIOAndRuntimeErrorsDoNotBlockOtherConversations(t *testing.T) {
	runs := make(chan string, 2)
	blocked := make(chan struct{})
	release := make(chan struct{})
	g, a, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(_ context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
			runs <- s.ID
			return errors.New("agent test failure")
		}), nil
	})
	original := g.conversations.resolve
	g.conversations.resolve = func(ctx context.Context, route channel.Route) (*ctxpkg.Session, error) {
		if route.ChatID == "a" {
			close(blocked)
			select {
			case <-release:
				return nil, errors.New("resume test failure")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return original(ctx, route)
	}
	admit(t, g, inbound("a", "1", "slow resume"))
	receive(t, blocked)
	admit(t, g, inbound("b", "2", "fast"))
	receive(t, runs)
	out := receive(t, a.sent)
	if !strings.Contains(out.msg.Text, "❌ Agent 运行失败") || out.msg.Kind != channel.OutboundError {
		t.Fatal(out)
	}
	close(release)
	out = receive(t, a.sent)
	if !strings.Contains(out.msg.Text, "❌ Session 恢复失败") {
		t.Fatal(out)
	}
}

func TestCancellationAfterSessionResolveBeforeAppend(t *testing.T) {
	called := make(chan struct{}, 1)
	var g *Gateway
	g, _, store := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		g.Cancel()
		called <- struct{}{}
		return runnerFunc(func(context.Context, *ctxpkg.Session, engine.Reporter) error {
			t.Error("runner started after cancel")
			return nil
		}), nil
	})
	msg := inbound("chat", "1", "must not persist")
	admit(t, g, msg)
	receive(t, called)
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	key := channel.BuildSessionKey(msg.Route)
	snap, err := store.Load(context.Background(), key)
	if err != nil || len(snap.History) != 0 {
		t.Fatal("canceled append persisted", err)
	}
	lock, err := store.AcquireWriter(context.Background(), key)
	if err != nil {
		t.Fatal("writer leaked", err)
	}
	_ = lock.Release()
}
