package approval

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

type notifierFunc func(context.Context, Request) error

func (f notifierFunc) RequestApproval(ctx context.Context, req Request) error { return f(ctx, req) }

var quietNotifier = notifierFunc(func(context.Context, Request) error { return nil })

func testRequest(sessionID, taskID string) Request {
	return Request{SessionID: sessionID, TaskID: taskID, ToolName: "test_tool", Args: "test args",
		Route: channel.Route{Channel: "test", AccountID: "default", ChatID: sessionID}}
}

func startApprovalWait(ctx context.Context, manager *ApprovalManager, chatID, taskID string) <-chan ApprovalResult {
	result := make(chan ApprovalResult, 1)
	go func() {
		allowed, reason := manager.Wait(ctx, testRequest(chatID, taskID), quietNotifier)
		result <- ApprovalResult{Allowed: allowed, Reason: reason}
	}()
	return result
}

func waitForPendingApproval(t *testing.T, manager *ApprovalManager, chatID, taskID string) *pendingApproval {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.RLock()
		pending := manager.pendingTasks[Key{SessionID: chatID, TaskID: taskID}]
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

	if manager.Resolve(Key{SessionID: "unrelated-chat", TaskID: "same-task"}, true, "wrong chat") {
		t.Fatal("unrelated chat resolved an approval")
	}
	manager.Resolve(Key{TaskID: "same-task"}, true, "unscoped resolver cannot resolve session-scoped requests")
	if !manager.Resolve(Key{SessionID: "chat-one", TaskID: "same-task"}, true, "approved in first chat") {
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
	if !manager.Resolve(Key{SessionID: "chat-two", TaskID: "same-task"}, false, "rejected in second chat") {
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
			resolved <- manager.Resolve(Key{SessionID: "chat", TaskID: "task"}, true, "approved")
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
	if allowed, reason := manager.Wait(ctx, testRequest("chat", "task"), quietNotifier); allowed || reason == "" {
		t.Fatalf("duplicate wait was not denied: allowed=%v reason=%q", allowed, reason)
	}
	if !manager.Resolve(Key{SessionID: "chat", TaskID: "task"}, true, "original request") {
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
			if manager.Resolve(Key{SessionID: "chat", TaskID: "task"}, true, "too late") {
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

func TestImmediateReplyDuringNotificationIsNotLost(t *testing.T) {
	t.Parallel()
	manager := NewApprovalManager()
	req := testRequest("session", "task")
	notifier := notifierFunc(func(ctx context.Context, actual Request) error {
		if actual != req {
			t.Fatal("approval request changed")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("notifier did not receive approval deadline")
		}
		if !manager.Resolve(Key{SessionID: actual.SessionID, TaskID: actual.TaskID}, true, "immediate reply") {
			t.Fatal("notification ran before registration")
		}
		return nil
	})
	allowed, reason := manager.Wait(context.Background(), req, notifier)
	if !allowed || reason != "immediate reply" {
		t.Fatal("immediate response was lost", reason)
	}
	assertNoPendingApprovals(t, manager)
	if allowed, _ := manager.Wait(context.Background(), req, nil); allowed {
		t.Fatal("missing notifier allowed approval")
	}
	assertNoPendingApprovals(t, manager)
}

func TestApprovalSendFailureCleansUp(t *testing.T) {
	t.Parallel()
	manager := NewApprovalManager()
	notifier := notifierFunc(func(context.Context, Request) error { return errors.New("delivery unavailable") })
	allowed, reason := manager.Wait(context.Background(), testRequest("chat", "task"), notifier)
	if allowed || !strings.Contains(reason, "发送审批请求失败") {
		t.Fatalf("send failure did not reject approval: allowed=%v reason=%q", allowed, reason)
	}
	assertNoPendingApprovals(t, manager)
	if manager.Resolve(Key{SessionID: "chat", TaskID: "task"}, true, "too late") {
		t.Fatal("failed notification left a resolvable approval")
	}
}
