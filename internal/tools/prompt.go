package tools

import (
	"sort"

	"github.com/ketitongxue/go-tiny-claw/internal/promptinfo"
)

type ToolPromptMetadata = promptinfo.Tool

type PromptAwareTool interface{ PromptMetadata() ToolPromptMetadata }

// PromptMetadata works with custom registries too. No Registry API expansion is
// required; ordinary tools still work and receive a schema-description summary.
func PromptMetadata(r Registry) []ToolPromptMetadata {
	if r == nil {
		return nil
	}
	if source, ok := r.(interface{ PromptMetadata() []ToolPromptMetadata }); ok {
		return source.PromptMetadata()
	}
	var out []ToolPromptMetadata
	for _, def := range r.GetAvailableTools() {
		out = append(out, ToolPromptMetadata{Name: def.Name, Summary: def.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (r *registryImpl) PromptMetadata() []ToolPromptMetadata {
	var out []ToolPromptMetadata
	for name, tool := range r.tools {
		metadata := ToolPromptMetadata{Name: name, Summary: tool.Definition().Description}
		if aware, ok := tool.(PromptAwareTool); ok {
			metadata = aware.PromptMetadata()
			metadata.Name = name
		}
		metadata.Guidelines = append([]string(nil), metadata.Guidelines...)
		out = append(out, metadata)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (t *ReadFileTool) PromptMetadata() ToolPromptMetadata {
	return ToolPromptMetadata{Name: t.Name(), Summary: "读取工作区内已有文件。", Guidelines: []string{"使用 read_file 阅读文件以了解上下文；不要用它读取目录。"}}
}
func (t *WriteFileTool) PromptMetadata() ToolPromptMetadata {
	return ToolPromptMetadata{Name: t.Name(), Summary: "创建新文件或完整替换文件。", Guidelines: []string{"使用 write_file 创建新文件或完整替换内容；修改已有文件前先阅读。"}}
}
func (t *EditFileTool) PromptMetadata() ToolPromptMetadata {
	return ToolPromptMetadata{Name: t.Name(), Summary: "对已有文件进行局部修改。", Guidelines: []string{"修改已有文件前先阅读；局部修改优先使用 edit_file，避免重写大文件。"}}
}
func (t *BashTool) PromptMetadata() ToolPromptMetadata {
	return ToolPromptMetadata{Name: t.Name(), Summary: "在配置的执行环境中运行命令。", Guidelines: []string{"使用 bash 的 ls、find 或 test 检查目录和文件是否存在，并运行验证命令。"}}
}
func (t *SubagentTool) PromptMetadata() ToolPromptMetadata {
	return ToolPromptMetadata{Name: t.Name(), Summary: "委派只读 Explorer 探索代码并返回摘要。", Guidelines: []string{"需要跨文件深入搜索时使用 spawn_subagent，给出明确的探索目标。"}}
}
