package context_test

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/project"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

var updatePromptGolden = flag.Bool("update-prompt-golden", false, "update stable virtual-path prompt fixtures")

type fixedResources struct{}

func (fixedResources) Load(string) (ctxpkg.PromptResources, error) {
	return ctxpkg.PromptResources{
		Project: project.Identity{Root: "/workspace", WorkDir: "/workspace/backend", Git: true}, ProjectTrusted: true,
		UserContext: []ctxpkg.ContextFile{{Path: "/agent-home/AGENTS.md", Scope: "user", Content: "Prefer focused changes."}},
		ProjectContext: []ctxpkg.ContextFile{
			{Path: "/workspace/AGENTS.md", Scope: "project", Content: "Run Go tests & vet."},
			{Path: "/workspace/backend/AGENTS.override.md", Scope: "current", Content: "Use table-driven tests."},
		},
		Skills:        []ctxpkg.Skill{{Name: "go-testing", Description: "Write Go unit tests", FilePath: "/workspace/backend/.claw/skills/go-testing/SKILL.md", BaseDir: "/workspace/backend/.claw/skills/go-testing", ReadPath: ".claw/skills/go-testing/SKILL.md"}},
		AppendPrompts: []ctxpkg.ContextFile{{Path: "/agent-home/APPEND_SYSTEM.md", Scope: "user", Content: "State verification results."}, {Path: "/workspace/backend/.claw/APPEND_SYSTEM.md", Scope: "project", Content: "Avoid unrelated changes."}},
	}, nil
}
func TestPromptGolden(t *testing.T) {
	for _, agent := range []string{"main", "explorer"} {
		t.Run(agent, func(t *testing.T) {
			registry := tools.NewRegistry()
			registry.Register(tools.NewReadFileTool("/workspace/backend"))
			registry.Register(tools.NewBashTool("/workspace/backend", nil, executor.CodingPolicy()))
			if agent == "main" {
				registry.Register(tools.NewWriteFileTool("/workspace/backend"))
				registry.Register(tools.NewEditFileTool("/workspace/backend"))
				registry.Register(tools.NewSubagentTool(nil, nil, nil))
			}
			service := ctxpkg.PromptService{Resources: fixedResources{}}
			state, err := service.Build("/workspace/backend", tools.PromptMetadata(registry), true, agent == "explorer")
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join("testdata", "prompt_"+agent+".golden")
			if *updatePromptGolden {
				if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte(state.Content+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if string(golden) != state.Content+"\n" {
				t.Fatalf("prompt changed; review diff before updating %s with -update-prompt-golden", name)
			}
			second, err := service.Build("/workspace/backend", tools.PromptMetadata(registry), true, agent == "explorer")
			if err != nil || !reflect.DeepEqual(state, second) {
				t.Fatal("non-deterministic state", err)
			}
			if agent == "explorer" && (strings.Contains(state.Content, "write_file") || strings.Contains(state.Content, "edit_file") || strings.Contains(state.Content, "spawn_subagent")) {
				t.Fatal("main rules in explorer")
			}
		})
	}
}

func BenchmarkPromptComposition(b *testing.B) {
	resources, _ := (fixedResources{}).Load("")
	in := ctxpkg.PromptInput{Core: ctxpkg.DefaultCore, ProjectContext: resources.ProjectContext, Skills: resources.Skills, Plan: ctxpkg.PlanPrompt, Addendum: resources.AppendPrompts}
	composer := ctxpkg.NewPromptComposer()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := composer.Compose(in); err != nil {
			b.Fatal(err)
		}
	}
}
