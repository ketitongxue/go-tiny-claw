package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func TestEngineCancelDoesNotPersistNewEvents(t *testing.T) {
	for _, phase := range []string{"action", "usage", "tool"} {
		t.Run(phase, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "sessions")
			store, err := sessionstore.NewFileStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			s, err := ctxpkg.NewSessionService(store).Create(context.Background(), phase, t.TempDir(), provider.ModelInfo{}, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "committed"}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			registry := tools.NewRegistry()
			registry.Register(testTool{name: "read_file", run: func(context.Context, json.RawMessage) (string, error) {
				cancel()
				return "must not be persisted", nil
			}})
			calls := 0
			var p provider.LLMProvider = providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if phase == "tool" {
					return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "call", Name: "read_file"}}}, nil
				}
				cancel()
				return &schema.Message{Role: schema.RoleAssistant, Content: "must not be persisted", Usage: &schema.Usage{PromptTokens: 10}}, nil
			})
			if phase == "usage" {
				p = observability.NewCostTracker(p, provider.ModelInfo{}, s)
			}
			if err := NewAgentEngine(p, registry, false, false).Run(ctx, s, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run error = %v", err)
			}
			if calls != 1 {
				t.Fatalf("model called after cancel: %d", calls)
			}
			// Read through another store, so a cached snapshot cannot mask disk damage.
			reader, err := sessionstore.NewFileStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := reader.Load(context.Background(), s.ID)
			want := 1
			if phase == "tool" {
				want = 2 // the assistant tool request committed before cancellation
			}
			if err != nil || len(loaded.History) != want || loaded.History[0].Content != "committed" || loaded.Usage.PromptTokens != 0 {
				t.Fatalf("committed state damaged: %+v (%v)", loaded, err)
			}
		})
	}
}
