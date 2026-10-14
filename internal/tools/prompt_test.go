package tools_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

type plainPromptTool struct{}

func (plainPromptTool) Name() string { return "plain" }
func (plainPromptTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "plain", Description: "Ordinary tool", InputSchema: map[string]any{"secret-schema-field": true}}
}
func (plainPromptTool) Execute(context.Context, json.RawMessage) (string, error) { return "ok", nil }

func TestOptionalToolPromptMetadata(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(plainPromptTool{})
	metadata := tools.PromptMetadata(r)
	if !reflect.DeepEqual(metadata, []tools.ToolPromptMetadata{{Name: "plain", Summary: "Ordinary tool"}}) {
		t.Fatal(metadata)
	}
	state, err := ctxpkg.NewPromptComposer().Compose(ctxpkg.PromptInput{Tools: metadata})
	if err != nil || strings.Contains(state.Content, "secret-schema-field") {
		t.Fatal(state, err)
	}
	result := r.Execute(context.Background(), schema.ToolCall{Name: "plain"})
	if result.IsError || result.Output != "ok" {
		t.Fatal(result)
	}
}

func TestToolPromptRules(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	for _, tc := range []struct {
		name       string
		main, bash bool
	}{{"main", true, true}, {"explorer", false, true}, {"without-bash", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := tools.NewRegistry()
			r.Register(tools.NewReadFileTool("."))
			if tc.main {
				r.Register(tools.NewWriteFileTool("."))
				r.Register(tools.NewEditFileTool("."))
				r.Register(tools.NewSubagentTool(nil, nil, nil))
			}
			if tc.bash {
				r.Register(tools.NewBashTool(".", nil, executor.ExplorerPolicy()))
			}
			state, err := (ctxpkg.PromptService{}).Build("", tools.PromptMetadata(r), false, !tc.main)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"write_file", "edit_file", "spawn_subagent"} {
				if strings.Contains(state.Content, name) != tc.main {
					t.Fatal(name, state.Content)
				}
			}
			if strings.Contains(state.Content, "bash") != tc.bash {
				t.Fatal(state.Content)
			}
			if strings.Contains(state.Content, "InputSchema") || strings.Contains(state.Content, "required") {
				t.Fatal("duplicated schema")
			}
		})
	}
}
