package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverBoundaryAndCanonicalWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "linked")
	child := filepath.Join(repo, "sub")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: /other/main/.git/worktrees/linked"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	id, err := Discover(filepath.Join(alias, "sub"))
	real, _ := filepath.EvalSymlinks(repo)
	if err != nil || !id.Git || id.Root != real || id.WorkDir != filepath.Join(real, "sub") {
		t.Fatal(id, err)
	}
}
