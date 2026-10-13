package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type executorFunc func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error)

func (f executorFunc) Execute(ctx context.Context, req executor.Request, onOutput executor.OutputHandler) (executor.Result, error) {
	return f(ctx, req, onOutput)
}

func TestBashDelegatesToExecutor(t *testing.T) {
	called := false
	runner := executorFunc(func(_ context.Context, req executor.Request, onOutput executor.OutputHandler) (executor.Result, error) {
		called = true
		if req.Command != "echo hello" || req.WorkDir != "/configured-workspace" || req.Timeout != executor.DefaultTimeout || onOutput != nil {
			t.Fatalf("unexpected executor request: %+v", req)
		}
		return executor.Result{Stdout: "hello\n", ExitCode: 0}, nil
	})
	output, err := NewBashTool("/configured-workspace", runner, executor.CodingPolicy()).Execute(context.Background(), json.RawMessage(`{"command":"echo hello"}`))
	if err != nil || output != "hello\n" || !called {
		t.Fatalf("output=%q error=%v called=%v", output, err, called)
	}
}

func TestBashValidatesArguments(t *testing.T) {
	tool := NewBashTool("unused", executorFunc(func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error) {
		t.Fatal("executor called with invalid arguments")
		return executor.Result{}, nil
	}), executor.CodingPolicy())
	for _, args := range []string{`{`, `{}`, `{"command":" "}`, `{"command":12}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("accepted invalid arguments: %s", args)
		}
	}
}

func TestBashFailuresPreserveOutputAndIsError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result executor.Result
		err    error
		status string
	}{
		{"exit", executor.Result{ExitCode: 7, Stdout: "partial", Stderr: "problem"}, nil, "Command exited with code 7"},
		{"timeout", executor.Result{ExitCode: -1, Stdout: "partial", Stderr: "problem"}, context.DeadlineExceeded, "超时"},
		{"cancel", executor.Result{ExitCode: -1, Stdout: "partial", Stderr: "problem"}, context.Canceled, "取消"},
		{"infrastructure", executor.Result{ExitCode: -1, Stdout: "partial", Stderr: "problem"}, errors.New("docker daemon unavailable"), "docker daemon unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			registry.Register(NewBashTool("unused", executorFunc(func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error) {
				return tc.result, tc.err
			}), executor.CodingPolicy()))
			result := registry.Execute(context.Background(), schema.ToolCall{ID: "bash-failure", Name: "bash", Arguments: json.RawMessage(`{"command":"run"}`)})
			if !result.IsError || result.ToolCallID != "bash-failure" {
				t.Fatalf("result=%+v", result)
			}
			for _, want := range []string{tc.status, "STDOUT:", "partial", "STDERR:", "problem"} {
				if !strings.Contains(result.Output, want) {
					t.Errorf("missing %q in %q", want, result.Output)
				}
			}
			if strings.Contains(result.Output, "执行成功") {
				t.Fatal("failure marked as success")
			}
		})
	}
}

func TestBashRealExitOne(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewBashTool(t.TempDir(), executor.NewLocalExecutor(), executor.CodingPolicy()))
	result := registry.Execute(context.Background(), schema.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"exit 1"}`)})
	if !result.IsError || !strings.Contains(result.Output, "code 1") || strings.Contains(result.Output, "执行成功") {
		t.Fatalf("result=%+v", result)
	}
}

func TestBashRealTimeoutIsErrorAndUsesPolicy(t *testing.T) {
	policy := executor.CodingPolicy()
	policy.Timeout = 100 * time.Millisecond
	registry := NewRegistry()
	registry.Register(NewBashTool(t.TempDir(), executor.NewLocalExecutor(), policy))
	start := time.Now()
	result := registry.Execute(context.Background(), schema.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"sleep 10"}`)})
	if !result.IsError || !strings.Contains(result.Output, "超时") || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout result=%+v duration=%v", result, time.Since(start))
	}
}

func TestBashSuccessfulStderrIsNotFailure(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewBashTool("unused", executorFunc(func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error) {
		return executor.Result{ExitCode: 0, Stderr: "warning"}, nil
	}), executor.CodingPolicy()))
	result := registry.Execute(context.Background(), schema.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"run"}`)})
	if result.IsError || !strings.Contains(result.Output, "STDERR:\nwarning") {
		t.Fatalf("successful stderr result=%+v", result)
	}
}

func TestBashFailureOutputLimitKeepsStderrAndExitCode(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewBashTool("unused", executorFunc(func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error) {
		return executor.Result{ExitCode: 1, Stdout: strings.Repeat("中", 10000), Stderr: "important error"}, nil
	}), executor.CodingPolicy()))
	result := registry.Execute(context.Background(), schema.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"generate output"}`)})
	if !result.IsError || len(result.Output) > maxBashOutputBytes || !utf8.ValidString(result.Output) || !strings.Contains(result.Output, "important error") || !strings.Contains(result.Output, "code 1") {
		t.Fatalf("result=%+v", result)
	}
}

func TestBashSuccessOutputLimit(t *testing.T) {
	tool := NewBashTool("unused", executorFunc(func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error) {
		return executor.Result{Stdout: strings.Repeat("中", 10000), ExitCode: 0}, nil
	}), executor.CodingPolicy())
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"generate output"}`))
	if err != nil || len(output) > maxBashOutputBytes || !strings.Contains(output, "已截断") {
		t.Fatalf("output length=%d error=%v", len(output), err)
	}
}
