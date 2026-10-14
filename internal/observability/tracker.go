package observability

import (
	"context"
	"log"
	"time"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// EstimateUsage never prices a subscription, or substitutes zero for unknown.
func EstimateUsage(model provider.ModelInfo, usage schema.Usage) provider.UsageBilling {
	mode := model.BillingMode
	if mode == "" {
		mode = provider.BillingUnknown
	}
	billing := provider.UsageBilling{Mode: mode}
	if mode != provider.BillingAPI || model.Pricing == nil || model.Pricing.Currency == "" {
		return billing
	}
	p := model.Pricing
	prompt := max(0, usage.PromptTokens)
	output := max(0, usage.CompletionTokens)
	cached := max(0, min(usage.CachedPromptTokens, prompt))
	billing.CostKnown = true
	billing.Currency = p.Currency
	billing.EstimatedCost = (float64(prompt-cached)*p.Input + float64(cached)*p.CachedInput + float64(output)*p.Output) / 1e6
	return billing
}

type CostTracker struct {
	nextProvider provider.LLMProvider
	model        provider.ModelInfo
	session      *ctxpkg.Session
}

func NewCostTracker(next provider.LLMProvider, model provider.ModelInfo, session *ctxpkg.Session) *CostTracker {
	if session != nil {
		session.SetModelInfo(model)
	}
	return &CostTracker{nextProvider: next, model: model, session: session}
}
func (t *CostTracker) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	start := time.Now()
	msg, err := t.nextProvider.Generate(ctx, msgs, tools)
	latency := time.Since(start)
	if err != nil {
		if t.session != nil {
			t.session.MarkCostUnknown()
		}
		log.Printf("[Tracker] API 调用失败 | 耗时: %v", latency)
		return msg, err
	}
	if msg == nil || msg.Usage == nil {
		if t.session != nil {
			t.session.MarkCostUnknown()
		}
		log.Printf("[Tracker] API 调用完成但未返回 Usage | 耗时: %v | Cost: unknown", latency)
		return msg, nil
	}
	usage := *msg.Usage
	billing := EstimateUsage(t.model, usage)
	log.Printf("[Tracker] 模型: %s | 耗时: %v | 输入: %d tk (缓存命中: %d tk) | 输出: %d tk | %s", t.model.Ref(), latency, usage.PromptTokens, max(0, min(usage.CachedPromptTokens, usage.PromptTokens)), usage.CompletionTokens, billing)
	if t.session != nil {
		t.session.RecordUsage(max(0, usage.PromptTokens), max(0, usage.CompletionTokens), billing)
		log.Printf("[Tracker] 会话 %s | %s", t.session.ID, t.session.UsageSnapshot().Billing())
	}
	return msg, nil
}
