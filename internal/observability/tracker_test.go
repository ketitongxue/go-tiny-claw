package observability

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type providerFunc func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f providerFunc) Generate(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, msgs, defs)
}

func TestPricingModelUsesOfficialCNYPrices(t *testing.T) {
	for _, tc := range []struct {
		model                 string
		input, output, cached float64
	}{
		{"glm-5.3", 8, 28, 2},
		{"glm-5.3-flash", 0.8, 2.8, 0.23},
		{"glm-5.3-flashx", 2, 7, 0.57},
	} {
		t.Run(tc.model, func(t *testing.T) {
			price, ok := PricingModel[tc.model]
			if !ok || price.InputPrice != tc.input || price.OutputPrice != tc.output || price.CachedInputPrice != tc.cached {
				t.Fatalf("price = %+v, configured = %v; want CNY/1M tokens: input %g, output %g, cached %g", price, ok, tc.input, tc.output, tc.cached)
			}
		})
	}
}

func TestCostTrackerEstimatesCNYCosts(t *testing.T) {
	for _, tc := range []struct {
		name, model                string
		prompt, completion, cached int
		wantCost                   float64
	}{
		{name: "GLM-5.3 regular", model: "glm-5.3", prompt: 1_000_000, completion: 1_000_000, wantCost: 36},
		{name: "Flash regular", model: "glm-5.3-flash", prompt: 1_000_000, completion: 1_000_000, wantCost: 3.6},
		{name: "FlashX regular", model: "glm-5.3-flashx", prompt: 1_000_000, completion: 1_000_000, wantCost: 9},
		{name: "GLM-5.3 partial cache", model: "glm-5.3", prompt: 1_000_000, completion: 100_000, cached: 250_000, wantCost: 9.3},
		{name: "Flash partial cache", model: "glm-5.3-flash", prompt: 1_000_000, completion: 100_000, cached: 250_000, wantCost: 0.9375},
		{name: "FlashX partial cache", model: "glm-5.3-flashx", prompt: 1_000_000, completion: 100_000, cached: 250_000, wantCost: 2.3425},
		{name: "GLM-5.3 full cache", model: "glm-5.3", prompt: 1_000_000, cached: 1_000_000, wantCost: 2},
		{name: "Flash full cache", model: "glm-5.3-flash", prompt: 1_000_000, cached: 1_000_000, wantCost: 0.23},
		{name: "FlashX full cache", model: "glm-5.3-flashx", prompt: 1_000_000, cached: 1_000_000, wantCost: 0.57},
		{name: "small token counts", model: "glm-5.3", prompt: 100, completion: 20, cached: 25, wantCost: 0.00121},
		{name: "output only", model: "glm-5.3", completion: 100, wantCost: 0.0028},
		{name: "negative cache count", model: "glm-5.3", prompt: 100, completion: 20, cached: -5, wantCost: 0.00136},
		{name: "cache exceeds prompt", model: "glm-5.3", prompt: 100, completion: 20, cached: 200, wantCost: 0.00076},
		{name: "case insensitive model", model: " GLM-5.3-FlashX ", prompt: 1_000_000, completion: 100_000, cached: 250_000, wantCost: 2.3425},
		{name: "unknown model", model: "unpriced-model", prompt: 100, completion: 20, cached: 25},
		{name: "no assumed legacy price", model: "glm-5.2", prompt: 100, completion: 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := schema.Usage{PromptTokens: tc.prompt, CompletionTokens: tc.completion, CachedPromptTokens: tc.cached}
			msg := &schema.Message{Role: schema.RoleAssistant, Usage: &usage}
			p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				return msg, nil
			})
			session := ctxpkg.NewSession(tc.name, t.TempDir())
			got, err := NewCostTracker(p, tc.model, session).Generate(context.Background(), nil, nil)
			if err != nil || got != msg || got.Usage.CachedPromptTokens != tc.cached {
				t.Fatalf("provider response was altered: %v, %v", got, err)
			}
			prompt, completion, cost := session.UsageTotals()
			if prompt != tc.prompt || completion != tc.completion || math.Abs(cost-tc.wantCost) > 1e-12 {
				t.Fatalf("usage = (%d, %d, %g CNY), want (%d, %d, %g CNY)", prompt, completion, cost, tc.prompt, tc.completion, tc.wantCost)
			}
		})
	}
}

func TestCostTrackerKeepsConcurrentChatUsageIsolated(t *testing.T) {
	p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return &schema.Message{Role: schema.RoleAssistant, Content: "answer", Usage: &schema.Usage{PromptTokens: 100, CompletionTokens: 20, CachedPromptTokens: 25}}, nil
	})
	chatA := ctxpkg.NewSession("chat-a", t.TempDir())
	chatB := ctxpkg.NewSession("chat-b", t.TempDir())
	trackerA := NewCostTracker(p, "glm-5.3", chatA)
	trackerB := NewCostTracker(p, "glm-5.3", chatB)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg, err := trackerA.Generate(context.Background(), nil, nil)
			if err != nil || msg.Content != "answer" {
				t.Errorf("tracker did not forward response: %v", err)
			}
			chatA.UsageTotals()
		}()
	}
	for range 15 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := trackerB.Generate(context.Background(), nil, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, tc := range []struct {
		session *ctxpkg.Session
		calls   int
	}{{chatA, 40}, {chatB, 15}} {
		prompt, completion, cost := tc.session.UsageTotals()
		wantCost := float64(tc.calls) * 0.00121
		if prompt != tc.calls*100 || completion != tc.calls*20 || math.Abs(cost-wantCost) > 1e-12 {
			t.Errorf("%s usage = (%d, %d, %g), want (%d, %d, %g)", tc.session.ID, prompt, completion, cost, tc.calls*100, tc.calls*20, wantCost)
		}
	}
}

func TestCostTrackerDoesNotChargeFailedOrUnmeteredCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  *schema.Message
		err  error
	}{
		{name: "failed", msg: &schema.Message{Usage: &schema.Usage{PromptTokens: 100}}, err: errors.New("provider unavailable")},
		{name: "unmetered", msg: &schema.Message{Role: schema.RoleAssistant, Content: "answer"}},
		{name: "nil response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := ctxpkg.NewSession(tc.name, t.TempDir())
			p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				return tc.msg, tc.err
			})
			got, err := NewCostTracker(p, "glm-5.3", session).Generate(context.Background(), nil, nil)
			if got != tc.msg || !errors.Is(err, tc.err) {
				t.Fatalf("provider response was altered: %v, %v", got, err)
			}
			if prompt, completion, cost := session.UsageTotals(); prompt != 0 || completion != 0 || cost != 0 {
				t.Fatalf("unexpected charge: %d, %d, %g", prompt, completion, cost)
			}
		})
	}
}
