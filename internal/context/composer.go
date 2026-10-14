package context

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

const DefaultCore = `你是 go-tiny-claw，在当前工作区协助完成软件研发任务。
根据实际证据开展工作，遵循适用的项目指南和技能。
保持简洁，在实际验证之前不要声称任务已经完成。`

const ExplorerCore = `你是专门负责深度探索的探路者 (Explorer Subagent)。
根据主 Agent 的任务阅读代码、查阅日志、收集证据。必须使用可用工具查证，不要凭空猜测。
未找到确切答案时继续搜索；找到线索后返回精炼的纯文本报告。不要修改工作区。`

const PlanPrompt = `长程任务与状态外部化规范 (Plan Mode: ON)
不要仅依赖短期记忆，将架构思路和进度持久化到文件。
[STEP 1: 强制环境嗅探 (Bootstrapping)]
首先使用 bash 检查工作区根目录是否已有 PLAN.md 和 TODO.md。
全新任务：使用 write_file 先创建 PLAN.md（理解、架构、技术选型），再创建 TODO.md（Markdown Checkbox 可执行步骤）。
断点续传：已有文件绝对不要覆盖；使用 read_file 阅读 PLAN.md 和 TODO.md，从第一个未完成的 - [ ] 任务继续。
[STEP 2: 严格的单步执行与实时打勾]
每完成并验证一个子任务，立即使用 edit_file（或 bash）把 TODO.md 对应项改成 - [x]，不要积累到最后才打勾。
[STEP 3: 迷失时的自救]
遇到错误或不知下一步时，使用 read_file 重新读取 TODO.md 确认位置。`

type ContextFile struct{ Path, Content, Scope string }

type PromptInput struct {
	Core                        string
	ToolsText                   string
	Guidelines                  []string
	UserContext, ProjectContext []ContextFile
	SkillsText                  string
	Runtime, Plan               string
	Addendum                    []string
}

// PromptComposer is deliberately pure: no filesystem, registry or config access.
type PromptComposer struct{}

func NewPromptComposer() *PromptComposer { return &PromptComposer{} }

func escape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
func renderContext(files []ContextFile) string {
	var parts []string
	for _, file := range files {
		parts = append(parts, fmt.Sprintf("<instruction path=\"%s\" scope=\"%s\">\n%s\n</instruction>", escape(file.Path), escape(file.Scope), escape(file.Content)))
	}
	return strings.Join(parts, "\n")
}
func (c *PromptComposer) Compose(in PromptInput) (PromptState, error) {
	var rules []string
	seen := map[string]bool{}
	for _, rule := range in.Guidelines {
		rule = strings.TrimSpace(rule)
		if rule != "" && !seen[rule] {
			seen[rule] = true
			rules = append(rules, "- "+escape(rule))
		}
	}
	appendices := make([]string, len(in.Addendum))
	for i, text := range in.Addendum {
		appendices[i] = escape(text)
	}
	state, err := NewPromptState([]PromptSection{
		{"core", escape(in.Core)}, {"tools", in.ToolsText}, {"rules", strings.Join(rules, "\n")},
		{"user_context", renderContext(in.UserContext)}, {"project_context", renderContext(in.ProjectContext)},
		{"skills", in.SkillsText}, {"runtime", escape(in.Runtime)}, {"plan", escape(in.Plan)}, {"addendum", strings.Join(appendices, "\n\n")},
	})
	state.ContextFilesCount = len(in.UserContext) + len(in.ProjectContext)
	return state, err
}
