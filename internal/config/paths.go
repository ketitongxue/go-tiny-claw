package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentHomeDir is the global, non-workspace configuration directory.
// CLAW_HOME is an explicit override, also useful for isolated tests.
func AgentHomeDir() (string, error) {
	if dir := os.Getenv("CLAW_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claw"), nil
}

type Paths struct{ Home, Config, Auth, Models, Host, OpenAIModels, Sessions string }

func NewPaths(home string) (Paths, error) {
	if strings.TrimSpace(home) == "" {
		return Paths{}, fmt.Errorf("agent home must not be empty")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, err
	}
	return Paths{Sessions: filepath.Join(home, "sessions"), Home: home, Config: filepath.Join(home, "config.json"), Auth: filepath.Join(home, "auth.json"), Models: filepath.Join(home, "models.json"), Host: filepath.Join(home, "host.json"), OpenAIModels: filepath.Join(home, "openai-models.json")}, nil
}

// Refuse broad workspaces that would expose the global credential directory.
// Resolve existing parents too, so a not-yet-created directory cannot bypass it.
func (p Paths) ValidateWorkspace(workspace string) error {
	home, err := canonicalPath(p.Home)
	if err != nil {
		return err
	}
	workspace, err = canonicalPath(workspace)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{workspace, home}, {home, workspace}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("workspace must be separate from global Agent Home %s", p.Home)
		}
	}
	return nil
}
func canonicalPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = canonicalPath(parent)
	return filepath.Join(resolved, filepath.Base(path)), err
}
