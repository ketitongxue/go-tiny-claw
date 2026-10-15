package delivery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

type recordingAdapter struct {
	routes   []channel.Route
	messages []channel.OutboundMessage
	err      error
	contexts []context.Context
}

func (*recordingAdapter) ID() string                                          { return "test" }
func (*recordingAdapter) Start(context.Context, channel.InboundHandler) error { return nil }
func (a *recordingAdapter) Send(ctx context.Context, route channel.Route, msg channel.OutboundMessage) error {
	a.routes = append(a.routes, route)
	a.messages = append(a.messages, msg)
	a.contexts = append(a.contexts, ctx)
	return a.err
}

func TestReporterPreservesRenderingAndDestination(t *testing.T) {
	a := &recordingAdapter{}
	registry := channel.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	s := NewService(registry)
	route := channel.Route{Channel: "test", AccountID: "default", ChatID: "a", ThreadID: "t"}
	r := NewChannelReporter(s, route)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.OnThinking(ctx)
	r.OnToolCall(ctx, "bash", "echo hi")
	r.OnToolResult(ctx, "bash", "hidden output", false)
	r.OnToolResult(ctx, "bash", "failure", true)
	r.OnMessage(ctx, "final answer")
	r.OnError(ctx, "❌ Agent 运行失败")
	want := []channel.OutboundMessage{
		{Kind: channel.OutboundThinking, Text: "🤔 模型正在慢思考 (Thinking)..."},
		{Kind: channel.OutboundToolStart, ToolName: "bash", Text: "🛠️ **正在执行工具**：`bash`\n参数：`echo hi`"},
		{Kind: channel.OutboundToolResult, ToolName: "bash", Text: "✅ **执行成功** (bash)"},
		{Kind: channel.OutboundToolResult, ToolName: "bash", IsError: true, Text: "⚠️ **执行报错** (bash)：\nfailure"},
		{Kind: channel.OutboundText, Text: "final answer"},
		{Kind: channel.OutboundError, IsError: true, Text: "❌ Agent 运行失败"},
	}
	if !reflect.DeepEqual(a.messages, want) {
		t.Fatalf("rendering changed: %+v", a.messages)
	}
	for i, dest := range a.routes {
		if dest != route || a.contexts[i] != ctx {
			t.Fatal("destination/context changed")
		}
	}
	a.err = errors.New("API unavailable")
	r.OnMessage(ctx, "progress send failure must not panic")
	if err := r.RequestApproval(ctx, approval.Request{Route: route, TaskID: "task", ToolName: "bash"}); !errors.Is(err, a.err) {
		t.Fatalf("notice error swallowed: %v", err)
	}
	if err := s.Send(ctx, channel.Route{}, channel.OutboundMessage{}); err == nil {
		t.Fatal("invalid route accepted")
	}
	unknown := route
	unknown.Channel = "missing"
	if err := s.Send(ctx, unknown, channel.OutboundMessage{}); err == nil {
		t.Fatal("unknown channel accepted")
	}
	before := len(a.messages)
	cancel()
	if err := s.Send(ctx, route, channel.OutboundMessage{}); !errors.Is(err, context.Canceled) || len(a.messages) != before {
		t.Fatal("canceled delivery issued request")
	}
}
