package eval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

type executorFunc func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error)

func (f executorFunc) Execute(ctx context.Context, req executor.Request, output executor.OutputHandler) (executor.Result, error) {
	return f(ctx, req, output)
}

func TestBenchmarkScriptsUseExecutor(t *testing.T) {
	for _, code := range []int{0, 7} {
		runner := NewBenchmarkRunner(nil, provider.ModelInfo{ID: "test"}, "", executorFunc(func(_ context.Context, req executor.Request, _ executor.OutputHandler) (executor.Result, error) {
			if req.Command != "validate" || req.WorkDir != "workspace" || req.Timeout != executor.CodingPolicy().Timeout {
				t.Fatalf("incorrect validation request: %+v", req)
			}
			return executor.Result{ExitCode: code, Stdout: "out", Stderr: "err"}, nil
		}))
		_, err := runner.runScript(context.Background(), "workspace", "validate")
		if (err != nil) != (code != 0) || (err != nil && (!strings.Contains(err.Error(), "exit 7") || !strings.Contains(err.Error(), "STDERR:\nerr"))) {
			t.Fatalf("exit %d incorrectly reported: %v", code, err)
		}
	}
}

func TestBenchmarkHasNoImplicitHostFallback(t *testing.T) {
	result := NewBenchmarkRunner(nil, provider.ModelInfo{ID: "test"}, "", nil).runSingleTest(context.Background(), TestCase{ID: "no-executor"})
	if result.Passed || !strings.Contains(result.ErrorMsg, "未配置") {
		t.Fatalf("benchmark silently chose local execution: %+v", result)
	}
}

func TestBenchmarkSetupInfrastructureFailureStopsBeforeModel(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := NewBenchmarkRunner(nil, provider.ModelInfo{ID: "test"}, "", executorFunc(func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error) {
		return executor.Result{ExitCode: -1}, errors.New("docker unavailable")
	}))
	result := runner.runSingleTest(context.Background(), TestCase{ID: "setup", SetupScript: "echo fixture"})
	if result.Passed || !strings.Contains(result.ErrorMsg, "docker unavailable") {
		t.Fatalf("setup infrastructure error not reported: %+v", result)
	}
}
