package feishu

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func startApprovalWait(ctx context.Context, manager *ApprovalManager, chatID, taskID string) <-chan ApprovalResult {
	result := make(chan ApprovalResult, 1)
	go func() {
		allowed, reason := manager.WaitForApprovalContext(ctx, chatID, taskID, "test_tool", "test args", nil)
		result <- ApprovalResult{Allowed: allowed, Reason: reason}
	}()
	return result
}

func waitForPendingApproval(t *testing.T, manager *ApprovalManager, chatID, taskID string) *pendingApproval {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.RLock()
		pending := manager.pendingTasks[approvalKey{chatID: chatID, taskID: taskID}]
		manager.mu.RUnlock()
		if pending != nil {
			return pending
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("approval %q in chat %q was not registered", taskID, chatID)
	return nil
}

func receiveApproval(t *testing.T, result <-chan ApprovalResult) ApprovalResult {
	t.Helper()
	select {
	case approval := <-result:
		return approval
	case <-time.After(2 * time.Second):
		t.Fatal("approval wait did not finish")
		return ApprovalResult{}
	}
}

func assertNoPendingApprovals(t *testing.T, manager *ApprovalManager) {
	t.Helper()
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if len(manager.pendingTasks) != 0 {
		t.Fatalf("pending approvals were not cleaned up: %d", len(manager.pendingTasks))
	}
}

func TestApprovalIsolatedByChat(t *testing.T) {
	t.Parallel()
	manager := NewApprovalManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first := startApprovalWait(ctx, manager, "chat-one", "same-task")
	second := startApprovalWait(ctx, manager, "chat-two", "same-task")
	waitForPendingApproval(t, manager, "chat-one", "same-task")
	waitForPendingApproval(t, manager, "chat-two", "same-task")

	if manager.ResolveApprovalForChat("unrelated-chat", "same-task", true, "wrong chat") {
		t.Fatal("unrelated chat resolved an approval")
	}
	manager.ResolveApproval("same-task", true, "legacy resolver cannot resolve chat-scoped requests")
	if !manager.ResolveApprovalForChat("chat-one", "same-task", true, "approved in first chat") {
		t.Fatal("first chat approval was not resolved")
	}
	if result := receiveApproval(t, first); !result.Allowed || result.Reason != "approved in first chat" {
		t.Fatalf("unexpected first chat result: %+v", result)
	}
	select {
	case result := <-second:
		t.Fatalf("first chat resolved the second chat request: %+v", result)
	default:
	}
	if !manager.ResolveApprovalForChat("chat-two", "same-task", false, "rejected in second chat") {
		t.Fatal("second chat rejection was not resolved")
	}
	if result := receiveApproval(t, second); result.Allowed || result.Reason != "rejected in second chat" {
		t.Fatalf("unexpected second chat result: %+v", result)
	}
	assertNoPendingApprovals(t, manager)
}

func TestDuplicateApprovalResolutionsDoNotBlock(t *testing.T) {
	t.Parallel()
	manager := NewApprovalManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := startApprovalWait(ctx, manager, "chat", "task")
	waitForPendingApproval(t, manager, "chat", "task")

	const resolvers = 32
	resolved := make(chan bool, resolvers)
	var group sync.WaitGroup
	for range resolvers {
		group.Add(1)
		go func() {
			defer group.Done()
			resolved <- manager.ResolveApprovalForChat("chat", "task", true, "approved")
		}()
	}
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("duplicate approval resolution blocked")
	}
	close(resolved)
	successes := 0
	for success := range resolved {
		if success {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("resolved %d times, want exactly one", successes)
	}
	if approval := receiveApproval(t, result); !approval.Allowed {
		t.Fatalf("approval unexpectedly rejected: %+v", approval)
	}
	assertNoPendingApprovals(t, manager)
}

func TestDuplicateApprovalWaitPreservesOriginal(t *testing.T) {
	t.Parallel()
	manager := NewApprovalManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	original := startApprovalWait(ctx, manager, "chat", "task")
	waitForPendingApproval(t, manager, "chat", "task")
	if allowed, reason := manager.WaitForApprovalContext(ctx, "chat", "task", "test_tool", "test args", nil); allowed || reason == "" {
		t.Fatalf("duplicate wait was not denied: allowed=%v reason=%q", allowed, reason)
	}
	if !manager.ResolveApprovalForChat("chat", "task", true, "original request") {
		t.Fatal("duplicate wait replaced the original request")
	}
	if result := receiveApproval(t, original); !result.Allowed || result.Reason != "original request" {
		t.Fatalf("unexpected original result: %+v", result)
	}
	assertNoPendingApprovals(t, manager)
}

func TestApprovalCancellationAndTimeoutCleanUp(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"canceled", "timeout", "already canceled"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			manager := NewApprovalManager()
			ctx, cancel := context.WithCancel(context.Background())
			if name == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			t.Cleanup(cancel)
			if name == "already canceled" {
				cancel()
			}
			result := startApprovalWait(ctx, manager, "chat", "task")
			if name != "already canceled" {
				waitForPendingApproval(t, manager, "chat", "task")
			}
			if name == "canceled" {
				cancel()
			}
			approval := receiveApproval(t, result)
			if approval.Allowed || !strings.Contains(approval.Reason, ctx.Err().Error()) {
				t.Fatalf("canceled approval was not rejected with context reason: %+v", approval)
			}
			assertNoPendingApprovals(t, manager)
			if manager.ResolveApprovalForChat("chat", "task", true, "too late") {
				t.Fatal("approval resolved after cancellation")
			}
		})
	}
}

func TestApprovalHasFiniteDefaultDeadline(t *testing.T) {
	t.Parallel()
	manager := NewApprovalManager()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := startApprovalWait(ctx, manager, "chat", "task")
	pending := waitForPendingApproval(t, manager, "chat", "task")
	deadline, ok := pending.ctx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining <= 0 || remaining > defaultApprovalTimeout {
		t.Fatalf("unexpected default deadline: present=%v remaining=%v", ok, remaining)
	}
	cancel()
	if approval := receiveApproval(t, result); approval.Allowed {
		t.Fatal("canceled default wait was allowed")
	}
	assertNoPendingApprovals(t, manager)
}

func TestApprovalSendFailureCleansUp(t *testing.T) {
	t.Parallel()
	manager := NewApprovalManager()
	// An unconfigured reporter fails locally without making any network request.
	reporter := &FeishuReporter{chatId: "chat"}
	allowed, reason := manager.WaitForApprovalContext(context.Background(), "chat", "task", "test_tool", "test args", reporter)
	if allowed || !strings.Contains(reason, "发送审批请求失败") {
		t.Fatalf("send failure did not reject approval: allowed=%v reason=%q", allowed, reason)
	}
	assertNoPendingApprovals(t, manager)
	if manager.ResolveApprovalForChat("chat", "task", true, "too late") {
		t.Fatal("failed notification left a resolvable approval")
	}
}
