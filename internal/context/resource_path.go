package context

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Diagnostic struct{ Level, Path, Message string }

const maxResourceBytes = 1 << 20

// Reads are bounded and diagnostics never quote instruction contents.
func readResource(root, path string) (string, string, error) {
	real, err := regularWithin(root, path)
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(real)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("resource must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxResourceBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(b) > maxResourceBytes {
		return "", "", errors.New("resource exceeds 1 MiB limit")
	}
	return real, string(b), nil
}

func canonicalExisting(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func regularWithin(root, path string) (string, error) {
	real, err := canonicalExisting(path)
	if err != nil {
		return "", err
	}
	if !within(root, real) {
		return "", fmt.Errorf("resource resolves outside allowed root")
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("resource must be a regular file")
	}
	return real, nil
}
