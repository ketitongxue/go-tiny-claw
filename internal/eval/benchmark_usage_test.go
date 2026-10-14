package eval

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type usageProvider struct{}

func (usageProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: "done", Usage: &schema.Usage{PromptTokens: 100, CompletionTokens: 10}}, nil
}
func TestBenchmarkUsesSharedBillingWithoutInventingSubscriptionCost(t *testing.T) {
	t.Chdir(t.TempDir())
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	model := provider.ModelInfo{ProviderID: "test", ID: "coder", ToolCalling: true, BillingMode: provider.BillingSubscription}
	runner := NewBenchmarkRunner(usageProvider{}, model, "oauth", executorFunc(func(_ context.Context, req executor.Request, _ executor.OutputHandler) (executor.Result, error) {
		if len(req.Env) != 0 {
			t.Error("provider credentials passed to executor")
		}
		return executor.Result{ExitCode: 0}, nil
	}))
	results := runner.RunSuite(context.Background(), []TestCase{{ID: "case", TaskPrompt: "task", ValidateScript: "true"}})
	if len(results) != 1 || !results[0].Passed || results[0].Usage.PromptTokens != 100 || results[0].Usage.CompletionTokens != 10 || results[0].Usage.CostKnown || results[0].Usage.BillingMode != provider.BillingSubscription {
		t.Fatal(results)
	}
	if strings.Contains(logs.String(), "0.000000") || !strings.Contains(logs.String(), "Subscription") {
		t.Fatal("misleading benchmark cost", logs.String())
	}
}
