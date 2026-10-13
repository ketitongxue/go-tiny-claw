package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// BashTool owns argument parsing and model-facing output, not process startup.
type BashTool struct {
	workDir  string
	executor executor.Executor
	policy   executor.SandboxPolicy
}

func NewBashTool(workDir string, exec executor.Executor, policy executor.SandboxPolicy) *BashTool {
	return &BashTool{workDir: workDir, executor: exec, policy: policy}
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "在配置的执行环境中运行 bash 命令。支持管道和链式命令（如 &&）。返回 stdout、stderr 和失败时的退出码；权限由执行环境决定。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要执行的 bash 命令，例如 ls -la 或 go test ./...",
				},
			},
			"required": []string{"command"},
		},
	}
}

type bashArgs struct {
	Command string `json:"command"`
}

func (t *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input bashArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(input.Command) == "" {
		return "", fmt.Errorf("command 不能为空")
	}
	if t.executor == nil {
		return "", fmt.Errorf("Bash executor 未配置")
	}
	result, err := t.executor.Execute(ctx, executor.Request{
		Command: input.Command,
		WorkDir: t.workDir,
		Timeout: t.policy.Timeout,
	}, nil)
	if err != nil {
		status := fmt.Sprintf("执行基础设施错误: %v", err)
		if errors.Is(err, context.DeadlineExceeded) {
			status = fmt.Sprintf("命令执行超时，已终止: %v", err)
		} else if errors.Is(err, context.Canceled) {
			status = fmt.Sprintf("命令执行已取消: %v", err)
		}
		return bashFailureOutput(status, result), err
	}
	if result.ExitCode != 0 {
		err := fmt.Errorf("command exited with code %d", result.ExitCode)
		return bashFailureOutput(fmt.Sprintf("Command exited with code %d", result.ExitCode), result), err
	}
	return limitBashOutput(bashOutput(result)), nil
}

func bashOutput(result executor.Result) string {
	if result.Stdout == "" && result.Stderr == "" {
		return "命令执行成功，无终端输出。"
	}
	if result.Stderr == "" {
		return result.Stdout
	}
	return bashStreams(result, maxBashOutputBytes)
}

const maxBashOutputBytes = 8000

func bashFailureOutput(status string, result executor.Result) string {
	status = limitBashText(status, 1024)
	return status + "\n\n" + bashStreams(result, maxBashOutputBytes-len(status)-2)
}

// Budget both streams so a large stdout cannot hide stderr or the exit status.
func bashStreams(result executor.Result, budget int) string {
	remaining := budget - len("STDOUT:\n\nSTDERR:\n")
	stdoutBudget := remaining / 2
	stderrBudget := remaining - stdoutBudget
	if len(result.Stdout) < stdoutBudget {
		stderrBudget += stdoutBudget - len(result.Stdout)
	} else if len(result.Stderr) < stderrBudget {
		stdoutBudget += stderrBudget - len(result.Stderr)
	}
	return fmt.Sprintf("STDOUT:\n%s\nSTDERR:\n%s", limitBashText(result.Stdout, stdoutBudget), limitBashText(result.Stderr, stderrBudget))
}

func limitBashOutput(output string) string { return limitBashText(output, maxBashOutputBytes) }

// The truncation marker is included in the budget, including on failure paths.
func limitBashText(output string, budget int) string {
	if len(output) <= budget {
		return output
	}
	const marker = "\n\n...[终端输出过长，已截断]..."
	end := budget - len(marker)
	for end > 0 && !utf8.RuneStart(output[end]) {
		end--
	}
	return output[:end] + marker
}
