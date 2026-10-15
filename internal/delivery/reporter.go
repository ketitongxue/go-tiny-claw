package delivery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
)

// ChannelReporter has an immutable destination, including for parallel tools.
// Rendering is shared; payload encoding and platform size limits stay in adapters.
type ChannelReporter struct {
	delivery *Service
	route    channel.Route
	policy   ReportingPolicy

	// Only presentation waits for pending admission notices. No network I/O
	// holds acceptedMu, and unrelated conversation reporters remain independent.
	acceptedMu      sync.Mutex
	acceptedPending int
	acceptedDone    chan struct{}
}

func NewChannelReporter(delivery *Service, route channel.Route, policy ReportingPolicy) *ChannelReporter {
	return &ChannelReporter{delivery: delivery, route: route, policy: policy}
}

func (r *ChannelReporter) Route() channel.Route { return r.route }

func (r *ChannelReporter) report(ctx context.Context, msg channel.OutboundMessage) {
	if err := r.send(ctx, msg); err != nil {
		log.Printf("[Gateway] 发送消息失败: %v", err)
	}
}

// PrepareAccepted reserves presentation order before enqueue, without sending
// or waiting for delivery. Admission must invoke notify after unlocking on
// success, or discard on queue rejection. Neither callback returns an error.
func (r *ChannelReporter) PrepareAccepted() (notify func(context.Context), discard func()) {
	if !r.policy.ShowAccepted() {
		return nil, func() {}
	}
	r.acceptedMu.Lock()
	if r.acceptedPending == 0 {
		r.acceptedDone = make(chan struct{})
	}
	r.acceptedPending++
	r.acceptedMu.Unlock()
	done := sync.OnceFunc(func() {
		r.acceptedMu.Lock()
		defer r.acceptedMu.Unlock()
		r.acceptedPending--
		if r.acceptedPending == 0 {
			close(r.acceptedDone)
			r.acceptedDone = nil
		}
	})
	return func(ctx context.Context) {
		defer done() // Failed/canceled Accepted must not suppress the final reply.
		r.OnAccepted(ctx)
	}, done
}

func (r *ChannelReporter) send(ctx context.Context, msg channel.OutboundMessage) error {
	for {
		r.acceptedMu.Lock()
		done := r.acceptedDone
		r.acceptedMu.Unlock()
		if done == nil {
			return r.delivery.Send(ctx, r.route, msg)
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// OnAccepted is an admission notification, not part of engine.Reporter.
// Once enqueued, a task stays accepted even if this best-effort send fails.
func (r *ChannelReporter) OnAccepted(ctx context.Context) {
	if !r.policy.ShowAccepted() {
		return
	}
	if err := r.delivery.Send(ctx, r.route, channel.OutboundMessage{Kind: channel.OutboundText, Text: "🤖 已收到，正在处理……"}); err != nil {
		log.Printf("[Gateway] 发送任务接收通知失败: %v", err)
	}
}

func (r *ChannelReporter) OnThinking(ctx context.Context) {
	if !r.policy.ShowThinking() {
		return
	}
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundThinking, Text: "🤔 模型正在慢思考 (Thinking)..."})
}

func (r *ChannelReporter) OnToolCall(ctx context.Context, toolName, args string) {
	if !r.policy.ShowToolCall() {
		return
	}
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundToolStart, ToolName: toolName,
		Text: fmt.Sprintf("🛠️ **正在执行工具**：`%s`\n参数：`%s`", toolName, args)})
}

func (r *ChannelReporter) OnToolResult(ctx context.Context, toolName, result string, isError bool) {
	if !r.policy.ShowToolResult(isError) {
		return
	}
	text := fmt.Sprintf("✅ **执行成功** (%s)", toolName)
	if isError {
		text = fmt.Sprintf("⚠️ **执行报错** (%s)：\n%s", toolName, result)
	}
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundToolResult, Text: text, ToolName: toolName, IsError: isError})
}

func (r *ChannelReporter) OnMessage(ctx context.Context, content string) {
	if !r.policy.ShowFinal() {
		return
	}
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundText, Text: content})
}

// OnError reports task-level runtime failures, never individual tool failures.
func (r *ChannelReporter) OnError(ctx context.Context, content string) {
	if !r.policy.ShowError() {
		return
	}
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundError, Text: content, IsError: true})
}

// Approval is control-plane input, not telemetry: always send in every mode.
// Unlike progress reports, delivery must propagate failures and deny the
// operation rather than silently leaving a pending request behind.
func (r *ChannelReporter) RequestApproval(ctx context.Context, req approval.Request) error {
	if req.Route != r.route {
		return errors.New("approval notifier route mismatch")
	}
	text := fmt.Sprintf(`⚠️ **高危操作审批请求**
Agent 试图执行以下动作:
- 工具: %s
- 参数: %s

任务 ID: **%s**

👉 请在此消息下方回复 "approve %s" 或 "reject %s" 来决定是否放行。`, req.ToolName, req.Args, req.TaskID, req.TaskID, req.TaskID)
	return r.send(ctx, channel.OutboundMessage{Kind: channel.OutboundText, Text: text})
}

var _ engine.Reporter = (*ChannelReporter)(nil)
var _ approval.Notifier = (*ChannelReporter)(nil)
