// Package project identifies the canonical project boundary and records explicit
// user trust. It never executes repository commands or reads git configuration.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Identity struct {
	WorkDir string
	Root    string // nearest .git directory/gitfile, otherwise WorkDir
	Git     bool
}

func canonicalDir(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("project workspace must be a directory")
	}
	return path, nil
}

func Discover(workDir string) (Identity, error) {
	workDir, err := canonicalDir(workDir)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve project workspace: %w", err)
	}
	id := Identity{WorkDir: workDir, Root: workDir}
	for dir := workDir; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			// A linked worktree has its own gitfile. Do not follow it into the
			// main worktree, and do not execute git/hooks to find the boundary.
			if !info.IsDir() && !info.Mode().IsRegular() {
				return Identity{}, errors.New(".git boundary must be a directory or regular gitfile")
			}
			id.Root, id.Git = dir, true
			return id, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Identity{}, fmt.Errorf("discover project boundary: %w", err)
		}
		if filepath.Dir(dir) == dir {
			return id, nil
		}
	}
}
