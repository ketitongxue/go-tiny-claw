package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

type providerFunc func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f providerFunc) Generate(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, msgs, defs)
}

type testTool struct {
	name string
	run  func(context.Context, json.RawMessage) (string, error)
}

func (t testTool) Name() string { return t.name }
func (t testTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.name}
}
func (t testTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.run(ctx, args)
}

type recordingReporter struct {
	mu       sync.Mutex
	thinking int
	calls    []string
	results  []string
	messages []string
}

func (r *recordingReporter) OnThinking(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.thinking++
}
func (r *recordingReporter) OnToolCall(_ context.Context, name, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
}
func (r *recordingReporter) OnToolResult(_ context.Context, name, _ string, _ bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, name)
}
func (r *recordingReporter) OnMessage(_ context.Context, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, content)
}

func TestRunPreservesThinkingParallelToolsAndTrace(t *testing.T) {
	session := ctxpkg.NewSession("chat-a", t.TempDir())
	session.Append(schema.Message{Role: schema.RoleUser, Content: "inspect two files"})
	registry := tools.NewRegistry()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	registry.Register(testTool{name: "read_file", run: func(ctx context.Context, args json.RawMessage) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
			return string(args), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}})
	calls := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if msgs[0].Role != schema.RoleSystem {
			return nil, errors.New("system prompt missing")
		}
		switch calls {
		case 1, 3:
			if len(defs) != 0 {
				return nil, errors.New("thinking should not execute tools")
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "thinking"}, nil
		case 2:
			if len(defs) != 1 || defs[0].Name != "read_file" {
				return nil, errors.New("action tools missing")
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "reading", ToolCalls: []schema.ToolCall{
				{ID: "call-a", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)},
				{ID: "call-b", Name: "read_file", Arguments: json.RawMessage(`{"path":"b"}`)},
			}}, nil
		case 4:
			if msgs[2].Content != "thinking\nreading" || len(msgs[2].ToolCalls) != 2 {
				return nil, errors.New("thinking/action were not merged into one assistant message")
			}
			if msgs[3].ToolCallID != "call-a" || msgs[4].ToolCallID != "call-b" {
				return nil, errors.New("parallel tool results lost request order")
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
		default:
			return nil, errors.New("unexpected model call")
		}
	})
	reporter := &recordingReporter{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- NewAgentEngine(p, registry, true, false).Run(ctx, session, reporter) }()
	// Both calls must have entered before either can finish: this detects an
	// accidental serial tool loop without making wall-clock speed assertions.
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("tool calls did not execute concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 4 || reporter.thinking != 2 || len(reporter.calls) != 2 || len(reporter.results) != 2 || len(reporter.messages) != 2 {
		t.Fatalf("lost engine events: calls=%d reporter=%+v", calls, reporter)
	}
	traces, err := filepath.Glob(filepath.Join(session.WorkDir, ".claw", "traces", "trace_chat-a_*.json"))
	if err != nil || len(traces) != 1 {
		t.Fatalf("expected one trace, got %v (%v)", traces, err)
	}
	data, err := os.ReadFile(traces[0])
	if err != nil || !json.Valid(data) || !strings.Contains(string(data), "Tool.Execute") {
		t.Fatalf("missing valid tool trace: %s (%v)", data, err)
	}
}

func TestRunPassesRecoveryAndRepeatedFailureReminderToModel(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(testTool{name: "read_file", run: func(context.Context, json.RawMessage) (string, error) {
		return "", errors.New("no such file or directory")
	}})
	calls := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls > 1 {
			foundRecovery := false
			for _, msg := range msgs {
				if msg.ToolCallID != "" && strings.Contains(msg.Content, "[系统救援指南]") {
					foundRecovery = true
				}
			}
			if !foundRecovery {
				return nil, errors.New("model did not receive recovery guidance")
			}
		}
		if calls == 4 {
			if !strings.Contains(msgs[len(msgs)-1].Content, "SYSTEM REMINDER") {
				return nil, errors.New("missing repeated failure reminder")
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "need correct path"}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprint(calls), Name: "read_file", Arguments: json.RawMessage(`{"path":"missing"}`)}}}, nil
	})
	session := ctxpkg.NewSession("chat-recovery", t.TempDir())
	session.Append(schema.Message{Role: schema.RoleUser, Content: "read file"})
	if err := NewAgentEngine(p, registry, false, false).Run(context.Background(), session, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunSubOnlyUsesProvidedRegistry(t *testing.T) {
	mainRegistry := tools.NewRegistry()
	mainRegistry.Register(testTool{name: "write_file", run: func(context.Context, json.RawMessage) (string, error) {
		t.Error("subagent executed a tool outside its provided registry")
		return "", nil
	}})
	readOnly := tools.NewRegistry()
	readOnly.Register(testTool{name: "read_file", run: func(context.Context, json.RawMessage) (string, error) {
		return "source evidence", nil
	}})
	calls := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if len(defs) != 1 || defs[0].Name != "read_file" {
			return nil, errors.New("subagent was offered main agent tools")
		}
		if calls == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "read", Name: "read_file"}, {ID: "write", Name: "write_file"}}}, nil
		}
		if msgs[3].ToolCallID != "read" || msgs[3].Content != "source evidence" || msgs[4].ToolCallID != "write" || !strings.Contains(msgs[4].Content, "不存在") {
			return nil, errors.New("subagent evidence or tool isolation lost")
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "verified evidence"}, nil
	})
	reporter := &recordingReporter{}
	answer, err := NewAgentEngine(p, mainRegistry, true, true).RunSub(context.Background(), "inspect", readOnly, reporter)
	if err != nil || answer != "verified evidence" || reporter.thinking != 0 || len(reporter.calls) != 2 {
		t.Fatalf("subagent result=%q err=%v reporter=%+v", answer, err, reporter)
	}
	for _, name := range reporter.calls {
		if !strings.HasPrefix(name, "[Subagent] ") {
			t.Fatalf("subagent reporting missing label: %s", name)
		}
	}
}

func TestEnginesRespectCanceledContextBeforeModelCall(t *testing.T) {
	p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		t.Error("model called after cancellation")
		return &schema.Message{Role: schema.RoleAssistant}, nil
	})
	registry := tools.NewRegistry()
	e := NewAgentEngine(p, registry, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Run(ctx, ctxpkg.NewSession("canceled", t.TempDir()), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if _, err := e.RunSub(ctx, "inspect", registry, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunSub error = %v", err)
	}
}

func TestRunSubStopsAtTurnBudget(t *testing.T) {
	calls := 0
	p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprint(calls), Name: "unknown"}}}, nil
	})
	registry := tools.NewRegistry()
	_, err := NewAgentEngine(p, registry, false, false).RunSub(context.Background(), "inspect", registry, nil)
	if err == nil || calls != 10 {
		t.Fatalf("subagent budget not enforced: calls=%d error=%v", calls, err)
	}
}
