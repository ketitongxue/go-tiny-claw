package context

import (
	"os"
	"path/filepath"

	"github.com/ketitongxue/go-tiny-claw/internal/promptinfo"
)

// PromptSource is the only prompt contract the engine consumes.
type PromptSource interface {
	Build(workDir string, metadata []promptinfo.Tool, plan, explorer bool) (PromptState, error)
}

type PromptService struct{}

func (s PromptService) Build(workDir string, metadata []promptinfo.Tool, plan, explorer bool) (PromptState, error) {
	in := PromptInput{Core: DefaultCore, Tools: metadata, Guidelines: []string{"始终用中文回复。", "遇到工具失败时阅读错误信息，修正后再尝试。"}}
	if explorer {
		in.Core = ExplorerCore
	} else if plan {
		in.Plan = PlanPrompt
	}
	if workDir != "" {
		path := filepath.Join(workDir, "AGENTS.md")
		if b, err := os.ReadFile(path); err == nil {
			in.ProjectContext = []ContextFile{{Path: path, Content: string(b), Scope: "current"}}
		}
		in.SkillsText = escape(NewSkillLoader(workDir).LoadAll())
	}
	return NewPromptComposer().Compose(in)
}
