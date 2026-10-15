package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type providerFunc func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f providerFunc) Generate(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, msgs, defs)
}

func TestStartupModesAndCredentialRequirements(t *testing.T) {
	opts, err := parseOptions(nil, io.Discard)
	if err != nil || opts.mode != "feishu" || !filepath.IsAbs(opts.workDir) {
		t.Fatalf("default startup: %+v, %v", opts, err)
	}
	opts, err = parseOptions([]string{"-mode", "demo", "-thinking", "-plan", "-prompt", "hello"}, io.Discard)
	if err != nil || !opts.thinking || !opts.plan || opts.prompt != "hello" {
		t.Fatalf("demo options: %+v, %v", opts, err)
	}
	getenv := func(key string) string {
		if key == "ZHIPU_API_KEY" {
			return "test-only-key"
		}
		return ""
	}
	if err := validateEnvironment(config.ChannelsConfig{}, getenv); err != nil {
		t.Fatalf("legacy demo must not require Feishu credentials: %v", err)
	}
	if err := validateEnvironment(config.ChannelsConfig{Feishu: config.ChannelConfig{Enabled: true}}, getenv); err == nil || !strings.Contains(err.Error(), "FEISHU_APP_ID, FEISHU_APP_SECRET") || strings.Contains(err.Error(), "test-only-key") {
		t.Fatalf("missing credential names should be reported without values: %v", err)
	}
	for _, args := range [][]string{{"-mode", "invalid"}, {"-workdir", ""}, {"-model", ""}, {"-mode", "demo", "-prompt", ""}, {"unexpected"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("invalid args accepted: %v", args)
		}
	}
}

func TestSandboxCLIOptions(t *testing.T) {
	for _, mode := range []string{executor.ModeAuto, executor.ModeDocker, executor.ModeLocal} {
		opts, err := parseOptions([]string{"-sandbox=" + mode, "-sandbox-image=custom-sandbox:v2"}, io.Discard)
		if err != nil || opts.sandbox != mode || opts.sandboxImage != "custom-sandbox:v2" {
			t.Fatalf("options=%+v error=%v", opts, err)
		}
	}
	opts, err := parseOptions(nil, io.Discard)
	if err != nil || opts.sandbox != executor.ModeAuto || opts.sandboxImage != executor.DefaultImage {
		t.Fatalf("unsafe defaults: %+v (%v)", opts, err)
	}
	for _, args := range [][]string{{"-sandbox=invalid"}, {"-sandbox="}, {"-sandbox-image="}, {"-sandbox-image=-bad"}, {"-sandbox-image=image;echo hacked"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("invalid sandbox options accepted: %v", args)
		}
	}
}

func installDockerStub(t *testing.T, script string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSandboxStartupNeverFallsBackToLocal(t *testing.T) {
	for _, mode := range []string{executor.ModeAuto, executor.ModeDocker} {
		t.Run(mode, func(t *testing.T) {
			installDockerStub(t, "echo 'daemon unavailable' >&2; exit 1")
			opts := options{mode: "demo", workDir: t.TempDir(), sandbox: mode, sandboxImage: executor.DefaultImage}
			var output bytes.Buffer
			exec, err := configureSandbox(context.Background(), opts, &output)
			if err == nil || exec != nil || !strings.Contains(err.Error(), "claw -sandbox=local") || strings.Contains(output.String(), "LocalExecutor") {
				t.Fatalf("unsafe fallback: executor=%T error=%v output=%s", exec, err, output.String())
			}
		})
	}
}

func TestSandboxStartupUsesDockerAndConfiguredImage(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "arguments")
	t.Setenv("DOCKER_STUB_LOG", logFile)
	installDockerStub(t, `printf '%s\n' "$@" >> "$DOCKER_STUB_LOG"
case "$1" in version) echo 29.0.0 ;; image) echo sha256:test ;; *) exit 2 ;; esac`)
	for _, mode := range []string{executor.ModeAuto, executor.ModeDocker} {
		exec, err := configureSandbox(context.Background(), options{mode: "demo", workDir: t.TempDir(), sandbox: mode, sandboxImage: "custom-sandbox:v2"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := exec.(*executor.DockerExecutor); !ok {
			t.Fatalf("mode=%s selected %T instead of Docker", mode, exec)
		}
	}
	log, err := os.ReadFile(logFile)
	if err != nil || strings.Count(string(log), "custom-sandbox:v2") != 2 {
		t.Fatalf("custom image was not checked: %s (%v)", log, err)
	}
}

func TestSandboxMissingImageFailsClosed(t *testing.T) {
	installDockerStub(t, `case "$1" in version) echo 29.0.0 ;; image) echo missing >&2; exit 1 ;; esac`)
	exec, err := configureSandbox(context.Background(), options{workDir: t.TempDir(), sandbox: executor.ModeAuto, sandboxImage: "missing-image:v1"}, io.Discard)
	if err == nil || exec != nil || !strings.Contains(err.Error(), "docker build") {
		t.Fatalf("missing image accepted: %T (%v)", exec, err)
	}
}

func TestRunSandboxFailureStopsBeforeModel(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	t.Setenv("ZAI_CODING_CN_API_KEY", "")
	t.Setenv("ZHIPU_API_KEY", "offline-placeholder")
	installDockerStub(t, "echo 'daemon unavailable' >&2; exit 1")
	var output bytes.Buffer
	err := run(context.Background(), []string{"-mode=demo", "-sandbox=auto", "-workdir=" + t.TempDir()}, &output)
	if err == nil || !strings.Contains(err.Error(), "daemon unavailable") || !strings.Contains(err.Error(), "-sandbox=local") || strings.Contains(output.String(), "LocalExecutor") {
		t.Fatalf("run ignored unsafe startup: error=%v output=%s", err, output.String())
	}
}

func TestLocalModeWarnsWithoutCheckingDocker(t *testing.T) {
	// No CLI at all: local remains an explicit, usable opt-in.
	t.Setenv("PATH", t.TempDir())
	for _, mode := range []string{"demo", "feishu", "gateway"} {
		var output bytes.Buffer
		exec, err := configureSandbox(context.Background(), options{mode: mode, sandbox: executor.ModeLocal}, &output)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := exec.(*executor.LocalExecutor); !ok {
			t.Fatalf("local selected %T", exec)
		}
		for _, want := range []string{"WARNING", "UNSAFE", "当前操作系统用户权限", "不构成文件系统隔离", "远程无人值守"} {
			if !strings.Contains(output.String(), want) {
				t.Errorf("missing warning %q: %s", want, output.String())
			}
		}
		if mode == "feishu" && !strings.Contains(output.String(), "飞书远程请求") {
			t.Fatal("missing Feishu-specific unsafe warning")
		}
		if mode == "gateway" && !strings.Contains(output.String(), "Messaging Gateway 远程请求") {
			t.Fatal("missing gateway unsafe warning")
		}
	}
}

func TestCheckConfigDoesNotStartModelOrCreateWorkspace(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	for _, key := range []string{"ZHIPU_API_KEY", "FEISHU_APP_ID", "FEISHU_APP_SECRET"} {
		t.Setenv(key, "offline-placeholder")
	}
	// Offline validation must not probe Docker, even when auto is selected.
	t.Setenv("PATH", t.TempDir())
	workDir := filepath.Join(t.TempDir(), "not-created")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"-check-config", "-workdir", workDir}, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("check-config changed workspace: %v", err)
	}
	if !strings.Contains(output.String(), "尚未验证") || strings.Contains(output.String(), "offline-placeholder") {
		t.Fatalf("config check must distinguish offline validation and hide values: %s", output.String())
	}
}

func TestSessionFactoryRunsExistingToolsAndSubagent(t *testing.T) {
	runSessionFactoryRegression(t, options{mode: "feishu", model: "glm-5.3", workDir: t.TempDir()}, sessionExecutors{
		coding: executor.NewLocalExecutor(),
		explorer: executorFunc(func(_ context.Context, req executor.Request, _ executor.OutputHandler) (executor.Result, error) {
			if strings.Contains(req.Command, "echo hacked") {
				return executor.Result{ExitCode: 1, Stderr: "Read-only file system"}, nil
			}
			return executor.Result{ExitCode: 0, Stdout: "beta"}, nil
		}),
	})
}

func runSessionFactoryRegression(t *testing.T, opts options, executors sessionExecutors) {
	t.Helper()
	opts.modelInfo = provider.ModelInfo{ProviderID: "zai-coding-cn", ID: "glm-5.3", ToolCalling: true, BillingMode: provider.BillingSubscription}
	opts.authType = "api_key"
	workDir := opts.workDir
	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte("factory-workspace-guide"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := ctxpkg.NewSession("chat-a", workDir)
	other := ctxpkg.NewSession("chat-b", workDir)
	phase := 0
	makeCall := func(name, id string, args map[string]string) schema.ToolCall {
		encoded, _ := json.Marshal(args)
		return schema.ToolCall{ID: id, Name: name, Arguments: encoded}
	}
	p := providerFunc(func(_ context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		resp := &schema.Message{Role: schema.RoleAssistant, Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 2}}
		last := msgs[len(msgs)-1]
		if strings.Contains(msgs[0].Content, "Explorer Subagent") {
			if len(defs) != 2 {
				return nil, fmt.Errorf("explorer received %d tools, expected read_file and bash", len(defs))
			}
			for _, def := range defs {
				if def.Name != "read_file" && def.Name != "bash" {
					return nil, fmt.Errorf("unexpected explorer tool %s", def.Name)
				}
			}
			switch last.ToolCallID {
			case "":
				resp.ToolCalls = []schema.ToolCall{makeCall("read_file", "sub-read", map[string]string{"path": "sample.txt"})}
			case "sub-read":
				if last.Content != "beta" {
					return nil, fmt.Errorf("subagent read wrong workspace: %q", last.Content)
				}
				resp.ToolCalls = []schema.ToolCall{makeCall("bash", "sub-write", map[string]string{"command": "echo hacked > sample.txt"})}
			case "sub-write":
				if !strings.Contains(last.Content, "code 1") || !strings.Contains(last.Content, "Read-only file system") || !strings.Contains(last.Content, "[系统救援指南]") {
					return nil, fmt.Errorf("Explorer did not reject writing: %q", last.Content)
				}
				resp.ToolCalls = []schema.ToolCall{makeCall("bash", "sub-bash", map[string]string{"command": "cat sample.txt"})}
			case "sub-bash":
				if last.Content != "beta" {
					return nil, fmt.Errorf("Explorer modified workspace or lost Bash output: %q", last.Content)
				}
				resp.Content = "confirmed beta"
			default:
				return nil, fmt.Errorf("unexpected subagent result: %q", last.ToolCallID)
			}
			return resp, nil
		}
		if !strings.Contains(msgs[0].Content, "factory-workspace-guide") || len(defs) != 5 {
			return nil, fmt.Errorf("main engine lost workspace guide or tools")
		}
		switch phase {
		case 0:
			resp.ToolCalls = []schema.ToolCall{makeCall("write_file", "write", map[string]string{"path": "sample.txt", "content": "alpha"})}
		case 1:
			resp.ToolCalls = []schema.ToolCall{makeCall("edit_file", "edit", map[string]string{"path": "sample.txt", "old_text": "alpha", "new_text": "beta"})}
		case 2:
			resp.ToolCalls = []schema.ToolCall{
				makeCall("read_file", "read", map[string]string{"path": "sample.txt"}),
				makeCall("bash", "shell", map[string]string{"command": "cat sample.txt"}),
			}
		case 3:
			for _, msg := range msgs[len(msgs)-2:] {
				if msg.Content != "beta" {
					return nil, fmt.Errorf("real tool result = %q, expected beta", msg.Content)
				}
			}
			resp.ToolCalls = []schema.ToolCall{makeCall("spawn_subagent", "sub", map[string]string{"task_prompt": "读取 sample.txt"})}
		case 4:
			if !strings.Contains(last.Content, "confirmed beta") {
				return nil, fmt.Errorf("missing subagent result: %q", last.Content)
			}
			resp.Content = "done"
		default:
			return nil, fmt.Errorf("unexpected phase %d", phase)
		}
		phase++
		return resp, nil
	})
	eng := newSessionEngine(p, sess, nil, opts, executors, nil)
	sess.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "run regression"})
	if err := eng.Run(context.Background(), sess, nil); err != nil {
		t.Fatal(err)
	}
	if input, output, cost := sess.UsageTotals(); input != 90 || output != 18 || cost != 0 {
		t.Fatalf("main + subagent accounting = %d/%d/%f", input, output, cost)
	}
	if usage := sess.UsageSnapshot(); usage.CostKnown || usage.BillingMode != provider.BillingSubscription {
		t.Fatalf("subscription mispriced: %+v", usage)
	}
	if input, output, cost := other.UsageTotals(); input != 0 || output != 0 || cost != 0 {
		t.Fatal("usage leaked to another session")
	}
	traces, err := filepath.Glob(filepath.Join(workDir, ".claw", "traces", "*.json"))
	if err != nil || len(traces) != 1 {
		t.Fatalf("trace export: %v, %v", traces, err)
	}
	data, err := os.ReadFile(traces[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`"provider_id": "zai-coding-cn"`, `"model_id": "glm-5.3"`, `"billing_mode": "subscription"`, `"auth_type": "api_key"`} {
		if !strings.Contains(string(data), value) {
			t.Errorf("missing trace metadata %s", value)
		}
	}
}

type executorFunc func(context.Context, executor.Request, executor.OutputHandler) (executor.Result, error)

func (f executorFunc) Execute(ctx context.Context, req executor.Request, output executor.OutputHandler) (executor.Result, error) {
	return f(ctx, req, output)
}

func TestExplorerUsesSeparateReadOnlyDockerPolicy(t *testing.T) {
	workDir := t.TempDir()
	file := filepath.Join(workDir, "main.go")
	if err := os.WriteFile(file, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(t.TempDir(), "docker-arguments")
	t.Setenv("DOCKER_STUB_LOG", logFile)
	installDockerStub(t, `printf '%s\n' "$@" >> "$DOCKER_STUB_LOG"
case "$1" in
  version) echo 29.0.0 ;;
  image) echo sha256:test ;;
  rm) exit 0 ;;
  run)
    shift
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--mount" ]; then
        case "$2" in *,readonly) echo 'Read-only file system' >&2; exit 1 ;; esac
      fi
      shift
    done
    printf coding ;;
  *) exit 2 ;;
esac`)
	opts := options{mode: "demo", model: "glm-5.2", workDir: workDir, sandbox: executor.ModeDocker, sandboxImage: executor.DefaultImage}
	executors, err := configureExecutors(context.Background(), opts, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if executors.coding == executors.explorer || executors.explorer == nil {
		t.Fatal("Explorer shares the main RW executor")
	}
	mainTurn, subTurn := 0, 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		if strings.Contains(msgs[0].Content, "Explorer Subagent") {
			subTurn++
			if len(defs) != 2 {
				return nil, fmt.Errorf("Docker Explorer must have read_file and Bash")
			}
			if subTurn == 1 {
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "attempt-write", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hacked > main.go"}`)}}}, nil
			}
			last := msgs[len(msgs)-1]
			if !strings.Contains(last.Content, "code 1") || !strings.Contains(last.Content, "Read-only file system") || !strings.Contains(last.Content, "[系统救援指南]") {
				return nil, fmt.Errorf("Explorer write was not rejected with recovery: %s", last.Content)
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "write refused"}, nil
		}
		mainTurn++
		switch mainTurn {
		case 1:
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "coding", Name: "bash", Arguments: json.RawMessage(`{"command":"echo coding"}`)}}}, nil
		case 2:
			if msgs[len(msgs)-1].Content != "coding" {
				return nil, fmt.Errorf("main did not use RW coding executor")
			}
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "explore", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"尝试写入 main.go"}`)}}}, nil
		default:
			if !strings.Contains(msgs[len(msgs)-1].Content, "write refused") {
				return nil, fmt.Errorf("main lost Explorer result")
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
		}
	})
	sess := ctxpkg.NewSession("explorer-policy", workDir)
	sess.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "test Explorer isolation"})
	if err := newSessionEngine(p, sess, nil, opts, executors, nil).Run(context.Background(), sess, nil); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--memory\n1024m", "--memory\n512m", "--cpus\n1", ",readonly\n", "echo hacked > main.go"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("Explorer's actual Docker arguments lost %q: %s", want, log)
		}
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "unchanged" {
		t.Fatalf("Explorer changed workspace: %q (%v)", content, err)
	}
}

func TestLocalExplorerDoesNotExposeUnsafeBash(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "sample.txt"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := options{mode: "demo", model: "glm-5.2", workDir: workDir, sandbox: executor.ModeLocal}
	var output bytes.Buffer
	executors, err := configureExecutors(context.Background(), opts, &output)
	if err != nil || executors.explorer != nil || !strings.Contains(output.String(), "Explorer 已禁用 Bash") {
		t.Fatalf("local pretended to be read-only: %+v (%v) output=%s", executors, err, output.String())
	}
	turn := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		if len(defs) != 1 || defs[0].Name != "read_file" {
			return nil, fmt.Errorf("Local Explorer exposed unsafe tools: %v", defs)
		}
		turn++
		if turn == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{
				{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"sample.txt"}`)},
				{ID: "unsafe", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hacked > sample.txt"}`)},
			}}, nil
		}
		if msgs[len(msgs)-2].Content != "evidence" || !strings.Contains(msgs[len(msgs)-1].Content, "不存在") {
			return nil, fmt.Errorf("Local Explorer read or denied Bash incorrectly: %v", msgs)
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "evidence"}, nil
	})
	sess := ctxpkg.NewSession("local-explorer", workDir)
	// Reach the real Explorer registry through the main spawn_subagent tool.
	mainTurn := 0
	engProvider := providerFunc(func(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		if strings.Contains(msgs[0].Content, "Explorer Subagent") {
			return p.Generate(ctx, msgs, defs)
		}
		mainTurn++
		if mainTurn == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "sub", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"读取 sample.txt"}`)}}}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
	})
	eng := newSessionEngine(engProvider, sess, nil, opts, executors, nil)
	sess.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "read-only local exploration"})
	if err := eng.Run(context.Background(), sess, nil); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workDir, "sample.txt"))
	if err != nil || string(content) != "evidence" || turn != 2 {
		t.Fatalf("local Explorer modified workspace: %q (%v), turns=%d", content, err, turn)
	}
}

func TestFactoryDeniesDangerousToolWithoutApprovalNotifier(t *testing.T) {
	sess := ctxpkg.NewSession("chat", t.TempDir())
	phase := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		phase++
		if phase == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "danger", Name: "bash", Arguments: json.RawMessage(`{"command":"sudo --version"}`)}}}, nil
		}
		last := msgs[len(msgs)-1]
		if last.ToolCallID != "danger" || !strings.Contains(last.Content, "审批通道不可用") {
			return nil, fmt.Errorf("dangerous command was not denied: %+v", last)
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "denied"}, nil
	})
	sess.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "approval test"})
	// Explicit runtime injection enables approval even in demo mode and without
	// a concrete channel reporter. No mode-dependent tool policy remains.
	approvals := approval.NewService(approval.NewApprovalManager(), sess.ID, channel.Route{Channel: "test", AccountID: "default", ChatID: "chat"}, nil)
	if err := newSessionEngine(p, sess, nil, options{mode: "demo", model: "glm-5.2"}, sessionExecutors{coding: executor.NewLocalExecutor()}, approvals).Run(context.Background(), sess, nil); err != nil {
		t.Fatal(err)
	}
}
