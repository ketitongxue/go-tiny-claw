package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathInWorkDirAllowsFilesInsideWorkDir(t *testing.T) {
	workDir := t.TempDir()
	insidePath := filepath.Join(workDir, "inside.txt")
	if err := os.WriteFile(insidePath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveExistingPathInWorkDir(workDir, "inside.txt")
	if err != nil {
		t.Fatalf("expected an in-workspace file to be allowed: %v", err)
	}

	expected, err := filepath.EvalSymlinks(insidePath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != expected {
		t.Fatalf("resolved path = %q, want %q", resolved, expected)
	}
}

func TestResolvePathInWorkDirRejectsTraversalAndExternalSymlinks(t *testing.T) {
	workDir := t.TempDir()
	outsidePath := filepath.Join(filepath.Dir(workDir), "go-tiny-claw-read-file-outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outsidePath) })

	for _, requestedPath := range []string{
		"../" + filepath.Base(outsidePath),
		outsidePath,
	} {
		if _, err := resolveExistingPathInWorkDir(workDir, requestedPath); err == nil {
			t.Errorf("expected path %q to be rejected", requestedPath)
		}
	}

	linkPath := filepath.Join(workDir, "outside-link.txt")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExistingPathInWorkDir(workDir, filepath.Base(linkPath)); err == nil {
		t.Fatal("expected a symlink to outside the workspace to be rejected")
	}
}

func TestResolvePathForWriteInWorkDirRejectsTraversalAndAllowsNewFiles(t *testing.T) {
	workDir := t.TempDir()

	resolved, err := resolvePathForWriteInWorkDir(workDir, "nested/new.txt")
	if err != nil {
		t.Fatalf("expected a new in-workspace file to be allowed: %v", err)
	}
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(realWorkDir, resolved); err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		t.Fatalf("resolved path %q escaped workDir %q", resolved, workDir)
	}

	outsidePath := filepath.Join(filepath.Dir(workDir), "go-tiny-claw-write-outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outsidePath) })

	for _, requestedPath := range []string{
		"../" + filepath.Base(outsidePath),
		outsidePath,
	} {
		if _, err := resolvePathForWriteInWorkDir(workDir, requestedPath); err == nil {
			t.Errorf("expected path %q to be rejected", requestedPath)
		}
	}

	linkPath := filepath.Join(workDir, "outside-link")
	if err := os.Symlink(filepath.Dir(outsidePath), linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePathForWriteInWorkDir(workDir, "outside-link/new.txt"); err == nil {
		t.Fatal("expected a write through an external symlink to be rejected")
	}
}
