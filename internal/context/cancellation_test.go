package context

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestCanceledSessionMutationsPreserveCommittedState(t *testing.T) {
	store, err := sessionstore.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := NewSessionService(store).Create(ctx, "cancel", t.TempDir(), provider.ModelInfo{}, "none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(ctx, schema.Message{Role: schema.RoleUser, Content: "committed"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, mutate := range []func() error{
		func() error {
			return s.Append(canceled, schema.Message{Role: schema.RoleUser, Content: "not committed"})
		},
		func() error { return s.RecordUsage(canceled, 100, 20, provider.UsageBilling{}) },
		func() error { return s.MarkCostUnknown(canceled) },
	} {
		if err := mutate(); !errors.Is(err, context.Canceled) {
			t.Fatalf("mutation error = %v", err)
		}
	}
	after, err := store.Load(ctx, s.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("cancellation changed committed state: %v", err)
	}
	if len(s.GetWorkingMemory(0)) != 1 || s.UsageSnapshot().PromptTokens != 0 {
		t.Fatal("canceled mutation published in memory")
	}
}

type contextStore struct {
	*sessionstore.MemoryStore
	want context.Context
	seen int
}

func (s *contextStore) Append(ctx context.Context, id string, events ...sessionstore.Event) error {
	if ctx != s.want {
		return errors.New("persistence context was replaced")
	}
	s.seen++
	return s.MemoryStore.Append(ctx, id, events...)
}

func TestMutationContextReachesStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &contextStore{MemoryStore: sessionstore.NewMemoryStore(), want: ctx}
	s, err := NewSessionService(store).Create(ctx, "propagation", t.TempDir(), provider.ModelInfo{}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(ctx, schema.Message{Role: schema.RoleUser}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(ctx, 1, 2, provider.UsageBilling{}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCostUnknown(ctx); err != nil {
		t.Fatal(err)
	}
	if store.seen != 3 {
		t.Fatalf("store mutations = %d", store.seen)
	}
}
