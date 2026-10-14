package observability

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math"
	"strings"
	"sync"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type providerFunc func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f providerFunc) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, msgs, tools)
}
func apiModel() provider.ModelInfo {
	return provider.ModelInfo{ProviderID: "api", ID: "priced", BillingMode: provider.BillingAPI, Pricing: &provider.Pricing{Input: 8, Output: 28, CachedInput: 2, Currency: "CNY"}}
}
func TestEstimateUsage(t *testing.T) {
	for _, tc := range []struct {
		prompt, output, cached int
		cost                   float64
	}{{1000000, 1000000, 0, 36}, {1000000, 100000, 250000, 9.3}, {100, 20, 25, 0.00121}, {100, 20, -5, 0.00136}, {100, 20, 200, 0.00076}} {
		b := EstimateUsage(apiModel(), schema.Usage{PromptTokens: tc.prompt, CompletionTokens: tc.output, CachedPromptTokens: tc.cached})
		if !b.CostKnown || b.Currency != "CNY" || math.Abs(b.EstimatedCost-tc.cost) > 1e-12 {
			t.Fatal(b)
		}
	}
	for _, mode := range []provider.BillingMode{provider.BillingSubscription, provider.BillingUnknown} {
		m := apiModel()
		m.BillingMode = mode
		b := EstimateUsage(m, schema.Usage{PromptTokens: 100})
		if b.CostKnown || b.EstimatedCost != 0 {
			t.Fatal("mispriced subscription/unknown", b)
		}
	}
	m := apiModel()
	m.Pricing = nil
	if b := EstimateUsage(m, schema.Usage{}); b.CostKnown {
		t.Fatal("assumed free API")
	}
}
func TestCostTrackerModesAndLogs(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	for _, mode := range []provider.BillingMode{provider.BillingAPI, provider.BillingSubscription, provider.BillingUnknown} {
		logs.Reset()
		m := apiModel()
		m.BillingMode = mode
		usage := schema.Usage{PromptTokens: 100, CompletionTokens: 20, CachedPromptTokens: 25}
		msg := &schema.Message{Role: schema.RoleAssistant, Usage: &usage}
		p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			return msg, nil
		})
		s := ctxpkg.NewSession("test", t.TempDir())
		got, err := NewCostTracker(p, m, s).Generate(context.Background(), nil, nil)
		if err != nil || got != msg || msg.Usage.CachedPromptTokens != 25 {
			t.Fatal("response altered")
		}
		u := s.UsageSnapshot()
		if u.PromptTokens != 100 || u.CompletionTokens != 20 || u.BillingMode != mode || u.CostKnown != (mode == provider.BillingAPI) {
			t.Fatal(u)
		}
		if mode != provider.BillingAPI && (strings.Contains(logs.String(), "0.000000") || strings.Contains(logs.String(), "¥")) {
			t.Fatal("unknown shown as free", logs.String())
		}
		if mode == provider.BillingSubscription && !strings.Contains(logs.String(), "Subscription") {
			t.Fatal(logs.String())
		}
	}
}
func TestConcurrentSessionUsage(t *testing.T) {
	p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return &schema.Message{Usage: &schema.Usage{PromptTokens: 100, CompletionTokens: 20, CachedPromptTokens: 25}}, nil
	})
	a := ctxpkg.NewSession("a", t.TempDir())
	b := ctxpkg.NewSession("b", t.TempDir())
	ta := NewCostTracker(p, apiModel(), a)
	tb := NewCostTracker(p, apiModel(), b)
	var wg sync.WaitGroup
	for _, tc := range []struct {
		tracker *CostTracker
		count   int
	}{{ta, 40}, {tb, 15}} {
		for range tc.count {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := tc.tracker.Generate(context.Background(), nil, nil); err != nil {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	for _, tc := range []struct {
		session *ctxpkg.Session
		count   int
	}{{a, 40}, {b, 15}} {
		u := tc.session.UsageSnapshot()
		if u.PromptTokens != tc.count*100 || u.CompletionTokens != tc.count*20 || math.Abs(u.EstimatedCost-float64(tc.count)*0.00121) > 1e-12 {
			t.Fatal(u)
		}
	}
}
func TestFailedAndUnmetered(t *testing.T) {
	for _, tc := range []struct {
		msg *schema.Message
		err error
	}{{nil, errors.New("unavailable")}, {&schema.Message{Content: "ok"}, nil}, {nil, nil}} {
		s := ctxpkg.NewSession("test", t.TempDir())
		p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			return tc.msg, tc.err
		})
		got, err := NewCostTracker(p, apiModel(), s).Generate(context.Background(), nil, nil)
		if got != tc.msg || !errors.Is(err, tc.err) {
			t.Fatal("altered response")
		}
		u := s.UsageSnapshot()
		if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.EstimatedCost != 0 {
			t.Fatal("charged failed call")
		}
	}
}
