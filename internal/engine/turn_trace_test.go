package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func TestMultiTurnSpanLifetimes(t *testing.T) {
	s := ctxpkg.NewSession("three-turns", t.TempDir())
	registry := tools.NewRegistry()
	registry.Register(testTool{name: "read_file", run: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }})
	calls := 0
	p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		// Ensure millisecond durations are nonzero; correctness is asserted by
		// span ordering, not a fragile wall-clock upper bound.
		time.Sleep(2 * time.Millisecond)
		if calls == 3 {
			return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprint(calls), Name: "read_file"}}}, nil
	})
	if err := NewAgentEngine(p, registry, false, false).Run(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(s.WorkDir, ".claw", "traces", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("traces: %v (%v)", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var root observability.Span
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 3 {
		t.Fatalf("turns = %d", len(root.Children))
	}
	for i, turn := range root.Children {
		if turn.Name != fmt.Sprintf("Turn-%d", i+1) || turn.DurationMs <= 0 || turn.EndTime.Before(turn.StartTime) {
			t.Fatalf("invalid turn %d: %s %v/%v %dms", i+1, turn.Name, turn.StartTime, turn.EndTime, turn.DurationMs)
		}
		if i < 2 && turn.EndTime.After(root.Children[i+1].StartTime) {
			t.Fatalf("%s includes a later turn", turn.Name)
		}
		for _, child := range turn.Children {
			if child.EndTime.After(turn.EndTime) || child.EndTime.IsZero() {
				t.Fatalf("unfinished child %s in %s", child.Name, turn.Name)
			}
		}
	}
}
