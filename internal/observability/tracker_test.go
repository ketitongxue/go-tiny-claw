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

func TestCostTrackerKeepsConcurrentChatUsageIsolated(t *testing.T) {
	p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return &schema.Message{Role: schema.RoleAssistant, Content: "answer", Usage: &schema.Usage{PromptTokens: 100, CompletionTokens: 20}}, nil
	})
	chatA := ctxpkg.NewSession("chat-a", t.TempDir())
	chatB := ctxpkg.NewSession("chat-b", t.TempDir())
	trackerA := NewCostTracker(p, "glm-5.2", chatA)
	trackerB := NewCostTracker(p, "glm-5.2", chatB)
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
	price := PricingModel["glm-5.2"]
	for _, tc := range []struct {
		session *ctxpkg.Session
		calls   int
	}{{chatA, 40}, {chatB, 15}} {
		prompt, completion, cost := tc.session.UsageTotals()
		wantCost := float64(tc.calls) * (100*price.InputPrice + 20*price.OutputPrice) / 1e6
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := ctxpkg.NewSession(tc.name, t.TempDir())
			p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				return tc.msg, tc.err
			})
			got, err := NewCostTracker(p, "glm-5.2", session).Generate(context.Background(), nil, nil)
			if got != tc.msg || !errors.Is(err, tc.err) {
				t.Fatalf("provider response was altered: %v, %v", got, err)
			}
			if prompt, completion, cost := session.UsageTotals(); prompt != 0 || completion != 0 || cost != 0 {
				t.Fatalf("unexpected charge: %d, %d, %g", prompt, completion, cost)
			}
		})
	}
}
