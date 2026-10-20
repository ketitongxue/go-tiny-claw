package gateway

import (
	"context"
	"fmt"
	"sync"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

const ConversationQueueSize = 64

type AgentRunner interface {
	Run(context.Context, *ctxpkg.Session, engine.Reporter) error
}

// RuntimeFactory constructs independent engine/registry/accounting state and
// receives optional approval middleware explicitly, without reporter assertions.
type RuntimeFactory func(context.Context, *ctxpkg.Session, channel.Route, engine.Reporter, *approval.Service) (AgentRunner, error)

type Conversation struct {
	Route    channel.Route
	key      string
	queue    chan channel.InboundMessage
	reporter *delivery.ChannelReporter
	runner   AgentRunner // worker-owned
	mu       sync.RWMutex
	session  *ctxpkg.Session // set by worker; commands read its actual ID under mu

	// Protected by ConversationManager.mu, not c.mu. Only the worker closes
	// closed, after Session.Close and removal; admission never waits on it.
	activitySeq uint64
	closing     bool
	closed      chan struct{}
}

func (c *Conversation) sessionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.session != nil {
		return c.session.ID
	}
	return c.key
}

// process remains synchronous, including tools, subagents and approval waits.
// The worker cannot arm an idle timer until this entire call has returned.
func (c *Conversation) process(ctx context.Context, m *ConversationManager, msg channel.InboundMessage) {
	ctx = observability.WithMessagingAttributes(ctx, observability.MessagingAttributes{
		Channel: c.Route.Channel, AccountID: c.Route.AccountID, ChatType: string(msg.ChatType),
	})
	if ctx.Err() != nil {
		return
	}
	if c.session == nil {
		sess, err := m.resolve(ctx, c.Route)
		if err != nil || sess == nil {
			if err == nil {
				err = fmt.Errorf("session resolver returned nil")
			}
			c.reporter.OnError(ctx, fmt.Sprintf("❌ Session 恢复失败: %v", err))
			return
		}
		c.mu.Lock()
		c.session = sess
		c.mu.Unlock()
	}
	if ctx.Err() != nil {
		return
	}
	if c.runner == nil {
		var approvals *approval.Service
		if m.approvals != nil {
			approvals = approval.NewService(m.approvals, c.session.ID, c.Route, c.reporter)
		}
		runner, err := m.factory(ctx, c.session, c.Route, c.reporter, approvals)
		if err != nil {
			c.reporter.OnError(ctx, fmt.Sprintf("❌ 模型不可用，请检查模型或重新登录: %v", err))
			return
		}
		c.runner = runner
	}
	if ctx.Err() != nil {
		return
	}
	if c.runner == nil {
		c.reporter.OnError(ctx, "❌ 无法创建会话引擎")
		return
	}
	if err := c.session.Append(ctx, schema.Message{Role: schema.RoleUser, Content: msg.Text}); err != nil {
		c.reporter.OnError(ctx, fmt.Sprintf("❌ Session 保存失败: %v", err))
		return
	}
	if err := c.runner.Run(ctx, c.session, c.reporter); err != nil && ctx.Err() == nil {
		c.reporter.OnError(ctx, fmt.Sprintf("❌ Agent 运行失败: %v", err))
	}
}
