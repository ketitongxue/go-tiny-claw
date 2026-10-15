package delivery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestChannelReporterModes(t *testing.T) {
	for _, mode := range []ReportMode{ReportModeQuiet, ReportModeCompact, ReportModeVerbose, ""} {
		t.Run(string(mode), func(t *testing.T) {
			a := &recordingAdapter{}
			registry := channel.NewRegistry()
			if err := registry.Register(a); err != nil {
				t.Fatal(err)
			}
			route := channel.Route{Channel: "test", AccountID: "default", ChatID: "chat", ThreadID: "thread"}
			r := NewChannelReporter(NewService(registry), route, ReportingPolicy{Mode: mode})
			ctx := context.Background()
			r.OnAccepted(ctx)
			r.OnThinking(ctx)
			r.OnToolCall(ctx, "bash", "private-command")
			r.OnToolResult(ctx, "bash", "private-stdout", false)
			r.OnToolResult(ctx, "bash", "private-stderr", true)
			r.OnToolCall(ctx, "[Subagent] read_file", "private-path")
			r.OnToolResult(ctx, "[Subagent] read_file", "private-code", true)
			r.OnMessage(ctx, "final answer")
			r.OnError(ctx, "❌ Agent 运行失败: task failure")
			if err := r.RequestApproval(ctx, approval.Request{Route: route, TaskID: "task", ToolName: "bash", Args: "danger"}); err != nil {
				t.Fatal(err)
			}
			var want []channel.OutboundMessage
			if mode == "" || mode == ReportModeCompact {
				want = append(want, channel.OutboundMessage{Kind: channel.OutboundText, Text: "🤖 已收到，正在处理……"})
			}
			if mode == ReportModeVerbose {
				want = append(want,
					channel.OutboundMessage{Kind: channel.OutboundThinking, Text: "🤔 模型正在慢思考 (Thinking)..."},
					channel.OutboundMessage{Kind: channel.OutboundToolStart, ToolName: "bash", Text: "🛠️ **正在执行工具**：`bash`\n参数：`private-command`"},
					channel.OutboundMessage{Kind: channel.OutboundToolResult, ToolName: "bash", Text: "✅ **执行成功** (bash)"},
					channel.OutboundMessage{Kind: channel.OutboundToolResult, ToolName: "bash", IsError: true, Text: "⚠️ **执行报错** (bash)：\nprivate-stderr"},
					channel.OutboundMessage{Kind: channel.OutboundToolStart, ToolName: "[Subagent] read_file", Text: "🛠️ **正在执行工具**：`[Subagent] read_file`\n参数：`private-path`"},
					channel.OutboundMessage{Kind: channel.OutboundToolResult, ToolName: "[Subagent] read_file", IsError: true, Text: "⚠️ **执行报错** ([Subagent] read_file)：\nprivate-code"},
				)
			}
			want = append(want,
				channel.OutboundMessage{Kind: channel.OutboundText, Text: "final answer"},
				channel.OutboundMessage{Kind: channel.OutboundError, IsError: true, Text: "❌ Agent 运行失败: task failure"},
				channel.OutboundMessage{Kind: channel.OutboundText, Text: `⚠️ **高危操作审批请求**
Agent 试图执行以下动作:
- 工具: bash
- 参数: danger

任务 ID: **task**

👉 请在此消息下方回复 "approve task" 或 "reject task" 来决定是否放行。`},
			)
			if !reflect.DeepEqual(a.messages, want) {
				t.Fatalf("got %d sends: %+v; want %d: %+v", len(a.messages), a.messages, len(want), want)
			}
			for i, dest := range a.routes {
				if dest != route || a.contexts[i] != ctx {
					t.Fatal("destination/context changed")
				}
			}
		})
	}
}

func TestApprovalDeliveryFailureDeniesEveryMode(t *testing.T) {
	for _, mode := range []ReportMode{ReportModeQuiet, ReportModeCompact, ReportModeVerbose} {
		t.Run(string(mode), func(t *testing.T) {
			a := &recordingAdapter{err: errors.New("API unavailable")}
			registry := channel.NewRegistry()
			if err := registry.Register(a); err != nil {
				t.Fatal(err)
			}
			route := channel.Route{Channel: "test", AccountID: "default", ChatID: "chat"}
			r := NewChannelReporter(NewService(registry), route, ReportingPolicy{Mode: mode})
			req := approval.Request{SessionID: "session", Route: route, TaskID: "task", ToolName: "bash"}
			if err := r.RequestApproval(context.Background(), req); !errors.Is(err, a.err) {
				t.Fatalf("approval send error swallowed: %v", err)
			}
			manager := approval.NewApprovalManager()
			allowed, reason := manager.Wait(context.Background(), req, r)
			if allowed || !strings.Contains(reason, "发送审批请求失败") || len(a.messages) != 2 {
				t.Fatalf("send failure did not deny operation: %t, %s, %+v", allowed, reason, a.messages)
			}
			if manager.Resolve(approval.Key{SessionID: req.SessionID, TaskID: req.TaskID}, true, "late") {
				t.Fatal("failed notification left pending approval")
			}
			req.Route.ChatID = "wrong-chat"
			if err := r.RequestApproval(context.Background(), req); err == nil || len(a.messages) != 2 {
				t.Fatalf("route mismatch sent approval: %v", err)
			}
		})
	}
}

func TestDiscardedAcceptedDoesNotBlockReports(t *testing.T) {
	a := &recordingAdapter{}
	registry := channel.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	route := channel.Route{Channel: "test", AccountID: "default", ChatID: "chat"}
	r := NewChannelReporter(NewService(registry), route, ReportingPolicy{})
	notify, discard := r.PrepareAccepted()
	if notify == nil || len(a.messages) != 0 {
		t.Fatal("preparing Accepted sent a notification or failed to reserve compact output")
	}
	// Rejected enqueue must not notify or release another task's reservation.
	_, discardSecond := r.PrepareAccepted()
	defer discardSecond()
	discard()
	discard() // Completion is idempotent.
	waiting, stopWaiting := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopWaiting()
	if err := r.RequestApproval(waiting, approval.Request{Route: route, TaskID: "task"}); !errors.Is(err, context.DeadlineExceeded) || len(a.messages) != 0 {
		t.Fatalf("discard released another Accepted reservation: %v, %+v", err, a.messages)
	}
	discardSecond()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r.OnMessage(ctx, "still visible")
	if !reflect.DeepEqual(a.messages, []channel.OutboundMessage{{Kind: channel.OutboundText, Text: "still visible"}}) {
		t.Fatalf("discarded notification blocked/leaked delivery: %+v", a.messages)
	}
}

func TestPendingAcceptedWaitHonorsCancellation(t *testing.T) {
	a := &recordingAdapter{}
	registry := channel.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	route := channel.Route{Channel: "test", AccountID: "default", ChatID: "chat"}
	r := NewChannelReporter(NewService(registry), route, ReportingPolicy{})
	_, discard := r.PrepareAccepted()
	defer discard()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.RequestApproval(ctx, approval.Request{Route: route, TaskID: "task"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pending Accepted ignored cancellation/approval send error: %v", err)
	}
	if len(a.messages) != 0 {
		t.Fatal("canceled approval was sent while Accepted was pending")
	}
}

func TestReporterPreservesRenderingAndDestination(t *testing.T) {
	a := &recordingAdapter{}
	registry := channel.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	s := NewService(registry)
	route := channel.Route{Channel: "test", AccountID: "default", ChatID: "a", ThreadID: "t"}
	r := NewChannelReporter(s, route, ReportingPolicy{Mode: ReportModeVerbose})
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
