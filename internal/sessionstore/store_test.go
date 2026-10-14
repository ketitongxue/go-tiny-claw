package sessionstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func snapshot(id string) Snapshot {
	return Snapshot{Metadata: Metadata{ID: id, WorkDir: "/project", ProviderID: "custom", ModelID: "model-a", AuthType: "oauth", CreatedAt: time.Now()}, Usage: Usage{CostKnown: true, Currency: "CNY"}}
}
func TestStores(t *testing.T) {
	f, err := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]Store{"memory": NewMemoryStore(), "file": f} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			id := "../../chat"
			if err := store.Create(ctx, snapshot(id)); err != nil {
				t.Fatal(err)
			}
			if err := store.Create(ctx, snapshot(id)); !errors.Is(err, ErrExists) {
				t.Fatal(err)
			}
			msgs := []schema.Message{{Role: schema.RoleUser, Content: "task"}, {Role: schema.RoleAssistant, Usage: &schema.Usage{PromptTokens: 42}, ToolCalls: []schema.ToolCall{{ID: "call_123", Name: "read_file", Arguments: []byte(`{"path":"x"}`)}}}, {Role: schema.RoleUser, ToolCallID: "call_123", Content: "完整输出"}}
			for _, m := range msgs {
				e := NewEvent("message")
				e.Message = &m
				if err := store.Append(ctx, id, e); err != nil {
					t.Fatal(err)
				}
			}
			e := NewEvent("usage")
			e.Usage = &Usage{PromptTokens: 100, CompletionTokens: 20, EstimatedCost: 1, CostKnown: true, Currency: "CNY"}
			if err := store.Append(ctx, id, e); err != nil {
				t.Fatal(err)
			}
			s, err := store.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.History) != 3 || s.History[2].ToolCallID != s.History[1].ToolCalls[0].ID || s.Usage.PromptTokens != 100 || s.Metadata.ModelID != "model-a" || s.History[1].Usage.PromptTokens != 42 {
				t.Fatalf("%+v", s)
			}
			meta := s.Metadata
			meta.AuthType = "api_key"
			updated := NewEvent("metadata_updated")
			updated.Metadata = &meta
			if err := store.Append(ctx, id, updated); err != nil {
				t.Fatal(err)
			}
			s, err = store.Load(ctx, id)
			if err != nil || s.Metadata.AuthType != "api_key" {
				t.Fatal("metadata replay failed", err)
			}
			list, err := store.List(ctx)
			if err != nil || len(list) != 1 || list[0].Messages != 3 {
				t.Fatalf("%+v %v", list, err)
			}
			if err := store.Delete(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(ctx, id); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}
func TestRecoveryPermissionsConcurrency(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "home", "sessions")
	f, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint(i)
			if err := f.Create(ctx, snapshot(id)); err != nil {
				t.Error(err)
			}
			for j := 0; j < 10; j++ {
				e := NewEvent("message")
				e.Message = &schema.Message{Role: schema.RoleUser, Content: "hi"}
				if err := f.Append(ctx, id, e); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	for _, p := range []string{filepath.Dir(dir), dir, f.path("0")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		expected := os.FileMode(0700)
		if p == f.path("0") {
			expected = 0600
		}
		if info.Mode().Perm() != expected {
			t.Fatalf("%s: %v", p, info.Mode())
		}
	}
	p := f.path("0")
	original, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(original, []byte(`{"version":`)...), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := f.Load(ctx, "0")
	if err != nil || len(s.History) != 10 {
		t.Fatalf("%v %d", err, len(s.History))
	}
	e := NewEvent("message")
	e.Message = &schema.Message{Role: schema.RoleUser, Content: "after crash"}
	if err := f.Append(ctx, "0", e); err != nil {
		t.Fatal(err)
	}
	s, err = f.Load(ctx, "0")
	if err != nil || len(s.History) != 11 {
		t.Fatalf("%v %d", err, len(s.History))
	}
	if err := os.WriteFile(p, append([]byte("broken\n"), original...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Load(ctx, "0"); !errors.Is(err, ErrCorrupted) {
		t.Fatal(err)
	}
}
