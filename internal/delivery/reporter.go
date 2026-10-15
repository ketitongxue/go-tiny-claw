package delivery

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
)

// ChannelReporter has an immutable destination, including for parallel tools.
// Rendering is shared; payload encoding and platform size limits stay in adapters.
type ChannelReporter struct {
	delivery *Service
	route    channel.Route
}

func NewChannelReporter(delivery *Service, route channel.Route) *ChannelReporter {
	return &ChannelReporter{delivery: delivery, route: route}
}

func (r *ChannelReporter) Route() channel.Route { return r.route }

func (r *ChannelReporter) report(ctx context.Context, msg channel.OutboundMessage) {
	if err := r.delivery.Send(ctx, r.route, msg); err != nil {
		log.Printf("[Gateway] 发送消息失败: %v", err)
	}
}

func (r *ChannelReporter) OnThinking(ctx context.Context) {
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundThinking, Text: "🤔 模型正在慢思考 (Thinking)..."})
}

func (r *ChannelReporter) OnToolCall(ctx context.Context, toolName, args string) {
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundToolStart, ToolName: toolName,
		Text: fmt.Sprintf("🛠️ **正在执行工具**：`%s`\n参数：`%s`", toolName, args)})
}

func (r *ChannelReporter) OnToolResult(ctx context.Context, toolName, result string, isError bool) {
	text := fmt.Sprintf("✅ **执行成功** (%s)", toolName)
	if isError {
		text = fmt.Sprintf("⚠️ **执行报错** (%s)：\n%s", toolName, result)
	}
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundToolResult, Text: text, ToolName: toolName, IsError: isError})
}

func (r *ChannelReporter) OnMessage(ctx context.Context, content string) {
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundText, Text: content})
}

func (r *ChannelReporter) OnError(ctx context.Context, content string) {
	r.report(ctx, channel.OutboundMessage{Kind: channel.OutboundError, Text: content, IsError: true})
}

// Unlike progress reports, approval delivery must propagate failures and deny
// the operation rather than silently leaving a pending request behind.
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
	return r.delivery.Send(ctx, r.route, channel.OutboundMessage{Kind: channel.OutboundText, Text: text})
}

var _ engine.Reporter = (*ChannelReporter)(nil)
var _ approval.Notifier = (*ChannelReporter)(nil)
