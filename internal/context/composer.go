package context

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/promptinfo"
)

type ContextFile struct{ Path, Content, Scope string }

type PromptInput struct {
	Core, CoreSource            string
	Tools                       []promptinfo.Tool
	Guidelines                  []string
	UserContext, ProjectContext []ContextFile
	Skills                      []Skill
	Runtime, Plan               string
	Addendum                    []ContextFile
}

// PromptComposer is deliberately pure: no filesystem, registry or config access.
type PromptComposer struct{}

func NewPromptComposer() *PromptComposer { return &PromptComposer{} }

// Text nodes keep readable Markdown/code line breaks. Attributes retain XML
// whitespace escaping so a resource path cannot introduce another attribute.
func escapeAttribute(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

var textWhitespace = strings.NewReplacer("&#xA;", "\n", "&#xD;", "\r", "&#x9;", "\t")

func escape(value string) string { return textWhitespace.Replace(escapeAttribute(value)) }
func renderContext(files []ContextFile) string {
	var parts []string
	for _, file := range files {
		parts = append(parts, fmt.Sprintf("<instruction path=\"%s\" scope=\"%s\">\n%s\n</instruction>", escapeAttribute(file.Path), escapeAttribute(file.Scope), escape(file.Content)))
	}
	return strings.Join(parts, "\n")
}
func renderSkills(skills []Skill) string {
	var parts []string
	for _, skill := range skills {
		parts = append(parts, fmt.Sprintf("<skill>\n  <name>%s</name>\n  <description>%s</description>\n  <location>%s</location>\n  <read_path>%s</read_path>\n</skill>", escape(skill.Name), escape(skill.Description), escape(skill.FilePath), escape(skill.ReadPath)))
	}
	return strings.Join(parts, "\n")
}

func (c *PromptComposer) Compose(in PromptInput) (PromptState, error) {
	metadata := append([]promptinfo.Tool(nil), in.Tools...)
	sort.Slice(metadata, func(i, j int) bool { return metadata[i].Name < metadata[j].Name })
	var toolLines []string
	guidelines := append([]string(nil), in.Guidelines...)
	for _, tool := range metadata {
		toolLines = append(toolLines, "- "+escape(tool.Name)+": "+escape(tool.Summary))
		guidelines = append(guidelines, tool.Guidelines...)
	}
	var rules []string
	seen := map[string]bool{}
	for _, rule := range guidelines {
		rule = strings.TrimSpace(rule)
		if rule != "" && !seen[rule] {
			seen[rule] = true
			rules = append(rules, "- "+escape(rule))
		}
	}
	state, err := NewPromptState([]PromptSection{
		{"core", escape(in.Core)}, {"tools", strings.Join(toolLines, "\n")}, {"rules", strings.Join(rules, "\n")},
		{"user_context", renderContext(in.UserContext)}, {"project_context", renderContext(in.ProjectContext)},
		{"skills", renderSkills(in.Skills)}, {"runtime", escape(in.Runtime)}, {"plan", escape(in.Plan)}, {"addendum", renderContext(in.Addendum)},
	})
	state.SkillsCount = len(in.Skills)
	state.ContextFilesCount = len(in.UserContext) + len(in.ProjectContext)
	coreSource := in.CoreSource
	if coreSource == "" {
		coreSource = "builtin"
	}
	state.Sources = map[string][]string{
		"core": {coreSource}, "tools": {"registry"}, "rules": {"runtime / registry"},
		"runtime": {"generated"}, "plan": {"builtin"},
	}
	for name, files := range map[string][]ContextFile{"user_context": in.UserContext, "project_context": in.ProjectContext, "addendum": in.Addendum} {
		for _, file := range files {
			state.Sources[name] = append(state.Sources[name], file.Path)
		}
	}
	for _, skill := range in.Skills {
		state.Sources["skills"] = append(state.Sources["skills"], skill.FilePath)
	}
	return state, err
}
