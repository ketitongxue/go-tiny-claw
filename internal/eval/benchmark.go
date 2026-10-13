package eval

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

// TestCase 定义了一个需要 Agent 去完成并验证的独立任务
type TestCase struct {
	ID             string // 用例唯一标识
	Name           string // 用例名称
	SetupScript    string // 【可选】在 Agent 运行前执行的 bash 脚本 (用于初始化靶机代码)
	TaskPrompt     string // 发送给 Agent 的任务指令
	ValidateScript string // 【核心】在 Agent 运行结束后执行的 bash 校验脚本。exit 0 视为成功，其他视为失败
	MaxTurns       int    // 允许 Agent 尝试的最大轮数 (超时算失败)
}

// TestResult 存放单次跑分结果
type TestResult struct {
	TestCaseID   string
	Passed       bool
	TotalCostCNY float64
	DurationMs   int64
	ErrorMsg     string
}

type BenchmarkRunner struct {
	modelName string
	executor  executor.Executor
}

func NewBenchmarkRunner(model string, exec executor.Executor) *BenchmarkRunner {
	return &BenchmarkRunner{modelName: model, executor: exec}
}

// RunSuite 执行一组评测集，并返回跑分报告
func (b *BenchmarkRunner) RunSuite(ctx context.Context, testcases []TestCase) {
	log.Println("==================================================")
	log.Printf("🚀 启动自动化 Harness Benchmark 评估... | 模型: %s\n", b.modelName)
	log.Println("==================================================")

	var results []TestResult
	passedCount := 0
	totalCost := 0.0

	for _, tc := range testcases {
		log.Printf("\n>>> ⏳ 正在执行用例 [%s]: %s\n", tc.ID, tc.Name)

		res := b.runSingleTest(ctx, tc)
		results = append(results, res)

		if res.Passed {
			passedCount++
			log.Printf(">>> ✅ 用例 [%s] 测试通过! | 耗时: %dms | 花费: $%.6f\n", tc.ID, res.DurationMs, res.TotalCostCNY)
		} else {
			log.Printf(">>> ❌ 用例 [%s] 测试失败! | 错误: %s\n", tc.ID, res.ErrorMsg)
		}
		totalCost += res.TotalCostCNY
	}

	// 打印终极报表
	log.Println("\n================ 🏆 跑分终极报告 ================")
	log.Printf("总用例数: %d | 成功数: %d | 成功率: %.2f%%\n", len(testcases), passedCount, float64(passedCount)/float64(len(testcases))*100)
	log.Printf("总消耗成本: $%.6f\n", totalCost)
	log.Println("==================================================")
}

func (b *BenchmarkRunner) runSingleTest(ctx context.Context, tc TestCase) TestResult {
	startTime := time.Now()

	if b.executor == nil {
		return TestResult{TestCaseID: tc.ID, ErrorMsg: "Benchmark Executor 未配置"}
	}
	// Separate fixture directories are not sandboxes; the injected Executor
	// provides the command isolation, including for generated-code validation.
	workDir, err := os.Getwd()
	if err != nil {
		return TestResult{TestCaseID: tc.ID, ErrorMsg: err.Error()}
	}
	workDir += fmt.Sprintf("/workspace/%s_%d", tc.ID, time.Now().UnixNano())
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return TestResult{TestCaseID: tc.ID, ErrorMsg: err.Error()}
	}

	// 2. (可选) 执行 Setup 脚本准备靶机代码
	if tc.SetupScript != "" {
		if _, err := b.runScript(ctx, workDir, tc.SetupScript); err != nil {
			return TestResult{TestCaseID: tc.ID, Passed: false, ErrorMsg: fmt.Sprintf("靶机 Setup 失败: %v", err)}
		}
	}

	// 3. 组装具备打点能力 (Tracker) 的引擎
	realProvider := provider.NewZhipuOpenAIProvider(b.modelName) // 使用真实的 GLM API
	session := ctxpkg.NewSession(tc.ID, workDir)                 // 为本次跑分单独建一个 Session 记账
	trackedProvider := observability.NewCostTracker(realProvider, b.modelName, session)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir, b.executor, executor.CodingPolicy()))
	registry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(trackedProvider, registry, false, false)

	// 4. 让 Agent 开始干活
	session.Append(schema.Message{Role: schema.RoleUser, Content: tc.TaskPrompt})
	// 我们传入一个空的 reporter 屏蔽普通日志，防止刷屏
	err = eng.Run(ctx, session, nil)

	if err != nil {
		return TestResult{TestCaseID: tc.ID, Passed: false, ErrorMsg: fmt.Sprintf("Agent 崩溃: %v", err)}
	}

	// 5. 【核心断言】Agent 跑完了，我们来验收成果！
	_, err = b.runScript(ctx, workDir, tc.ValidateScript)

	duration := time.Since(startTime).Milliseconds()

	if err != nil {
		return TestResult{
			TestCaseID:   tc.ID,
			Passed:       false,
			TotalCostCNY: session.TotalCostCNY,
			DurationMs:   duration,
			ErrorMsg:     fmt.Sprintf("验证脚本执行失败: %v", err),
		}
	}

	return TestResult{
		TestCaseID:   tc.ID,
		Passed:       true,
		TotalCostCNY: session.TotalCostCNY,
		DurationMs:   duration,
	}
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
