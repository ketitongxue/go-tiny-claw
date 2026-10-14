package tools_test

import (
	"strings"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

func TestToolPromptRules(t *testing.T) {
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
