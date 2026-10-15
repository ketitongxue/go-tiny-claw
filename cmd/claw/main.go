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

	"github.com/ketitongxue/go-tiny-claw/internal/app"
	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/feishu"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/openai"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

const demoPrompt = `为了加快执行速度，请你在一轮回复中，【同时并行】完成以下两件事：
1. 使用 bash 工具执行 'sleep 2 && echo "系统环境检查完毕"'
2. 使用 write_file 工具，在当前目录下创建一个 'trace_test.md'，内容写上 "测试并发的写入"。
请确保你是分别调用两个不同的工具，不要试图把它们合并成一个命令！`

type options struct {
	mode, workDir, model, prompt, resume string
	sandbox, sandboxImage                string
	thinking, plan, checkConfig          bool
	modelInfo                            provider.ModelInfo
	authType                             string
	contextPolicy                        ctxpkg.ContextPolicy
	promptHome, language                 string
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("claw", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&opts.mode, "mode", "feishu", "运行模式：feishu（长连接）或 demo（原终端演示）")
	fs.StringVar(&opts.workDir, "workdir", "workspace", "工具、会话、技能和 Trace 共用的工作区")
	fs.StringVar(&opts.model, "model", "", "模型 provider/model；默认读取全局 config.json")
	fs.StringVar(&opts.resume, "resume", "", "恢复已有 demo Session ID")
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
	modelExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "model" {
			modelExplicit = true
		}
	})
	if strings.TrimSpace(opts.workDir) == "" || modelExplicit && strings.TrimSpace(opts.model) == "" {
		return opts, fmt.Errorf("workdir 和显式 model 不能为空")
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
	var required []string
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
func newSessionEngine(p provider.LLMProvider, sess *ctxpkg.Session, reporter engine.Reporter, opts options, executors sessionExecutors, approvals *approval.Service) *engine.AgentEngine {
	registry, explorerRegistry := newToolRegistries(sess.WorkDir, executors)

	if approvals != nil {
		registry.Use(approvals.Check)
		explorerRegistry.Use(approvals.Check)
	}

	tracked := observability.NewCostTracker(p, opts.modelInfo, sess)
	sess.SetAuthType(opts.authType)
	eng := engine.NewAgentEngine(tracked, registry, opts.thinking, opts.plan)
	eng.Prompts = ctxpkg.PromptService{Resources: ctxpkg.ResourceLoader{Home: opts.promptHome}, Language: opts.language}
	eng.ContextBuilder = ctxpkg.NewContextBuilder(opts.contextPolicy)
	eng.ContextSpec = ctxpkg.ModelContextSpec{ContextWindow: opts.modelInfo.ContextWindow, MaxOutput: opts.modelInfo.MaxOutput}
	registry.Register(tools.NewSubagentTool(eng, explorerRegistry, reporter))
	return eng
}

// Shared by live sessions and offline prompt inspection, so tool selection and
// prompt metadata cannot silently drift. Subagent binding happens after wiring.
func newToolRegistries(workDir string, executors sessionExecutors) (tools.Registry, tools.Registry) {
	return newMainToolRegistry(workDir, executors.coding), newExplorerToolRegistry(workDir, executors.explorer)
}

func newMainToolRegistry(workDir string, exec executor.Executor) tools.Registry {
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir, exec, executor.CodingPolicy()))
	registry.Register(tools.NewEditFileTool(workDir))
	return registry
}

func newExplorerToolRegistry(workDir string, exec executor.Executor) tools.Registry {
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	if exec != nil {
		registry.Register(tools.NewBashTool(workDir, exec, executor.ExplorerPolicy()))
	}
	return registry
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

func run(ctx context.Context, args []string, output io.Writer) (runErr error) {
	if handled, err := dispatchCommand(ctx, args, commandIO{input: os.Stdin, output: output}); handled {
		return err
	}
	opts, err := parseOptions(args, output)
	if err != nil {
		return err
	}
	if err := validateEnvironment(opts.mode, os.Getenv); err != nil {
		return err
	}
	runtime, err := loadRuntime()
	if err != nil {
		return err
	}
	opts.contextPolicy = runtime.Config.Context
	opts.promptHome, opts.language = runtime.Paths.Home, runtime.Config.Language
	store, err := sessionstore.NewFileStore(runtime.Paths.Sessions)
	if err != nil {
		return err
	}
	service := ctxpkg.NewSessionService(store)
	var resumed *ctxpkg.Session
	if opts.resume != "" {
		if opts.mode != "demo" {
			return errors.New("-resume 仅用于 demo；飞书自动恢复")
		}
		resumed, err = service.Resume(ctx, opts.resume)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, resumed.Close()) }()
		opts.workDir = resumed.WorkDir
		opts.model = resumed.ModelRef().String()
	}
	ref, legacy, err := config.ResolveSelectedModel(opts.model, runtime.Config)
	if err != nil {
		return err
	}
	if legacy {
		fmt.Fprintf(output, "WARNING: -model %s 已弃用；请使用 -model %s\n", opts.model, ref)
	}
	opts.model = ref.String()
	if err := runtime.Paths.ValidateWorkspace(opts.workDir); err != nil {
		return err
	}
	if opts.checkConfig && ref.ProviderID == "openai" {
		if err := checkOpenAIOAuthConfig(ctx, runtime, output); err != nil {
			return err
		}
	}
	model, credentialInfo, err := runtime.Manager.Inspect(ctx, ref)
	if err != nil && (opts.mode != "feishu" || opts.checkConfig) {
		return fmt.Errorf("所选/默认模型 %s 不可用: %w。请运行 claw auth login 或指定 -model", ref, err)
	}
	opts.modelInfo = model
	opts.authType = string(credentialInfo.Type)
	if info, err := os.Stat(opts.workDir); err == nil && !info.IsDir() {
		return fmt.Errorf("工作区不是目录: %s", opts.workDir)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("检查工作区失败: %w", err)
	}
	if opts.checkConfig {
		fmt.Fprintf(output, "本地配置检查通过：mode=%s workspace=%s sandbox=%s sandbox-image=%s\nProvider: %s\nModel: %s\nAuth: %s\nCredential: available (%s)\nConfig: %s\n", opts.mode, opts.workDir, opts.sandbox, opts.sandboxImage, ref.ProviderID, ref.ModelID, credentialInfo.Type, credentialInfo.Source, runtime.Paths.Config)
		if credentialInfo.RefreshBlocked {
			fmt.Fprintln(output, "OAuth refresh deferred until earliest_refresh_at（本检查未联网）。")
		} else if credentialInfo.NeedsRefresh {
			fmt.Fprintln(output, "OAuth token 已过期或即将过期；运行时需刷新（本检查未联网）。")
		}
		fmt.Fprintln(output, "尚未验证 Sandbox、凭证有效性、网络连接或飞书收发消息。")
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.workDir, 0o755); err != nil {
		return fmt.Errorf("创建工作区失败: %w", err)
	}
	// Discover warnings before serving Feishu or contacting a model. Runtime
	// prompt builds still reload resources so later trust/revocation is observed.
	resources, err := (ctxpkg.ResourceLoader{Home: runtime.Paths.Home}).Load(opts.workDir)
	for _, d := range resources.Diagnostics {
		fmt.Fprintf(output, "[Prompt] %s: %s: %s\n", d.Level, d.Path, d.Message)
	}
	if err != nil {
		return err
	}
	executors, err := configureExecutors(ctx, opts, output)
	if err != nil {
		return err
	}
	factory := func(callCtx context.Context, sess *ctxpkg.Session, _ channel.Route, reporter engine.Reporter, approvals *approval.Service) (gateway.AgentRunner, error) {
		if err := runtime.Paths.ValidateWorkspace(sess.WorkDir); err != nil {
			return nil, err
		}
		p, m, err := runtime.Manager.Create(callCtx, sess.ModelRef())
		if err != nil {
			return nil, err
		}
		localOpts := opts
		localOpts.modelInfo = m
		return newSessionEngine(p, sess, reporter, localOpts, executors, approvals), nil
	}
	if opts.mode == "feishu" {
		adapter, err := feishu.NewAdapter()
		if err != nil {
			return err
		}
		channels := channel.NewRegistry()
		if err := channels.Register(adapter); err != nil {
			return err
		}
		sessions := gateway.DurableSessions{Service: service, LegacyID: legacyFeishuSessionID,
			Create: func(callCtx context.Context, id string) (*ctxpkg.Session, error) {
				selected, info, err := runtime.Manager.Inspect(callCtx, ref)
				if err != nil {
					return nil, err
				}
				return service.Create(callCtx, id, opts.workDir, selected, string(info.Type))
			}}
		gw, err := gateway.New(ctx, gateway.Config{Channels: channels, ResolveSession: sessions.Resolve,
			Factory: factory, Approvals: approval.NewApprovalManager()})
		if err != nil {
			return err
		}
		log.Printf("[Gateway] 启动飞书事件长连接，工作区: %s，模型: %s", opts.workDir, opts.model)
		return gw.Run()
	}

	reporter := engine.NewTerminalReporter()
	sess := resumed
	if sess == nil {
		id, err := ctxpkg.UniqueSessionID()
		if err != nil {
			return err
		}
		sess, err = service.Create(ctx, id, opts.workDir, model, opts.authType)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, sess.Close()) }()
	}
	eng, err := factory(ctx, sess, channel.Route{}, reporter, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Session: %s | Model: %s\n", sess.ID, sess.ModelRef())
	if err := sess.Append(ctx, schema.Message{Role: schema.RoleUser, Content: opts.prompt}); err != nil {
		return err
	}
	log.Println("启动终端演示（含工具、子智能体、费用统计和 Tracing）")
	if err := eng.Run(ctx, sess, reporter); err != nil {
		return err
	}
	usage := sess.UsageSnapshot()
	log.Printf("会话 %s | Input Tokens: %d | Output Tokens: %d | %s", sess.ID, usage.PromptTokens, usage.CompletionTokens, usage.Billing())
	return nil
}

// Legacy ChatID sessions belonged only to the original default bot, never to
// another channel/account/thread. The gateway itself has no platform special case.
func legacyFeishuSessionID(route channel.Route) string {
	if route.Channel == "feishu" && route.AccountID == "default" && route.ThreadID == "" {
		return route.ChatID
	}
	return ""
}

func checkOpenAIOAuthConfig(ctx context.Context, r *app.Runtime, output io.Writer) error {
	fmt.Fprintln(output, "OpenAI OAuth: dynamic registration supported (no client ID environment variable required)")
	stored, err := r.Auth.StoredCredential(ctx, "openai")
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return err
	}
	info, _ := r.Auth.Inspect(ctx, "openai")
	if info.Type == auth.CredentialAPIKey {
		fmt.Fprintln(output, "Existing OAuth credential: none (API key configured)\nHost ID: not required for API key; initialized on OAuth login")
		return nil
	}
	hostID, hostErr := r.Hosts.OpenAIHostID(ctx)
	if hostErr != nil {
		return hostErr
	}
	fmt.Fprintf(output, "Host ID: available (%s; value not displayed)\n", r.Paths.Host)
	if errors.Is(err, auth.ErrNotFound) {
		fmt.Fprintln(output, "Existing OAuth credential: none")
		return nil
	}
	if c, ok := stored.(auth.OAuthCredential); ok {
		if err := openai.CheckCredential(c); err != nil {
			fmt.Fprintln(output, "Existing OAuth credential: incomplete; re-login required")
			return nil
		}
		if c.ExtAgentHostID != hostID {
			return errors.New("OpenAI OAuth host ID does not match this installation; restore host.json or logout before registering again")
		}
		planUsage := "disabled"
		if c.HasScope(openai.PlanScope) {
			planUsage = "enabled"
		}
		fmt.Fprintf(output, "Existing OAuth credential: connected; ChatGPT Plan Usage: %s\n", planUsage)
		if r.OpenAICatalogError != nil {
			fmt.Fprintln(output, "OAuth model cache: invalid; run claw models to refresh")
		}
	} else {
		fmt.Fprintln(output, "Existing OAuth credential: none (API key configured)")
	}
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
