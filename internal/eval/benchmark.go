package eval

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

type TestCase struct {
	ID, Name                                string
	SetupScript, TaskPrompt, ValidateScript string
	MaxTurns                                int
}
type TestResult struct {
	TestCaseID string
	Passed     bool
	Usage      ctxpkg.UsageTotals
	DurationMs int64
	ErrorMsg   string
}
type BenchmarkRunner struct {
	provider provider.LLMProvider
	model    provider.ModelInfo
	authType string
	executor executor.Executor
}

func NewBenchmarkRunner(p provider.LLMProvider, model provider.ModelInfo, authType string, exec executor.Executor) *BenchmarkRunner {
	return &BenchmarkRunner{provider: p, model: model, authType: authType, executor: exec}
}
func (b *BenchmarkRunner) RunSuite(ctx context.Context, testcases []TestCase) []TestResult {
	log.Printf("启动 Harness Benchmark | 模型: %s", b.model.Ref())
	results := make([]TestResult, 0, len(testcases))
	passed := 0
	totals := ctxpkg.UsageTotals{BillingMode: b.model.BillingMode, CostKnown: true}
	for _, tc := range testcases {
		res := b.runSingleTest(ctx, tc)
		results = append(results, res)
		if res.Passed {
			passed++
			log.Printf("用例 %s 通过 | 耗时: %dms | %s", tc.ID, res.DurationMs, res.Usage.Billing())
		} else {
			log.Printf("用例 %s 失败 | %s | %s", tc.ID, res.ErrorMsg, res.Usage.Billing())
		}
		totals.PromptTokens += res.Usage.PromptTokens
		totals.CompletionTokens += res.Usage.CompletionTokens
		totals.EstimatedCost += res.Usage.EstimatedCost
		totals.CostKnown = totals.CostKnown && res.Usage.CostKnown
		totals.Currency = res.Usage.Currency
	}
	if len(testcases) == 0 {
		totals.CostKnown = false
	}
	log.Printf("总用例数: %d | 成功数: %d | 输入: %d tk | 输出: %d tk | %s", len(testcases), passed, totals.PromptTokens, totals.CompletionTokens, totals.Billing())
	return results
}
func (b *BenchmarkRunner) runSingleTest(ctx context.Context, tc TestCase) TestResult {
	start := time.Now()
	result := TestResult{TestCaseID: tc.ID, Usage: ctxpkg.UsageTotals{BillingMode: b.model.BillingMode}}
	if b.executor == nil {
		result.ErrorMsg = "Benchmark Executor 未配置"
		return result
	}
	base, err := os.Getwd()
	if err != nil {
		result.ErrorMsg = err.Error()
		return result
	}
	// The Executor, not a fixture directory, provides isolation. Use a generated
	// path rather than putting user-controlled case IDs in filesystem syntax.
	base = filepath.Join(base, "workspace")
	if err := os.MkdirAll(base, 0755); err != nil {
		result.ErrorMsg = err.Error()
		return result
	}
	workDir, err := os.MkdirTemp(base, "bench-*")
	if err != nil {
		result.ErrorMsg = err.Error()
		return result
	}
	if tc.SetupScript != "" {
		if _, err := b.runScript(ctx, workDir, tc.SetupScript); err != nil {
			result.ErrorMsg = fmt.Sprintf("靶机 Setup 失败: %v", err)
			return result
		}
	}
	if b.provider == nil {
		result.ErrorMsg = "Benchmark Provider 未配置"
		return result
	}
	session := ctxpkg.NewSession(tc.ID, workDir)
	tracked := observability.NewCostTracker(b.provider, b.model, session)
	session.SetAuthType(b.authType)
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir, b.executor, executor.CodingPolicy()))
	registry.Register(tools.NewEditFileTool(workDir))
	eng := engine.NewAgentEngine(tracked, registry, false, false)
	session.Append(schema.Message{Role: schema.RoleUser, Content: tc.TaskPrompt})
	err = eng.Run(ctx, session, nil)
	if err != nil {
		result.ErrorMsg = fmt.Sprintf("Agent 失败: %v", err)
	} else if _, err = b.runScript(ctx, workDir, tc.ValidateScript); err != nil {
		result.ErrorMsg = fmt.Sprintf("验证脚本执行失败: %v", err)
	} else {
		result.Passed = true
	}
	result.Usage = session.UsageSnapshot()
	result.DurationMs = time.Since(start).Milliseconds()
	return result
}
func (b *BenchmarkRunner) runScript(ctx context.Context, workDir, script string) (executor.Result, error) {
	result, err := b.executor.Execute(ctx, executor.Request{Command: script, WorkDir: workDir, Timeout: executor.CodingPolicy().Timeout}, nil)
	if err != nil {
		return result, fmt.Errorf("%w\nSTDOUT:\n%s\nSTDERR:\n%s", err, result.Stdout, result.Stderr)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("exit %d\nSTDOUT:\n%s\nSTDERR:\n%s", result.ExitCode, result.Stdout, result.Stderr)
	}
	return result, nil
}
