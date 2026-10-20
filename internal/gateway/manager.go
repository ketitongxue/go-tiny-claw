package gateway

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
)

// ConversationManager holds one worker/writer per canonical route. No disk,
// provider, approval notification or delivery work runs under the map lock.
type ConversationManager struct {
	ctx             context.Context
	resolve         SessionResolver
	factory         RuntimeFactory
	delivery        *delivery.Service
	approvals       *approval.ApprovalManager
	reportingPolicy delivery.ReportingPolicy
	idleTimeout     time.Duration
	mu              sync.Mutex // also protects each conversation's admission/lifecycle state
	conversations   map[string]*Conversation
	workers         sync.WaitGroup
	closeErr        error
}

// enqueue owns lookup AND bounded admission. In particular, no caller can keep
// an old pointer and enqueue after its worker has committed to eviction.
func (m *ConversationManager) enqueue(ctx context.Context, msg channel.InboundMessage) (func(context.Context), error) {
	key := channel.BuildSessionKey(msg.Route)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := m.conversations[key]
	if c == nil {
		c = &Conversation{Route: msg.Route, key: key, queue: make(chan channel.InboundMessage, ConversationQueueSize),
			closed: make(chan struct{}), reporter: delivery.NewChannelReporter(m.delivery, msg.Route, m.reportingPolicy)}
		m.conversations[key] = c
		m.workers.Add(1)
		go c.run(m.ctx, m)
	}
	if c.closing {
		return nil, ErrConversationClosing
	}
	// Reserve output order before the worker can consume the message. Neither
	// PrepareAccepted nor discard performs network I/O or waits for delivery.
	notifyAccepted, discardAccepted := c.reporter.PrepareAccepted()
	select {
	case c.queue <- msg:
		c.activitySeq++
		return notifyAccepted, nil
	default:
		discardAccepted()
		return nil, ErrQueueFull
	}
}

func (m *ConversationManager) sessionID(route channel.Route) string {
	key := channel.BuildSessionKey(route)
	m.mu.Lock()
	c := m.conversations[key]
	m.mu.Unlock()
	if c != nil {
		return c.sessionID()
	}
	return key
}

func (m *ConversationManager) recordCloseError(err error) {
	if err == nil {
		return
	}
	log.Printf("[Conversation] session close failed: %v", err)
	m.mu.Lock()
	m.closeErr = errors.Join(m.closeErr, err)
	m.mu.Unlock()
}

func (m *ConversationManager) wait() error {
	// Parent is already canceled. Barrier with enqueue prevents Add/Wait
	// races even for a callback that was entering when shutdown started.
	m.mu.Lock()
	m.mu.Unlock()
	m.workers.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeErr
}
