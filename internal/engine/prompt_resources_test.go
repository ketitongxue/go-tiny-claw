package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/project"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func TestPromptResourcesResumeThinkingAndTrace(t *testing.T) {
	workspace, home := t.TempDir(), t.TempDir()
	put := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(workspace, "AGENTS.md"), "AGENTS PRIVATE FIRST")
	put(filepath.Join(workspace, ".claw", "SYSTEM.md"), "SYSTEM PRIVATE CORE")
	put(filepath.Join(workspace, ".claw", "skills", "go", "SKILL.md"), "---\nname: go-testing\ndescription: SKILL PRIVATE METADATA\n---\nSKILL PRIVATE BODY")
	if err := project.NewTrustStore(home).Trust(workspace); err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.NewFileStore(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	svc := ctxpkg.NewSessionService(store)
	session, err := svc.Create(context.Background(), "prompt-resume", workspace, provider.ModelInfo{ProviderID: "fake", ID: "pinned"}, "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "first task"}); err != nil {
		t.Fatal(err)
	}
	phase, calls := 0, 0
	p := providerFunc(func(_ context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		sys := msgs[0].Content
		if phase == 0 {
			if !strings.Contains(sys, "SYSTEM PRIVATE CORE") || !strings.Contains(sys, "AGENTS PRIVATE FIRST") || !strings.Contains(sys, "SKILL PRIVATE METADATA") {
				t.Fatal(sys)
			}
		} else {
			if strings.Contains(sys, "SYSTEM PRIVATE CORE") || !strings.Contains(sys, "AGENTS PRIVATE CHANGED") || strings.Contains(sys, "SKILL PRIVATE METADATA") {
				t.Fatal(sys)
			}
			found := false
			for _, m := range msgs {
				if m.Content == "first task" {
					found = true
				}
			}
			if !found {
				t.Fatal("resume lost history")
			}
		}
		if strings.Contains(sys, "SKILL PRIVATE BODY") {
			t.Fatal("eager skill body")
		}
		if calls%2 == 1 {
			return &schema.Message{Role: schema.RoleAssistant, Content: "EPHEMERAL THINKING PRIVATE"}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "verified"}, nil
	})
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workspace))
	eng := NewAgentEngine(p, registry, true, false)
	eng.Prompts = ctxpkg.PromptService{Resources: ctxpkg.ResourceLoader{Home: home}, OnDiagnostic: func(ctxpkg.Diagnostic) {}}
	if err := eng.Run(context.Background(), session, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	put(filepath.Join(workspace, "AGENTS.md"), "AGENTS PRIVATE CHANGED")
	if err := project.NewTrustStore(home).Revoke(workspace); err != nil {
		t.Fatal(err)
	}
	resumed, err := ctxpkg.NewSessionService(store).Resume(context.Background(), "prompt-resume")
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.ModelRef().String() != "fake/pinned" {
		t.Fatal("model pinning changed")
	}
	if err := resumed.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "continue"}); err != nil {
		t.Fatal(err)
	}
	phase = 1
	if err := eng.Run(context.Background(), resumed, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range resumed.GetWorkingMemory(0) {
		if strings.Contains(m.Content, "PRIVATE") {
			t.Fatal("thinking/system leaked into history")
		}
	}
	traces, err := filepath.Glob(filepath.Join(workspace, ".claw", "traces", "*.json"))
	if err != nil || len(traces) != 2 {
		t.Fatal(traces, err)
	}
	hashes := map[string]bool{}
	for _, path := range traces {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "PRIVATE") {
			t.Fatal("prompt/thinking in trace")
		}
		var span observability.Span
		if err := json.Unmarshal(data, &span); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"prompt.section_count", "prompt.sections", "prompt.core_hash", "prompt.project_context_hash", "prompt.skills_hash", "prompt.estimated_tokens", "prompt.skills_count", "prompt.context_files_count"} {
			if _, ok := span.Attributes[key]; !ok {
				t.Fatal("missing", key)
			}
		}
		hash, _ := span.Attributes["prompt.project_context_hash"].(string)
		hashes[hash] = true
	}
	if len(hashes) != 2 {
		t.Fatal("project section hash did not reflect changed context")
	}
}
