package gateway

import (
	"context"
	"log"
	"time"
)

// run alone owns the timer and runtime teardown. No idle countdown overlaps
// process, even when Run is blocked in a tool, subagent or human approval.
func (c *Conversation) run(ctx context.Context, m *ConversationManager) {
	defer m.workers.Done()
	evicted := false
	defer func() { c.closeRuntime(m, evicted) }()

	var timer *time.Timer
	if m.idleTimeout > 0 {
		timer = time.NewTimer(m.idleTimeout)
		stopIdleTimer(timer)
		defer stopIdleTimer(timer)
	}
	for {
		if ctx.Err() != nil {
			return
		}
		var tick <-chan time.Time
		var expectedSeq uint64
		if timer != nil {
			if seq, idle := m.idleSequence(c); idle {
				expectedSeq = seq
				stopIdleTimer(timer)
				timer.Reset(m.idleTimeout)
				tick = timer.C
			}
		}
		select {
		case <-ctx.Done():
			return
		case msg := <-c.queue:
			stopIdleTimer(timer)
			c.process(ctx, m, msg)
		case <-tick:
			if m.beginEviction(c, expectedSeq) {
				evicted = true
				log.Printf("[Conversation] idle eviction started channel=%s reason=idle_timeout", c.Route.Channel)
				return
			}
		}
	}
}

func stopIdleTimer(timer *time.Timer) {
	if timer != nil && !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

// idleSequence is called only by the worker between messages. Admission may
// occur immediately afterwards; beginEviction must revalidate this snapshot.
func (m *ConversationManager) idleSequence(c *Conversation) (uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return c.activitySeq, m.conversations[c.key] == c && !c.closing && len(c.queue) == 0
}

func (m *ConversationManager) beginEviction(c *Conversation, expectedSeq uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil || m.conversations[c.key] != c || c.closing || c.activitySeq != expectedSeq || len(c.queue) != 0 {
		return false
	}
	// Keep the entry until Session.Close has released the writer. Admission
	// returns a transient error instead of using this queue or a second runtime.
	c.closing = true
	return true
}

func (c *Conversation) closeRuntime(m *ConversationManager, evicted bool) {
	m.mu.Lock()
	c.closing = true // Also covers cancellation, which prevents new admission.
	m.mu.Unlock()
	if c.session != nil {
		// Never hold the manager or Gateway admission lock across disk/lock I/O.
		m.recordCloseError(c.session.Close())
	}
	count := m.finishEviction(c)
	if evicted {
		log.Printf("[Conversation] idle eviction completed channel=%s active=%d", c.Route.Channel, count)
	}
}

// Only the exiting worker calls finishEviction, exactly once, after Close.
// Shutdown waits on workers, including ones already removed by idle eviction.
func (m *ConversationManager) finishEviction(c *Conversation) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conversations[c.key] == c {
		delete(m.conversations, c.key)
	}
	close(c.closed)
	return len(m.conversations)
}
