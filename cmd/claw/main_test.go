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

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
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
	if err := validateEnvironment("demo", getenv); err != nil {
		t.Fatalf("legacy demo must not require Feishu credentials: %v", err)
	}
	if err := validateEnvironment("feishu", getenv); err == nil || !strings.Contains(err.Error(), "FEISHU_APP_ID, FEISHU_APP_SECRET") || strings.Contains(err.Error(), "test-only-key") {
		t.Fatalf("missing credential names should be reported without values: %v", err)
	}
	for _, args := range [][]string{{"-mode", "invalid"}, {"-workdir", ""}, {"-model", ""}, {"-mode", "demo", "-prompt", ""}, {"unexpected"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("invalid args accepted: %v", args)
		}
	}
}

func TestCheckConfigDoesNotStartModelOrCreateWorkspace(t *testing.T) {
	for _, key := range []string{"ZHIPU_API_KEY", "FEISHU_APP_ID", "FEISHU_APP_SECRET"} {
		t.Setenv(key, "offline-placeholder")
	}
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
	workDir := t.TempDir()
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
			if last.ToolCallID == "" {
				resp.ToolCalls = []schema.ToolCall{makeCall("read_file", "sub-read", map[string]string{"path": "sample.txt"})}
			} else if last.Content == "beta" {
				resp.Content = "confirmed beta"
			} else {
				return nil, fmt.Errorf("subagent read wrong workspace: %q", last.Content)
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
	eng := newSessionEngine(p, sess, nil, options{mode: "feishu", model: "glm-5.2"})
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "run regression"})
	if err := eng.Run(context.Background(), sess, nil); err != nil {
		t.Fatal(err)
	}
	if input, output, cost := sess.UsageTotals(); input != 70 || output != 14 || cost <= 0 {
		t.Fatalf("main + subagent accounting = %d/%d/%f", input, output, cost)
	}
	if input, output, cost := other.UsageTotals(); input != 0 || output != 0 || cost != 0 {
		t.Fatal("usage leaked to another session")
	}
	traces, err := filepath.Glob(filepath.Join(workDir, ".claw", "traces", "*.json"))
	if err != nil || len(traces) != 1 {
		t.Fatalf("trace export: %v, %v", traces, err)
	}
}

func TestFeishuFactoryDeniesDangerousToolWithoutApprovalChannel(t *testing.T) {
	sess := ctxpkg.NewSession("chat", t.TempDir())
	phase := 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		phase++
		if phase == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "danger", Name: "bash", Arguments: json.RawMessage(`{"command":"sudo --version"}`)}}}, nil
		}
		last := msgs[len(msgs)-1]
		if last.ToolCallID != "danger" || !strings.Contains(last.Content, "飞书审批通道不可用") {
			return nil, fmt.Errorf("dangerous command was not denied: %+v", last)
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "denied"}, nil
	})
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "approval test"})
	if err := newSessionEngine(p, sess, nil, options{mode: "feishu", model: "glm-5.2"}).Run(context.Background(), sess, nil); err != nil {
		t.Fatal(err)
	}
}
