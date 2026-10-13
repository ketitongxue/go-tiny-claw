// internal/observability/tracker.go
package observability

import (
	"context"
	"log"
	"strings"
	"time"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// PricingModel 按官方价格截图配置，单位为人民币元/百万 Tokens。
// 缓存存储限时免费，当前不计费；缓存命中使用 CachedInputPrice。
var PricingModel = map[string]struct {
	InputPrice       float64
	OutputPrice      float64
	CachedInputPrice float64
}{
	"glm-5.3":        {InputPrice: 8, OutputPrice: 28, CachedInputPrice: 2},
	"glm-5.3-flash":  {InputPrice: 0.8, OutputPrice: 2.8, CachedInputPrice: 0.23},
	"glm-5.3-flashx": {InputPrice: 2, OutputPrice: 7, CachedInputPrice: 0.57},
}

// CostTracker 是一个包装了真实 LLMProvider 的装饰器中间件
type CostTracker struct {
	nextProvider provider.LLMProvider
	modelName    string
	session      *ctxpkg.Session // 当前所属的会话 (用于累加总成本)
}

// NewCostTracker 构造函数：接收一个现有的 Provider，返回一个被监控的 Provider
func NewCostTracker(next provider.LLMProvider, modelName string, session *ctxpkg.Session) *CostTracker {
	return &CostTracker{
		nextProvider: next,
		modelName:    modelName,
		session:      session,
	}
}

// Generate 实现了 LLMProvider 接口！这意味着它可以被无缝注入到 Main Loop 中。
func (t *CostTracker) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {

	// 1. 记录请求发起的时刻
	startTime := time.Now()

	// 2. 调用真实的底层大模型去执行耗时的网络请求
	respMsg, err := t.nextProvider.Generate(ctx, msgs, availableTools)

	// 3. 计算耗时
	latency := time.Since(startTime)

	// 如果报错了，只打印报错时间，不计费
	if err != nil {
		log.Printf("[Tracker] ❌ API 调用失败，耗时: %v\n", latency)
		return respMsg, err
	}

	// 4. 解析 Token 并计算成本
	if respMsg != nil && respMsg.Usage != nil {
		promptTokens := respMsg.Usage.PromptTokens
		completionTokens := respMsg.Usage.CompletionTokens
		// 缓存命中属于输入总量，且不能为负数或超过输入总量。
		cachedPromptTokens := max(0, min(respMsg.Usage.CachedPromptTokens, promptTokens))

		var cost float64
		if price, exists := PricingModel[strings.ToLower(strings.TrimSpace(t.modelName))]; exists {
			// 人民币费用 = (未命中输入 * 输入单价 + 缓存命中 * 命中单价 + 输出 * 输出单价) / 1M。
			cost = (float64(promptTokens-cachedPromptTokens)*price.InputPrice +
				float64(cachedPromptTokens)*price.CachedInputPrice +
				float64(completionTokens)*price.OutputPrice) / 1e6
		} else {
			log.Printf("[Tracker] ⚠️ 模型 %q 未配置人民币价格，费用未估算（0 不代表免费）\n", t.modelName)
		}

		// 5. 打印精美的仪表盘日志
		log.Printf("[Tracker] 📊 API 调用完成 | 耗时: %v | 输入: %d tk (缓存命中: %d tk) | 输出: %d tk | 估算费用 (CNY): ¥%.6f\n",
			latency, promptTokens, cachedPromptTokens, completionTokens, cost)

		// 6. 将账单累加到当前的 Session 中，供人类后续随时查询
		if t.session != nil {
			t.session.RecordUsage(promptTokens, completionTokens, cost)
			_, _, totalCost := t.session.UsageTotals()
			log.Printf("[Tracker] 💰 当前会话 (%s) 累计花费: ¥%.6f\n", t.session.ID, totalCost)
		}
	} else {
		log.Printf("[Tracker] ⚠️ API 调用完成，但未返回 Usage 数据 | 耗时: %v\n", latency)
	}

	return respMsg, nil
}
