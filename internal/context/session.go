package context

import (
	stdctx "context"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"sync"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
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
	TotalEstimatedCost    float64 // consult CostKnown and Currency before displaying
	ProviderID            string
	ModelID               string
	AuthType              string
	BillingMode           provider.BillingMode
	CostKnown             bool
	Currency              string

	// 存放此 Session 中所有的用户输入、大模型回复和工具调用结果
	store   sessionstore.Store
	history []schema.Message
	mu      sync.RWMutex // 读写锁，防止并发读写历史时发生 Data Race
}

type UsageTotals struct {
	PromptTokens, CompletionTokens int
	EstimatedCost                  float64
	CostKnown                      bool
	Currency                       string
	BillingMode                    provider.BillingMode
}

func (u UsageTotals) Billing() provider.UsageBilling {
	return provider.UsageBilling{Mode: u.BillingMode, EstimatedCost: u.EstimatedCost, CostKnown: u.CostKnown, Currency: u.Currency}
}
func (s *Session) SetModelInfo(model provider.ModelInfo) {
	// Durable model identity is pinned at creation, not reselected by trackers.
	if s.store != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ProviderID = model.ProviderID
	s.ModelID = model.ID
	s.BillingMode = model.BillingMode
	if s.BillingMode == "" {
		s.BillingMode = provider.BillingUnknown
	}
	s.CostKnown = model.BillingMode == provider.BillingAPI && model.Pricing != nil && model.Pricing.Currency != ""
	if s.CostKnown {
		s.Currency = model.Pricing.Currency
	}
}
func (s *Session) SetAuthType(authType string) {
	if s.store != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AuthType = authType
}
func (s *Session) ModelAttributes() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]string{"provider_id": s.ProviderID, "model_id": s.ModelID, "billing_mode": string(s.BillingMode), "auth_type": s.AuthType}
}
func (s *Session) RecordUsage(prompt int, completion int, billing provider.UsageBilling) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.usageLocked()
	u.PromptTokens += prompt
	u.CompletionTokens += completion
	u.CostKnown = u.CostKnown && billing.CostKnown
	if billing.CostKnown {
		u.EstimatedCost += billing.EstimatedCost
	}
	e := sessionstore.NewEvent("usage")
	e.Usage = &u
	if s.store != nil {
		if err := s.store.Append(stdctx.Background(), s.ID, e); err != nil {
			return err
		}
	}
	s.TotalPromptTokens = u.PromptTokens
	s.TotalCompletionTokens = u.CompletionTokens
	s.TotalEstimatedCost = u.EstimatedCost
	s.CostKnown = u.CostKnown
	s.UpdatedAt = e.Timestamp
	return nil
}
func (s *Session) MarkCostUnknown() error { return s.RecordUsage(0, 0, provider.UsageBilling{}) }
func (s *Session) UsageSnapshot() UsageTotals {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return UsageTotals{PromptTokens: s.TotalPromptTokens, CompletionTokens: s.TotalCompletionTokens, EstimatedCost: s.TotalEstimatedCost, CostKnown: s.CostKnown, Currency: s.Currency, BillingMode: s.BillingMode}
}

// UsageTotals returns a consistent snapshot while parallel subagents record usage.
func (s *Session) UsageTotals() (prompt int, completion int, cost float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalPromptTokens, s.TotalCompletionTokens, s.TotalEstimatedCost
}

func NewSession(id string, workDir string) *Session {
	return &Session{
		ID:          id,
		WorkDir:     workDir,
		BillingMode: provider.BillingUnknown,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		history:     make([]schema.Message, 0),
	}
}

// Append 线程安全地向 Session 中追加消息
func (s *Session) Append(msgs ...schema.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]sessionstore.Event, len(msgs))
	for i := range msgs {
		events[i] = sessionstore.NewEvent("message")
		events[i].Message = &msgs[i]
	}
	if s.store != nil {
		if err := s.store.Append(stdctx.Background(), s.ID, events...); err != nil {
			return err
		}
	}
	// Own the data: tool arguments and usage pointers must not alias callers.
	copyMsgs := cloneMessages(msgs)
	s.history = append(s.history, copyMsgs...)
	s.UpdatedAt = time.Now()
	return nil
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
	copy(res, cloneMessages(s.history[start:]))
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
