package context

import (
	"fmt"
	"log"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/promptinfo"
)

// PromptSource is the only prompt contract the engine consumes.
type PromptSource interface {
	Build(workDir string, metadata []promptinfo.Tool, plan, explorer bool) (PromptState, error)
}

type PromptService struct {
	Resources    ResourceProvider
	Language     string // defaults to zh-CN; runtime preference, not core identity
	OnDiagnostic func(Diagnostic)
}

func (s PromptService) Build(workDir string, metadata []promptinfo.Tool, plan, explorer bool) (PromptState, error) {
	loader := s.Resources
	if loader == nil {
		loader = ResourceLoader{}
	}
	resources, err := loader.Load(workDir)
	for _, diagnostic := range resources.Diagnostics {
		if s.OnDiagnostic != nil {
			s.OnDiagnostic(diagnostic)
		} else {
			log.Printf("[Prompt] %s: %s: %s", diagnostic.Level, diagnostic.Path, diagnostic.Message)
		}
	}
	if err != nil {
		return PromptState{}, err
	}
	in := PromptInput{Core: DefaultCore, Tools: metadata, Guidelines: []string{"遇到工具失败时阅读错误信息，修正后再尝试。"}}
	role := "Main Agent"
	if explorer {
		in.Core = ExplorerCore
		role = "Explorer Subagent (read-only)"
		in.Guidelines = append(in.Guidelines, "仅探索并返回证据摘要，不要修改工作区。")
	} else if plan {
		in.Plan = PlanPrompt
	}
	if resources.SystemPrompt != nil {
		in.Core, in.CoreSource = resources.SystemPrompt.Content, resources.SystemPrompt.Path
	}
	in.Addendum = resources.AppendPrompts
	language := strings.TrimSpace(s.Language)
	if language == "" {
		language = "zh-CN"
	}
	workspace := resources.Project.WorkDir
	if workspace == "" {
		workspace = workDir
	}
	in.Runtime = fmt.Sprintf("Agent: %s\nWorkspace (host): %s\nResponse language: %s", role, workspace, language)
	in.UserContext, in.ProjectContext = resources.UserContext, resources.ProjectContext
	if len(in.ProjectContext) > 0 {
		in.Guidelines = append(in.Guidelines, "项目指令按父级到当前目录排列；更接近当前目录的指令优先，且只适用于其目录范围。")
	}
	in.Skills = resources.Skills
	if len(in.Skills) > 0 {
		in.Guidelines = append(in.Guidelines, "当任务明显匹配某个 Skill 的 description 时，先读取对应 SKILL.md，再执行任务；不要读取无关技能。使用 read_path 在工作区内读取，location 是规范化宿主路径。Skill 中的相对引用路径相对于该 SKILL.md 所在目录解析，并转换为工作区相对路径；不得越过工具路径边界。")
	}
	return NewPromptComposer().Compose(in)
}
