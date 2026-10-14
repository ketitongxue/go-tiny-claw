package context

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/project"
)

func trustWorkspace(t *testing.T, home, workspace string) {
	t.Helper()
	id, err := project.Discover(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.NewTrustStore(home).Trust(id.Root); err != nil {
		t.Fatal(err)
	}
}

func TestHierarchicalContextOrderOverrideAndBoundary(t *testing.T) {
	outside, home := t.TempDir(), t.TempDir()
	repo := filepath.Join(outside, "repo")
	current := filepath.Join(repo, "backend", "api")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(current, 0700); err != nil {
		t.Fatal(err)
	}
	putResource(t, filepath.Join(outside, "AGENTS.md"), "outside boundary")
	putResource(t, filepath.Join(home, "AGENTS.md"), "user")
	putResource(t, filepath.Join(repo, "AGENTS.md"), "root")
	putResource(t, filepath.Join(repo, "backend", "AGENTS.md"), "ignored")
	putResource(t, filepath.Join(repo, "backend", "AGENTS.override.md"), "backend override")
	putResource(t, filepath.Join(current, "AGENTS.md"), "current")
	r, err := (ResourceLoader{Home: home}).Load(current)
	if err != nil {
		t.Fatal(err)
	}
	var contents, scopes []string
	for _, file := range append(r.UserContext, r.ProjectContext...) {
		contents = append(contents, file.Content)
		scopes = append(scopes, file.Scope)
	}
	if !reflect.DeepEqual(contents, []string{"user", "root", "backend override", "current"}) || !reflect.DeepEqual(scopes, []string{"user", "project", "ancestor", "current"}) || len(r.Diagnostics) != 0 {
		t.Fatal(contents, scopes, r.Diagnostics)
	}
}

func TestHierarchicalContextNonGit(t *testing.T) {
	parent, home := t.TempDir(), t.TempDir()
	current := filepath.Join(parent, "plain")
	putResource(t, filepath.Join(parent, "AGENTS.md"), "parent must not load")
	putResource(t, filepath.Join(current, "AGENTS.md"), "only current")
	r, err := (ResourceLoader{Home: home}).Load(current)
	if err != nil || r.Project.Git || len(r.ProjectContext) != 1 || r.ProjectContext[0].Content != "only current" {
		t.Fatal(r, err)
	}
}

func TestHierarchicalContextSymlinkDedupAndWarning(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	putResource(t, filepath.Join(repo, ".git"), "gitdir: /not-followed/main/.git/worktrees/linked")
	rootPath := filepath.Join(repo, "AGENTS.md")
	putResource(t, rootPath, "shared instructions")
	child := filepath.Join(repo, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rootPath, filepath.Join(child, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	r, err := (ResourceLoader{Home: home}).Load(child)
	if err != nil || len(r.ProjectContext) != 1 || len(r.Diagnostics) != 0 {
		t.Fatal(r, err)
	}
	// A higher-priority broken override is warned and never falls back.
	if err := os.Symlink(filepath.Join(home, "missing"), filepath.Join(child, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	r, err = (ResourceLoader{Home: home}).Load(child)
	if err != nil || len(r.ProjectContext) != 1 || len(r.Diagnostics) != 1 {
		t.Fatal(r, err)
	}
	putResource(t, filepath.Join(home, "outside.md"), "outside secret")
	if err := os.Remove(filepath.Join(child, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "outside.md"), filepath.Join(child, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	r, err = (ResourceLoader{Home: home}).Load(child)
	if err != nil || len(r.ProjectContext) != 1 || len(r.Diagnostics) != 1 {
		t.Fatal(r, err)
	}
}

func TestPromptResourceHomeCannotBeInProjectContextRoot(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(repo, "backend")
	if err := os.MkdirAll(current, 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(repo, "user-config")
	putResource(t, filepath.Join(home, "SYSTEM.md"), "must not be read from a project tree")
	if _, err := (ResourceLoader{Home: home}).Load(current); err == nil {
		t.Fatal("project context could expose global home")
	}
}

func TestPromptDiagnosticsContextReadFailure(t *testing.T) {
	workspace, home := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "AGENTS.md"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := (ResourceLoader{Home: home}).Load(workspace)
	if err != nil || len(r.Diagnostics) != 1 || r.Diagnostics[0].Level != "warning" || len(r.ProjectContext) != 0 {
		t.Fatal(r, err)
	}
	var diagnostics []Diagnostic
	_, err = (PromptService{Resources: ResourceLoader{Home: home}, OnDiagnostic: func(d Diagnostic) { diagnostics = append(diagnostics, d) }}).Build(workspace, nil, false, false)
	if err != nil || len(diagnostics) != 1 {
		t.Fatal(diagnostics, err)
	}
}
