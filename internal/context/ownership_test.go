package context

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestSessionServiceWriterLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := sessionstore.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewSessionService(store)
	model := provider.ModelInfo{ProviderID: "fake", ID: "pinned"}
	s, err := svc.GetOrCreate(ctx, "chat", t.TempDir(), model, "none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	again, err := svc.GetOrCreate(ctx, "chat", t.TempDir(), provider.ModelInfo{}, "")
	if err != nil || again != s {
		t.Fatal("same service did not reuse active session", err)
	}
	if _, err := NewSessionService(store).Resume(ctx, "chat"); !errors.Is(err, sessionstore.ErrInUse) {
		t.Fatalf("second runtime writer error = %v", err)
	}
	if err := svc.Delete(ctx, "chat"); !errors.Is(err, sessionstore.ErrInUse) {
		t.Fatalf("active Delete error = %v", err)
	}
	if err := s.Append(ctx, schema.Message{Role: schema.RoleUser, Content: "remember"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, schema.Message{Role: schema.RoleUser}); !errors.Is(err, ErrSessionClosed) {
		t.Fatal("released session can still mutate", err)
	}
	if err := s.RecordUsage(ctx, 1, 2, provider.UsageBilling{}); !errors.Is(err, ErrSessionClosed) {
		t.Fatal(err)
	}
	resumed, err := svc.Resume(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if resumed == s || resumed.ModelRef() != model.Ref() || len(resumed.GetWorkingMemory(0)) != 1 {
		t.Fatal("closed session cache reused or pinning/history lost")
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resume(ctx, "chat"); !errors.Is(err, sessionstore.ErrNotFound) {
		t.Fatal(err)
	}
	// A failed Resume must not leak ownership, including the not-found path.
	lock, err := store.AcquireWriter(ctx, "chat")
	if err != nil {
		t.Fatal("failed Resume leaked writer", err)
	}
	_ = lock.Release()
}
