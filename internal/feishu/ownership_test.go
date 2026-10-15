package feishu

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func TestChatWorkerHoldsWriterUntilShutdown(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "cli_test")
	t.Setenv("FEISHU_APP_SECRET", "test")
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := sessionstore.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := ctxpkg.NewSessionService(store)
	workDir := t.TempDir()
	started := make(chan struct{}, 1)
	sessions := gateway.DurableSessions{Service: svc, LegacyID: legacyID, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, workDir, provider.ModelInfo{}, "")
	}}
	b := newTestRuntime(t, workDir, sessions.Resolve, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (gateway.AgentRunner, error) {
		p := resumeProvider(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		})
		return engine.NewAgentEngine(p, tools.NewRegistry(), false, false), nil
	}, approval.NewApprovalManager())
	if err := b.handleMessage(context.Background(), textEvent("chat", "1", "task")); err != nil {
		t.Fatal(err)
	}
	receive(t, started)
	other, err := sessionstore.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := channel.BuildSessionKey(feishuRoute("chat"))
	if err := other.Delete(context.Background(), key); !errors.Is(err, sessionstore.ErrInUse) {
		t.Fatalf("active chat Delete error = %v", err)
	}
	b.Close()
	b.workers.Wait()
	snap, err := other.Load(context.Background(), key)
	if err != nil || len(snap.History) != 1 || snap.History[0].Content != "task" {
		t.Fatal("shutdown persisted new events or damaged history", err)
	}
	if err := other.Delete(context.Background(), key); err != nil {
		t.Fatal("worker shutdown did not release writer", err)
	}
}

func TestFeishuCancellationBeforeUserAppend(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "cli_test")
	t.Setenv("FEISHU_APP_SECRET", "test")
	store, err := sessionstore.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	svc := ctxpkg.NewSessionService(store)
	workDir := t.TempDir()
	var b *testRuntime
	factoryCalled := make(chan struct{}, 1)
	sessions := gateway.DurableSessions{Service: svc, LegacyID: legacyID, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, workDir, provider.ModelInfo{}, "")
	}}
	b = newTestRuntime(t, workDir, sessions.Resolve, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (gateway.AgentRunner, error) {
		b.Close() // shutdown after resolving, before persistence of the prompt
		factoryCalled <- struct{}{}
		p := resumeProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			t.Error("provider called after bot cancellation")
			return nil, context.Canceled
		})
		return engine.NewAgentEngine(p, tools.NewRegistry(), false, false), nil
	}, approval.NewApprovalManager())
	if err := b.handleMessage(context.Background(), textEvent("chat", "1", "must not persist")); err != nil {
		t.Fatal(err)
	}
	receive(t, factoryCalled)
	b.workers.Wait()
	key := channel.BuildSessionKey(feishuRoute("chat"))
	snap, err := store.Load(context.Background(), key)
	if err != nil || len(snap.History) != 0 {
		t.Fatal("canceled Feishu user append persisted", err)
	}
	lock, err := store.AcquireWriter(context.Background(), key)
	if err != nil {
		t.Fatal("shutdown leaked writer", err)
	}
	_ = lock.Release()
}
