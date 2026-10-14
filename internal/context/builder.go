package context

import (
	"errors"
	"fmt"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

var ErrContextBudgetExceeded = errors.New("ContextBudgetExceeded")
var ErrToolChain = errors.New("cannot preserve legal tool chain")

type BuildResult struct {
	Messages                                         []schema.Message
	WindowTokens, EstimatedTokens, BudgetTokens      int
	TotalMessages, IncludedMessages, OmittedMessages int
	TruncatedToolResults                             int
}
type Builder interface {
	Build(schema.Message, []schema.Message, ModelContextSpec) (BuildResult, error)
}
type ContextBuilder struct {
	Policy    ContextPolicy
	Estimator TokenEstimator
}

func NewContextBuilder(p ContextPolicy) *ContextBuilder {
	return &ContextBuilder{Policy: p.Resolved(), Estimator: HeuristicEstimator{}}
}

// Validate tool units first, then group a whole ordinary-user turn together.
// Selection never cuts through a turn or parallel tool-call chain.
func units(history []schema.Message) ([][]schema.Message, error) {
	out := [][]schema.Message{}
	for i := 0; i < len(history); i++ {
		m := history[i]
		if m.ToolCallID != "" {
			return nil, ErrToolChain
		}
		unit := []schema.Message{m}
		if len(m.ToolCalls) > 0 {
			if m.Role != schema.RoleAssistant {
				return nil, ErrToolChain
			}
			pending := map[string]bool{}
			for _, c := range m.ToolCalls {
				if c.ID == "" || pending[c.ID] {
					return nil, ErrToolChain
				}
				pending[c.ID] = true
			}
			for len(pending) > 0 {
				i++
				if i >= len(history) {
					return nil, ErrToolChain
				}
				r := history[i]
				if !pending[r.ToolCallID] || r.Role != schema.RoleUser || len(r.ToolCalls) > 0 {
					return nil, ErrToolChain
				}
				delete(pending, r.ToolCallID)
				unit = append(unit, r)
			}
		}
		if m.Role == schema.RoleUser || len(out) == 0 {
			out = append(out, unit)
		} else {
			out[len(out)-1] = append(out[len(out)-1], unit...)
		}
	}
	return out, nil
}

const truncationMarker = "\n[tool output truncated for context; full output in session history]\n"

func (b *ContextBuilder) truncate(m schema.Message, limit int) (schema.Message, bool) {
	if m.ToolCallID == "" || b.Estimator.EstimateMessage(m) <= limit {
		return m, false
	}
	// Binary search a rune-safe head/tail length, including marker and envelope.
	runes := []rune(m.Content)
	lo, hi := 0, len(runes)
	best := m
	best.Content = truncationMarker
	for lo <= hi {
		n := (lo + hi) / 2
		candidate := m
		head := (n + 1) / 2
		tail := n / 2
		candidate.Content = string(runes[:head]) + truncationMarker + string(runes[len(runes)-tail:])
		if b.Estimator.EstimateMessage(candidate) <= limit {
			best = candidate
			lo = n + 1
		} else {
			hi = n - 1
		}
	}
	return best, true
}
func (b *ContextBuilder) Build(system schema.Message, history []schema.Message, model ModelContextSpec) (BuildResult, error) {
	window, budget := b.Policy.Budget(model)
	result := BuildResult{WindowTokens: window, BudgetTokens: budget, TotalMessages: len(history) + 1}
	if budget <= 0 {
		return result, fmt.Errorf("%w: invalid window/output policy", ErrContextBudgetExceeded)
	}
	sysTokens := b.Estimator.EstimateMessage(system)
	if sysTokens > budget {
		return result, fmt.Errorf("%w: system prompt exceeds input budget", ErrContextBudgetExceeded)
	}
	copied := cloneMessages(history)
	truncated := make(map[int]bool)
	for i, m := range copied {
		var changed bool
		copied[i], changed = b.truncate(m, b.Policy.Resolved().MaxToolResultTokens)
		if changed {
			truncated[i] = true
		}
	}
	groups, err := units(copied)
	if err != nil {
		return result, err
	}
	// Latest ordinary user request is a hard anchor, never silently truncated.
	anchor := -1
	for i := len(groups) - 1; i >= 0; i-- {
		m := groups[i][0]
		if m.Role == schema.RoleUser && m.ToolCallID == "" {
			anchor = i
			break
		}
	}
	used := sysTokens
	selected := make(map[int]bool)
	if anchor >= 0 {
		used += b.Estimator.EstimateMessages(groups[anchor])
		selected[anchor] = true
	}
	if used > budget {
		return result, fmt.Errorf("%w: current user request exceeds input budget", ErrContextBudgetExceeded)
	}
	for i := len(groups) - 1; i >= 0; i-- {
		if selected[i] {
			continue
		}
		n := b.Estimator.EstimateMessages(groups[i])
		if used+n > budget {
			if i == len(groups)-1 {
				return result, fmt.Errorf("%w: latest complete tool/action unit exceeds input budget", ErrContextBudgetExceeded)
			}
			break
		}
		selected[i] = true
		used += n
	}
	result.Messages = []schema.Message{system}
	offset := 0
	for i, g := range groups {
		if selected[i] {
			result.Messages = append(result.Messages, g...)
			for j := range g {
				if truncated[offset+j] {
					result.TruncatedToolResults++
				}
			}
		}
		offset += len(g)
	}
	result.EstimatedTokens = used
	result.IncludedMessages = len(result.Messages)
	result.OmittedMessages = result.TotalMessages - result.IncludedMessages
	return result, nil
}
