package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/feishu"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

const demoPrompt = `为了加快执行速度，请你在一轮回复中，【同时并行】完成以下两件事：
1. 使用 bash 工具执行 'sleep 2 && echo "系统环境检查完毕"'
2. 使用 write_file 工具，在当前目录下创建一个 'trace_test.md'，内容写上 "测试并发的写入"。
请确保你是分别调用两个不同的工具，不要试图把它们合并成一个命令！`

type options struct {
	mode, workDir, model, prompt string
	sandbox, sandboxImage        string
	thinking, plan, checkConfig  bool
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("claw", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&opts.mode, "mode", "feishu", "运行模式：feishu（长连接）或 demo（原终端演示）")
	fs.StringVar(&opts.workDir, "workdir", "workspace", "工具、会话、技能和 Trace 共用的工作区")
	fs.StringVar(&opts.model, "model", "glm-5.3", "智谱模型名称")
	fs.StringVar(&opts.prompt, "prompt", demoPrompt, "demo 模式下执行的指令")
	fs.StringVar(&opts.sandbox, "sandbox", executor.ModeAuto, "Bash 执行后端：auto（仅 Docker，失败拒绝启动）、docker、local（不安全）")
	fs.StringVar(&opts.sandboxImage, "sandbox-image", executor.DefaultImage, "预先构建的 Docker Sandbox 镜像")
	fs.BoolVar(&opts.thinking, "thinking", false, "开启独立思考阶段")
	fs.BoolVar(&opts.plan, "plan", false, "开启 PLAN.md / TODO.md 任务续接")
	fs.BoolVar(&opts.checkConfig, "check-config", false, "仅检查本地启动配置，不连接服务、不调用模型")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("不支持位置参数，请通过 -prompt 指定 demo 指令")
	}
	if opts.mode != "feishu" && opts.mode != "demo" {
		return opts, fmt.Errorf("未知运行模式 %q，可选 feishu 或 demo", opts.mode)
	}
	if err := executor.ValidateMode(opts.sandbox); err != nil {
		return opts, err
	}
	if err := executor.ValidateImage(opts.sandboxImage); err != nil {
		return opts, err
	}
	if strings.TrimSpace(opts.workDir) == "" || strings.TrimSpace(opts.model) == "" {
		return opts, fmt.Errorf("workdir 和 model 不能为空")
	}
	if opts.mode == "demo" && strings.TrimSpace(opts.prompt) == "" {
		return opts, fmt.Errorf("demo 指令不能为空")
	}
	workDir, err := filepath.Abs(opts.workDir)
	if err != nil {
		return opts, fmt.Errorf("解析工作区失败: %w", err)
	}
	opts.workDir = workDir
	return opts, nil
}

func validateEnvironment(mode string, getenv func(string) string) error {
	required := []string{"ZHIPU_API_KEY"}
	if mode == "feishu" {
		required = append(required, "FEISHU_APP_ID", "FEISHU_APP_SECRET")
	}
	var missing []string
	for _, key := range required {
		if strings.TrimSpace(getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少环境变量: %s；请在本机设置后重新启动（参见 README.md）", strings.Join(missing, ", "))
	}
	return nil
}

// Executors are immutable/stateless; each Execute creates its own process or container.
type sessionExecutors struct {
	coding   executor.Executor
	explorer executor.Executor // nil when the backend cannot enforce read-only execution
}

// 每个聊天分别装配引擎、计费器和审批闭包，避免上下文、失败计数与账单串线。
func newSessionEngine(p provider.LLMProvider, sess *ctxpkg.Session, reporter engine.Reporter, opts options, executors sessionExecutors) *engine.AgentEngine {
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(sess.WorkDir))
	registry.Register(tools.NewWriteFileTool(sess.WorkDir))
	registry.Register(tools.NewBashTool(sess.WorkDir, executors.coding, executor.CodingPolicy()))
	registry.Register(tools.NewEditFileTool(sess.WorkDir))

	explorerRegistry := tools.NewRegistry()
	explorerRegistry.Register(tools.NewReadFileTool(sess.WorkDir))
	if executors.explorer != nil {
		explorerRegistry.Register(tools.NewBashTool(sess.WorkDir, executors.explorer, executor.ExplorerPolicy()))
	}

	if opts.mode == "feishu" {
		middleware := func(ctx context.Context, call schema.ToolCall) (bool, string) {
			if !feishu.IsDangerousCommand(call.Name, string(call.Arguments)) {
				return true, ""
			}
			r, ok := reporter.(*feishu.FeishuReporter)
			if !ok || r == nil {
				return false, "飞书审批通道不可用"
			}
			return feishu.GlobalApprovalMgr.WaitForApprovalContext(ctx, r.ChatID(), call.ID, call.Name, string(call.Arguments), r)
		}
		registry.Use(middleware)
		explorerRegistry.Use(middleware)
	}

	tracked := observability.NewCostTracker(p, opts.model, sess)
	eng := engine.NewAgentEngine(tracked, registry, opts.thinking, opts.plan)
	registry.Register(tools.NewSubagentTool(eng, explorerRegistry, reporter))
	return eng
}

const localExecutorWarning = `WARNING: 当前 Bash 使用宿主机 LocalExecutor（UNSAFE）。
命令拥有当前操作系统用户权限。
workdir 只决定默认执行目录，不构成文件系统隔离。
请勿用于不可信代码或远程无人值守任务。
`

func configureSandbox(ctx context.Context, opts options, output io.Writer) (executor.Executor, error) {
	if opts.sandbox == executor.ModeLocal {
		fmt.Fprint(output, localExecutorWarning)
		if opts.mode == "feishu" {
			fmt.Fprintln(output, "WARNING: 飞书远程请求将直接执行宿主命令；人工审批不是 Sandbox。")
		}
	}
	exec, err := executor.NewConfigured(ctx, opts.sandbox, opts.sandboxImage, executor.CodingPolicy())
	if err != nil {
		return nil, fmt.Errorf("未检测到可用 Sandbox 或初始化失败: %w\n不会回退到 Local。若确认环境可信，可显式使用：claw -sandbox=local", err)
	}
	if docker, ok := exec.(*executor.DockerExecutor); ok {
		if err := docker.ValidateWorkspace(ctx, opts.workDir); err != nil {
			return nil, err
		}
	}
	return exec, nil
}

func configureExecutors(ctx context.Context, opts options, output io.Writer) (sessionExecutors, error) {
	coding, err := configureSandbox(ctx, opts, output)
	if err != nil {
		return sessionExecutors{}, err
	}
	if opts.sandbox == executor.ModeLocal {
		fmt.Fprintln(output, "Explorer 已禁用 Bash：LocalExecutor 无法强制只读；仅开放 read_file。")
		return sessionExecutors{coding: coding}, nil
	}
	// The same image/daemon was checked above, but this is a separate executor
	// with an immutable RO policy, never the main agent's RW executor.
	explorer, err := executor.NewDockerExecutor(opts.sandboxImage, executor.ExplorerPolicy())
	if err != nil {
		return sessionExecutors{}, err
	}
	return sessionExecutors{coding: coding, explorer: explorer}, nil
}

func run(ctx context.Context, args []string, output io.Writer) error {
	opts, err := parseOptions(args, output)
	if err != nil {
		return err
	}
	if err := validateEnvironment(opts.mode, os.Getenv); err != nil {
		return err
	}
	if info, err := os.Stat(opts.workDir); err == nil && !info.IsDir() {
		return fmt.Errorf("工作区不是目录: %s", opts.workDir)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("检查工作区失败: %w", err)
	}
	if opts.checkConfig {
		fmt.Fprintf(output, "本地配置检查通过：mode=%s model=%s workspace=%s sandbox=%s sandbox-image=%s\n尚未验证 Sandbox、凭证有效性、网络连接或飞书收发消息。\n", opts.mode, opts.model, opts.workDir, opts.sandbox, opts.sandboxImage)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.workDir, 0o755); err != nil {
		return fmt.Errorf("创建工作区失败: %w", err)
	}
	executors, err := configureExecutors(ctx, opts, output)
	if err != nil {
		return err
	}
	p := provider.NewZhipuOpenAIProvider(opts.model)
	if opts.mode == "feishu" {
		bot, err := feishu.NewFeishuBotWithFactory(opts.workDir, func(sess *ctxpkg.Session, reporter engine.Reporter) *engine.AgentEngine {
			return newSessionEngine(p, sess, reporter, opts, executors)
		})
		if err != nil {
			return err
		}
		log.Printf("启动飞书事件长连接，工作区: %s，模型: %s", opts.workDir, opts.model)
		return bot.StartLongConnection(ctx)
	}

	reporter := engine.NewTerminalReporter()
	sess := ctxpkg.NewSession("test_trace_001", opts.workDir)
	eng := newSessionEngine(p, sess, reporter, opts, executors)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: opts.prompt})
	log.Println("启动终端演示（含工具、子智能体、费用统计和 Tracing）")
	if err := eng.Run(ctx, sess, reporter); err != nil {
		return err
	}
	input, completion, cost := sess.UsageTotals()
	log.Printf("会话 %s | Input Tokens: %d | Output Tokens: %d | 估算费用 (CNY): ¥%.6f", sess.ID, input, completion, cost)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) && !errors.Is(err, context.Canceled) {
		log.Printf("启动或运行失败: %v", err)
		os.Exit(1)
	}
}
