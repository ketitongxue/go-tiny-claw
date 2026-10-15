// Package approval owns conversation-scoped human approval, without platform APIs.
package approval

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

type ApprovalResult struct {
	Allowed bool
	Reason  string
}

type Key struct {
	SessionID string
	TaskID    string
}

type Request struct {
	SessionID string
	Route     channel.Route
	TaskID    string
	ToolName  string
	Args      string
}

type Notifier interface {
	RequestApproval(context.Context, Request) error
}

type pendingApproval struct {
	ctx    context.Context
	result chan ApprovalResult
}

type ApprovalManager struct {
	mu           sync.RWMutex
	pendingTasks map[Key]*pendingApproval
}

const defaultApprovalTimeout = 5 * time.Minute

func NewApprovalManager() *ApprovalManager {
	return &ApprovalManager{pendingTasks: make(map[Key]*pendingApproval)}
}

// Wait registers before notification so immediate replies cannot be lost.
// Cancellation, timeout, duplicate IDs and notification failure deny by default.
func (m *ApprovalManager) Wait(ctx context.Context, req Request, notifier Notifier) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, defaultApprovalTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return false, fmt.Sprintf("审批已取消或超时: %v", err)
	}
	if req.SessionID == "" || req.TaskID == "" {
		return false, "审批需要 Session ID 和任务 ID"
	}
	if err := req.Route.Validate(); err != nil {
		return false, fmt.Sprintf("审批地址无效: %v", err)
	}
	if m == nil || notifier == nil {
		return false, "审批通道不可用"
	}
	key := Key{SessionID: req.SessionID, TaskID: req.TaskID}
	pending := &pendingApproval{ctx: ctx, result: make(chan ApprovalResult, 1)}
	m.mu.Lock()
	if m.pendingTasks == nil {
		m.pendingTasks = make(map[Key]*pendingApproval)
	}
	if _, exists := m.pendingTasks[key]; exists {
		m.mu.Unlock()
		return false, "当前会话中已有相同任务 ID 等待审批"
	}
	m.pendingTasks[key] = pending
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		// A completed task ID can be reused; an old waiter cannot remove it.
		if m.pendingTasks[key] == pending {
			delete(m.pendingTasks, key)
		}
		m.mu.Unlock()
	}()
	if err := notifier.RequestApproval(ctx, req); err != nil {
		return false, fmt.Sprintf("发送审批请求失败: %v", err)
	}
	log.Printf("[Approval] 已发送审批请求 (TaskID: %s)，等待审批结果", req.TaskID)
	select {
	case <-ctx.Done():
		return false, fmt.Sprintf("审批已取消或超时: %v", ctx.Err())
	case result := <-pending.result:
		if err := ctx.Err(); err != nil {
			return false, fmt.Sprintf("审批已取消或超时: %v", err)
		}
		return result.Allowed, result.Reason
	}
}

// Resolve atomically consumes only a request in this durable session. Duplicate
// or late replies never block and cannot resolve another conversation's task.
func (m *ApprovalManager) Resolve(key Key, allowed bool, reason string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, exists := m.pendingTasks[key]
	if !exists {
		return false
	}
	delete(m.pendingTasks, key)
	if pending.ctx.Err() != nil {
		return false
	}
	select {
	case pending.result <- ApprovalResult{Allowed: allowed, Reason: reason}:
		log.Printf("[Approval] 收到审批结果 (TaskID: %s, Allowed: %v)", key.TaskID, allowed)
		return true
	default:
		return false
	}
}
