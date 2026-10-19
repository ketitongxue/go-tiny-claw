package executor

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Both backends use this guard; Execute repeats it because contents may change.
func validateSandboxWorkDir(ctx context.Context, workDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if workDir == string(filepath.Separator) || strings.ContainsAny(workDir, "\r\n\x00") {
		return fmt.Errorf("%w: unsafe workspace %q", ErrInvalidWorkDir, workDir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("%w: cannot determine HOME: %w", ErrInvalidWorkDir, err)
	}
	home, err = canonicalHostPath(home)
	if err != nil {
		return err
	}
	if pathContains(workDir, home) {
		return fmt.Errorf("%w: HOME or its ancestor is forbidden", ErrInvalidWorkDir)
	}
	protected, err := protectedHostPaths()
	if err != nil {
		return err
	}
	for _, path := range protected {
		if pathContains(workDir, path) || pathContains(path, workDir) {
			return fmt.Errorf("%w: workspace overlaps protected host path %s", ErrInvalidWorkDir, path)
		}
	}
	return filepath.WalkDir(workDir, func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return fmt.Errorf("%w: inspect workspace: %w", ErrInvalidWorkDir, err)
		}
		mode := entry.Type()
		if mode&os.ModeSymlink != 0 {
			// Never follow a directory symlink during the walk. SRT / Docker
			// enforce reads of external targets; reject capability targets now.
			info, statErr := os.Stat(path)
			if statErr == nil {
				mode = info.Mode()
			} else if !os.IsNotExist(statErr) {
				return fmt.Errorf("%w: inspect symlink: %w", ErrInvalidWorkDir, statErr)
			}
		}
		if mode&(os.ModeSocket|os.ModeDevice|os.ModeNamedPipe) != 0 {
			return fmt.Errorf("%w: workspace contains a host socket/device/FIFO: %s", ErrInvalidWorkDir, path)
		}
		return nil
	})
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// Resolve existing ancestors of absent protected paths, but never turn EACCES,
// ELOOP or another canonicalization failure into a guessed security boundary.
func canonicalHostPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("%w: invalid host path", ErrInvalidWorkDir)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("%w: canonical path: %w", ErrInvalidWorkDir, err)
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	resolved, err = canonicalHostPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(absolute)), nil
}

// For assertions / known system constants only; security inputs use the
// error-returning canonicalHostPath above.
func canonicalPotentialPath(path string) string {
	resolved, _ := canonicalHostPath(path)
	return resolved
}
