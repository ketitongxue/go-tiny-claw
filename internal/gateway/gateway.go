// Package gateway owns channel-independent conversation admission and runtime.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
)

var ErrQueueFull = errors.New("当前会话队列已满，请稍后重试")

// Register adapters before Run. Approval is optional and scoped to this gateway.
type Config struct {
	Channels        *channel.Registry
	ResolveSession  SessionResolver
	Factory         RuntimeFactory
	Approvals       *approval.ApprovalManager
	ReportingPolicy delivery.ReportingPolicy
}

type Gateway struct {
	ctx           context.Context
	cancel        context.CancelFunc
	channels      *channel.Registry
	conversations *ConversationManager
	commands      commandRouter
	mu            sync.Mutex
	dedup         deduplicator
	started       bool
	adapters      sync.WaitGroup
	retry         adapterRetryPolicy
}

func New(ctx context.Context, cfg Config) (*Gateway, error) {
	if cfg.Channels == nil || cfg.ResolveSession == nil || cfg.Factory == nil {
		return nil, errors.New("gateway requires channels, session resolver and runtime factory")
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &ConversationManager{ctx: ctx, resolve: cfg.ResolveSession, factory: cfg.Factory,
		delivery: delivery.NewService(cfg.Channels), approvals: cfg.Approvals,
		reportingPolicy: cfg.ReportingPolicy, conversations: make(map[string]*Conversation)}
	return &Gateway{ctx: ctx, cancel: cancel, channels: cfg.Channels, conversations: m,
		retry: defaultAdapterRetryPolicy(), commands: commandRouter{approvals: cfg.Approvals, conversations: m}}, nil
}

// Receive acknowledges acceptance, not completion. Event callback cancellation
// is checked at admission; accepted work uses the gateway lifetime, never an SDK
// callback context that could end as soon as the acknowledgment is sent.
func (g *Gateway) Receive(ctx context.Context, msg channel.InboundMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := msg.Route.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(msg.ID) == "" {
		return errors.New("inbound message ID required")
	}
	if msg.ChatType != channel.ChatDirect && msg.ChatType != channel.ChatGroup {
		return errors.New("invalid chat type")
	}
	if _, ok := g.channels.Get(msg.Route.Channel); !ok {
		return fmt.Errorf("channel %q is not registered", msg.Route.Channel)
	}
	notifyAccepted, err := g.admit(ctx, msg)
	if err != nil {
		return err
	}
	// Admission has released g.mu. Network I/O must never serialize unrelated
	// conversations. Notification failure cannot undo enqueue or trigger retry.
	if notifyAccepted != nil {
		notifyAccepted(ctx)
	}
	return nil
}

// admit keeps dedup/enqueue atomic and returns a notification only for a newly
// accepted ordinary task. It performs no delivery under the admission lock.
func (g *Gateway) admit(ctx context.Context, msg channel.InboundMessage) (func(context.Context), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.ctx.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(msg.Text) == "" {
		return nil, nil
	}
	now := time.Now()
	key := DedupKey(msg)
	if g.dedup.contains(key, now) {
		return nil, nil
	}
	if g.commands.handle(msg) {
		g.dedup.remember(key, now)
		return nil, nil
	}
	// Adapters establish MentionedBot using their platform identity/metadata.
	// Plain control replies remain usable in a group without a mention.
	if msg.ChatType == channel.ChatGroup && !msg.MentionedBot {
		return nil, nil
	}
	c, err := g.conversations.getOrCreate(msg.Route)
	if err != nil {
		return nil, err
	}
	// Reserve output order before the worker can consume the message. This is
	// presentation-only bookkeeping: it never waits on or sends to an adapter.
	notifyAccepted, discardAccepted := c.reporter.PrepareAccepted()
	select {
	case c.queue <- msg:
		g.dedup.remember(key, now)
		return notifyAccepted, nil
	default:
		discardAccepted()
		return nil, ErrQueueFull
	}
}

// ConversationCount is a synchronized runtime diagnostic, without identities.
func (g *Gateway) ConversationCount() int {
	g.conversations.mu.Lock()
	defer g.conversations.mu.Unlock()
	return len(g.conversations.conversations)
}

// Cancel is nonblocking, including when invoked by a running factory/tool.
func (g *Gateway) Cancel() { g.cancel() }

// Shutdown cancels adapters/workers/approvals and waits for Session.Close. It
// must not be invoked by a worker itself; use Cancel there. No writer is released
// while a runner is still committing. Repeated/concurrent shutdown is safe.
func (g *Gateway) Shutdown() error {
	g.cancel()
	g.mu.Lock()
	g.mu.Unlock()
	g.adapters.Wait()
	return g.conversations.wait()
}

// Run supervises each adapter independently. Only cancellation or permanent
// failure of every adapter stops/drains the runtime. Start owns platform cleanup.
func (g *Gateway) Run() (runErr error) {
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return errors.New("gateway already started")
	}
	if err := g.ctx.Err(); err != nil {
		g.mu.Unlock()
		return errors.Join(err, g.Shutdown())
	}
	adapters := g.channels.All()
	if len(adapters) == 0 {
		g.mu.Unlock()
		return errors.New("gateway requires at least one adapter")
	}
	g.started = true
	results := make(chan error, len(adapters))
	g.adapters.Add(len(adapters))
	for _, adapter := range adapters {
		go func(a channel.Adapter) {
			defer g.adapters.Done()
			results <- g.supervise(a)
		}(adapter)
	}
	g.mu.Unlock()
	defer func() { runErr = errors.Join(runErr, g.Shutdown()) }()
	var failures []error
	for remaining := len(adapters); remaining > 0; remaining-- {
		select {
		case <-g.ctx.Done():
			return g.ctx.Err()
		case err := <-results:
			if g.ctx.Err() != nil {
				return g.ctx.Err()
			}
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
