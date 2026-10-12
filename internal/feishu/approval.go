package feishu

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sync"
	"time"
)

// ApprovalResult 审批结果包
type ApprovalResult struct {
	Allowed bool
	Reason  string
}

// ApprovalManager 统一管理当前正在等待人类审批的任务
type ApprovalManager struct {
	mu           sync.RWMutex
	pendingTasks map[approvalKey]*pendingApproval
}

type approvalKey struct {
	chatID string
	taskID string
}

type pendingApproval struct {
	ctx    context.Context
	result chan ApprovalResult
}

const defaultApprovalTimeout = 5 * time.Minute

func NewApprovalManager() *ApprovalManager {
	return &ApprovalManager{pendingTasks: make(map[approvalKey]*pendingApproval)}
}

// 全局单例，方便在 Registry Middleware 和 Feishu Webhook 之间共享状态。
var GlobalApprovalMgr = NewApprovalManager()

// WaitForApproval 保留旧的、无聊天范围的调用方式；飞书调用应使用带 chatID 的版本。
func (m *ApprovalManager) WaitForApproval(taskID, toolName, args string, reporter *FeishuReporter) (bool, string) {
	return m.WaitForApprovalContext(context.Background(), "", taskID, toolName, args, reporter)
}

// WaitForApprovalContext 仅接受当前聊天的审批，取消或超时后默认拒绝操作。
// 即使调用方未设置截止时间，等待也不会超过五分钟。
func (m *ApprovalManager) WaitForApprovalContext(ctx context.Context, chatID, taskID, toolName, args string, reporter *FeishuReporter) (bool, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, defaultApprovalTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return false, fmt.Sprintf("审批已取消或超时: %v", err)
	}

	key := approvalKey{chatID: chatID, taskID: taskID}
	pending := &pendingApproval{ctx: ctx, result: make(chan ApprovalResult, 1)}

	m.mu.Lock()
	if m.pendingTasks == nil {
		m.pendingTasks = make(map[approvalKey]*pendingApproval)
	}
	if _, exists := m.pendingTasks[key]; exists {
		m.mu.Unlock()
		return false, "当前聊天中已有相同任务 ID 等待审批"
	}
	m.pendingTasks[key] = pending
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		// 已完成的任务 ID 可以复用；旧等待者不能删除新的审批请求。
		if m.pendingTasks[key] == pending {
			delete(m.pendingTasks, key)
		}
		m.mu.Unlock()
	}()

	// 注册后再发送通知，以便立即到达的审批回复也能被接收。
	noticeMsg := fmt.Sprintf(`⚠️ **高危操作审批请求**
Agent 试图执行以下动作:
- 工具: %s
- 参数: %s

任务 ID: **%s**

👉 请在此消息下方回复 "approve %s" 或 "reject %s" 来决定是否放行。`, toolName, args, taskID, taskID, taskID)

	if reporter != nil {
		if err := reporter.sendMsgContext(ctx, noticeMsg); err != nil {
			return false, fmt.Sprintf("发送审批请求失败: %v", err)
		}
	} else {
		// 回退到终端打印 (兼容本地 CLI 模式)
		fmt.Printf("\n\033[31m[需要审批 TaskID: %s]\033[0m %s\n", taskID, noticeMsg)
	}

	log.Printf("[Approval] 已发送审批请求 (TaskID: %s)，等待审批结果\n", taskID)
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

// ResolveApproval 只解析旧接口注册的无聊天范围请求。
func (m *ApprovalManager) ResolveApproval(taskID string, allowed bool, reason string) {
	m.ResolveApprovalForChat("", taskID, allowed, reason)
}

// ResolveApprovalForChat 原子地消费当前聊天中的请求，重复或过期回复不会阻塞。
func (m *ApprovalManager) ResolveApprovalForChat(chatID, taskID string, allowed bool, reason string) bool {
	key := approvalKey{chatID: chatID, taskID: taskID}
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
		log.Printf("[Approval] 收到审批结果 (TaskID: %s, Allowed: %v)\n", taskID, allowed)
		return true
	default:
		return false
	}
}

// IsDangerousCommand 简单的正则检查黑名单，判断该工具调用是否需要审批
func IsDangerousCommand(toolName string, args string) bool {
	// 对于纯读取的工具，默认 YOLO 模式，全部放行
	if toolName != "bash" && toolName != "write_file" && toolName != "edit_file" {
		return false
	}

	// 针对 bash 的高危模式匹配
	if toolName == "bash" {
		dangerousPatterns := []string{
			`rm\s+-r`, // 级联删除
			`sudo\s+`, // 提权
			`drop\s+`, // 数据库删除
			`>.*\.go`, // 恶意覆盖源代码
		}
		for _, p := range dangerousPatterns {
			matched, _ := regexp.MatchString(p, args)
			if matched {
				return true
			}
		}
	}
	return false
}
