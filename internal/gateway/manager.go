package gateway

import (
	"context"
	"errors"
	"log"
	"sync"

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
	mu              sync.Mutex
	conversations   map[string]*Conversation
	workers         sync.WaitGroup
	closeErr        error
}

func (m *ConversationManager) getOrCreate(route channel.Route) (*Conversation, error) {
	key := channel.BuildSessionKey(route)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}
	if c := m.conversations[key]; c != nil {
		return c, nil
	}
	c := &Conversation{Route: route, key: key, queue: make(chan channel.InboundMessage, ConversationQueueSize),
		reporter: delivery.NewChannelReporter(m.delivery, route, m.reportingPolicy)}
	m.conversations[key] = c
	m.workers.Add(1)
	go c.run(m.ctx, m)
	return c, nil
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
	log.Printf("[Conversation] 释放 Session writer lock 失败: %v", err)
	m.mu.Lock()
	m.closeErr = errors.Join(m.closeErr, err)
	m.mu.Unlock()
}

func (m *ConversationManager) wait() error {
	// Parent is already canceled. Barrier with getOrCreate prevents Add/Wait
	// races even for a callback that was entering when shutdown started.
	m.mu.Lock()
	m.mu.Unlock()
	m.workers.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeErr
}
