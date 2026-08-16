package context

import (
	"sync"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// Session 代表了一次持续的人机交互过程。它负责维护该会话的完整历史。
type Session struct {
	ID        string
	WorkDir   string // 该会话绑定的物理工作区
	CreatedAt time.Time
	UpdatedAt time.Time

	// 【新增】用于统计该 Session 累计消耗的资源
	TotalPromptTokens     int
	TotalCompletionTokens int
	TotalCostCNY          float64

	// 存放此 Session 中所有的用户输入、大模型回复和工具调用结果
	history []schema.Message
	mu      sync.RWMutex // 读写锁，防止并发读写历史时发生 Data Race
}

// RecordUsage 是一个给外部 Tracker 调用的辅助方法，用于累加账单
func (s *Session) RecordUsage(prompt int, completion int, cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalPromptTokens += prompt
	s.TotalCompletionTokens += completion
	s.TotalCostCNY += cost
}

func NewSession(id string, workDir string) *Session {
	return &Session{
		ID:        id,
		WorkDir:   workDir,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		history:   make([]schema.Message, 0),
	}
}

// Append 线程安全地向 Session 中追加消息
func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()

	// 【持久化预留点】：在真实的工业级实现中（如 Claude Code），
	// 我们会在这里将 s.history 以 JSONL 的格式 Append 到 workDir/.claw/sessions/xxx.jsonl 中。
	// s.SaveToDisk()
}

// GetWorkingMemory 是驾驭工程的核心！
// 它不返回全量历史，而是从后往前截取最近的 N 条消息，形成 Agent 的“短期工作记忆”。
func (s *Session) GetWorkingMemory(limit int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.history)
	start := 0
	if total > limit && limit > 0 {
		start = total - limit
		// 工具调用链必须从普通 User 消息开始：如果窗口从 Assistant
		// tool_calls 或 ToolResult 中间切入，兼容 API 会拒绝整个 messages 数组。
		// 允许窗口比 limit 多一小段，只为补回这个工具调用轮次的起点。
		start = s.findConversationStart(start)
	}

	// 需要深拷贝顶层切片，避免调用方修改 Session 的底层历史数组。
	res := make([]schema.Message, total-start)
	copy(res, s.history[start:])
	return res
}

// findConversationStart 将工作记忆的起点回溯到最近一条普通用户消息。
// 这会同时覆盖两种非法截断：从 assistant tool_calls 开始，或从 tool result 开始。
func (s *Session) findConversationStart(candidate int) int {
	if candidate <= 0 {
		return 0
	}

	for i := candidate; i >= 0; i-- {
		if s.history[i].Role == schema.RoleUser && s.history[i].ToolCallID == "" {
			return i
		}
	}

	// 没有普通 User 消息时保留完整历史，避免继续制造孤儿工具消息。
	return 0
}

// ==========================================
// 全局 Session Manager: 用于多用户/多终端隔离
// ==========================================

type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

var GlobalSessionMgr = &SessionManager{
	sessions: make(map[string]*Session),
}

// GetOrCreate 获取或创建一个会话
func (sm *SessionManager) GetOrCreate(id string, workDir string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sess, exists := sm.sessions[id]; exists {
		return sess
	}
	sess := NewSession(id, workDir)
	sm.sessions[id] = sess
	return sess
}
