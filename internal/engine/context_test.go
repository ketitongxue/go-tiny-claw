package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func TestThirtyTurnsNoCharCompactor(t *testing.T) {
	registry := tools.NewRegistry()
	for _, name := range []string{"read_file", "bash"} {
		registry.Register(testTool{name: name, run: func(context.Context, json.RawMessage) (string, error) {
			return strings.Repeat("中文 tool output\n", 1000), nil
		}})
	}
	calls := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 33 {
			return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprintf("r%d", calls), Name: "read_file", Arguments: []byte(`{"path":"x"}`)}, {ID: fmt.Sprintf("b%d", calls), Name: "bash", Arguments: []byte(`{"command":"echo x"}`)}}}, nil
	})
	s := ctxpkg.NewSession("long", t.TempDir())
	if err := s.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "task"}); err != nil {
		t.Fatal(err)
	}
	eng := NewAgentEngine(p, registry, false, false)
	eng.ContextBuilder = ctxpkg.NewContextBuilder(ctxpkg.ContextPolicy{MaxToolResultTokens: 128})
	var logs strings.Builder
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	if err := eng.Run(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "[Compactor]") || strings.Contains(logs.String(), "内存告警") || strings.Contains(logs.String(), "压缩完成") {
		t.Fatal("legacy warning still active")
	}
	if len(s.GetWorkingMemory(0)) != 98 {
		t.Fatalf("lost complete history: %d", len(s.GetWorkingMemory(0)))
	}
	files, _ := filepath.Glob(filepath.Join(s.WorkDir, ".claw", "traces", "*.json"))
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"context.window_tokens", "context.input_budget_tokens", "context.estimated_tokens", "context.omitted_messages", "context.truncated_tool_results"} {
		if !strings.Contains(string(data), key) {
			t.Fatal("missing " + key)
		}
	}
}
func TestContextLimitOnlyOneRetry(t *testing.T) {
	for _, alwaysFail := range []bool{false, true} {
		calls := 0
		p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			calls++
			if alwaysFail || calls == 1 {
				return nil, errors.New("context_length_exceeded")
			}
			return &schema.Message{Role: schema.RoleAssistant}, nil
		})
		s := ctxpkg.NewSession("retry", t.TempDir())
		s.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "task"})
		err := NewAgentEngine(p, tools.NewRegistry(), false, false).Run(context.Background(), s, nil)
		if calls != 2 || (err != nil) != alwaysFail {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	}
}
func TestExplorerLongOutputBudget(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(testTool{name: "read_file", run: func(context.Context, json.RawMessage) (string, error) { return strings.Repeat("中文", 10000), nil }})
	calls := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls > 1 && !strings.Contains(msgs[len(msgs)-1].Content, "truncated") {
			t.Fatal("Explorer output not bounded")
		}
		if calls == 10 {
			return &schema.Message{Role: schema.RoleAssistant, Content: "report"}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprint(calls), Name: "read_file", Arguments: []byte(`{}`)}}}, nil
	})
	e := NewAgentEngine(p, tools.NewRegistry(), false, false)
	e.ContextBuilder = ctxpkg.NewContextBuilder(ctxpkg.ContextPolicy{MaxToolResultTokens: 200})
	report, err := e.RunSub(context.Background(), "inspect", reg, nil)
	if err != nil || report != "report" {
		t.Fatalf("%s %v", report, err)
	}
}
