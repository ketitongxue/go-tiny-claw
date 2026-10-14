package context

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

var sectionOrder = [...]string{"core", "tools", "rules", "user_context", "project_context", "skills", "runtime", "plan", "addendum"}

type PromptSection struct{ Name, Content string }

// PromptState is a per-run value. Callers must not mutate Sections after composition.
// Content is a provider-compatible rendering; hashes contain no instruction text.
type PromptState struct {
	Sections                       []PromptSection
	Content                        string
	Hashes                         map[string]string
	SkillsCount, ContextFilesCount int
}

// NewPromptState validates names/duplicates and normalizes order. Content is an
// XML fragment prepared by the composer; resource text must be escaped there.
func NewPromptState(sections []PromptSection) (PromptState, error) {
	values := make(map[string]string)
	for _, s := range sections {
		valid := false
		for _, name := range sectionOrder {
			if name == s.Name {
				valid = true
				break
			}
		}
		if !valid {
			return PromptState{}, fmt.Errorf("invalid prompt section name %q", s.Name)
		}
		if _, ok := values[s.Name]; ok {
			return PromptState{}, fmt.Errorf("duplicate prompt section %q", s.Name)
		}
		values[s.Name] = s.Content
	}
	state := PromptState{Hashes: make(map[string]string)}
	var rendered []string
	for _, name := range sectionOrder {
		content := values[name]
		state.Hashes[name] = fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		if strings.TrimSpace(content) == "" {
			continue
		}
		state.Sections = append(state.Sections, PromptSection{name, content})
		if name == "core" {
			rendered = append(rendered, content)
		} else {
			rendered = append(rendered, "<"+name+">\n"+content+"\n</"+name+">")
		}
	}
	state.Content = strings.Join(rendered, "\n\n")
	return state, nil
}
func (s PromptState) SectionHash(name string) string { return s.Hashes[name] }
func (s PromptState) Message() schema.Message {
	return schema.Message{Role: schema.RoleSystem, Content: s.Content}
}
func (s PromptState) Metrics() map[string]any {
	names := make([]string, len(s.Sections))
	for i, section := range s.Sections {
		names[i] = section.Name
	}
	return map[string]any{
		"prompt.section_count": len(s.Sections), "prompt.sections": names,
		"prompt.core_hash": s.SectionHash("core"), "prompt.project_context_hash": s.SectionHash("project_context"), "prompt.skills_hash": s.SectionHash("skills"),
		"prompt.estimated_tokens": (HeuristicEstimator{}).EstimateMessage(s.Message()),
		"prompt.skills_count":     s.SkillsCount, "prompt.context_files_count": s.ContextFilesCount,
	}
}
