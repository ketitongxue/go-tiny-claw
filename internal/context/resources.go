package context

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/ketitongxue/go-tiny-claw/internal/agenthome"
	"github.com/ketitongxue/go-tiny-claw/internal/project"
)

// ResourceProvider owns disk layouts and discovery; the composer consumes only
// its values. The engine depends on PromptSource, not individual resources.
type ResourceProvider interface {
	Load(workDir string) (PromptResources, error)
}

type PromptResources struct {
	UserContext, ProjectContext []ContextFile
	Skills                      []Skill
	SystemPrompt                *ContextFile // nil means use builtin core; empty file still overrides
	AppendPrompts               []ContextFile
	Diagnostics                 []Diagnostic
	Project                     project.Identity
	ProjectTrusted              bool
}

type ResourceLoader struct{ Home string }

var ErrPromptResources = errors.New("prompt resources could not be loaded; see diagnostics")

func (l ResourceLoader) Load(workDir string) (PromptResources, error) {
	var r PromptResources
	home := l.Home
	if home == "" {
		var err error
		home, err = agenthome.Dir()
		if err != nil {
			return r, err
		}
	}
	if workDir != "" {
		var err error
		r.Project, err = project.Discover(workDir)
		if err != nil {
			return r, err
		}
	}
	seen := map[string]bool{}
	// Missing optional user resources are normal, including a missing Agent Home.
	if realHome, err := canonicalExisting(home); err == nil {
		if workDir != "" && (within(r.Project.Root, realHome) || within(realHome, r.Project.WorkDir)) {
			return r, errors.New("prompt resource home must be separate from project context root")
		}
		r.UserContext = r.loadContext(realHome, filepath.Join(realHome, "AGENTS.md"), "user", seen)
		r.SystemPrompt = r.loadSystem(realHome, filepath.Join(realHome, "SYSTEM.md"), "user")
		if appendix := r.loadSystem(realHome, filepath.Join(realHome, "APPEND_SYSTEM.md"), "user"); appendix != nil {
			r.AppendPrompts = append(r.AppendPrompts, *appendix)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		r.Diagnostics = append(r.Diagnostics, Diagnostic{"error", home, "cannot resolve user prompt resource directory"})
	}
	if workDir == "" {
		return r, r.resourceError()
	}
	var dirs []string
	for dir := r.Project.WorkDir; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == r.Project.Root {
			break
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		dir, scope := dirs[i], "ancestor"
		if dir == r.Project.Root {
			scope = "project"
		}
		if dir == r.Project.WorkDir {
			scope = "current"
		}
		path := filepath.Join(dir, "AGENTS.override.md")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			path = filepath.Join(dir, "AGENTS.md")
		}
		r.ProjectContext = append(r.ProjectContext, r.loadContext(r.Project.Root, path, scope, seen)...)
	}
	trusted, trustErr := project.NewTrustStore(home).IsTrusted(r.Project.Root)
	r.ProjectTrusted = trusted
	if trustErr != nil {
		r.Diagnostics = append(r.Diagnostics, Diagnostic{"warning", filepath.Join(home, "trusted-projects.json"), "cannot read trust registry; project high-privilege prompt resources disabled"})
	}
	projectDir := filepath.Join(r.Project.WorkDir, ".claw")
	if !r.ProjectTrusted {
		for _, name := range []string{"SYSTEM.md", "APPEND_SYSTEM.md", "skills"} {
			path := filepath.Join(projectDir, name)
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				r.Diagnostics = append(r.Diagnostics, Diagnostic{"warning", path, "project contains high-privilege prompt resources, but this workspace is not trusted. Skipped. Run: claw trust (from this workspace), or claw trust -workdir <workspace>"})
			}
		}
	} else {
		r.loadProjectResources(projectDir)
	}
	return r, r.resourceError()
}

func (r *PromptResources) loadProjectResources(projectDir string) {
	skills, diagnostics := NewSkillLoader(r.Project.WorkDir).Discover()
	r.Skills = skills
	r.Diagnostics = append(r.Diagnostics, diagnostics...)
	if system := r.loadSystem(r.Project.WorkDir, filepath.Join(projectDir, "SYSTEM.md"), "project"); system != nil {
		r.SystemPrompt = system
	}
	if appendix := r.loadSystem(r.Project.WorkDir, filepath.Join(projectDir, "APPEND_SYSTEM.md"), "project"); appendix != nil {
		r.AppendPrompts = append(r.AppendPrompts, *appendix)
	}
}

func (r *PromptResources) resourceError() error {
	for _, d := range r.Diagnostics {
		if d.Level == "error" {
			return ErrPromptResources
		}
	}
	return nil
}

func (r *PromptResources) loadSystem(root, path, scope string) *ContextFile {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	real, content, err := readResource(root, path)
	if err != nil {
		r.Diagnostics = append(r.Diagnostics, Diagnostic{"error", path, "cannot load system resource: regular file inside its scope required (max 1 MiB)"})
		return nil
	}
	return &ContextFile{Path: real, Content: content, Scope: scope}
}

func (r *PromptResources) loadContext(root, path, scope string, seen map[string]bool) []ContextFile {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	real, content, err := readResource(root, path)
	if err != nil {
		r.Diagnostics = append(r.Diagnostics, Diagnostic{"warning", path, "cannot load context: regular file inside its scope required (max 1 MiB)"})
		return nil
	}
	if seen[real] {
		return nil
	}
	seen[real] = true
	return []ContextFile{{Path: real, Content: content, Scope: scope}}
}
