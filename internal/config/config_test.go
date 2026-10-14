package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig(t *testing.T) {
	paths, err := NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(paths.Config)
	if err != nil || c.DefaultModel != "" {
		t.Fatal(c, err)
	}
	ref, legacy, err := ResolveSelectedModel("", c)
	if err != nil || legacy || ref.String() != FallbackModel {
		t.Fatal(ref, err)
	}
	c.DefaultModel = "openai/gpt-5.2"
	if err := Save(paths.Config, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths.Config)
	if err != nil || loaded != c {
		t.Fatal(loaded, err)
	}
	for _, value := range []string{"glm-5.3", "zai-coding-cn/glm-5.3"} {
		ref, legacy, err = ResolveSelectedModel(value, c)
		if err != nil || ref.String() != FallbackModel || legacy != (value == "glm-5.3") {
			t.Fatal(ref, legacy, err)
		}
	}
	if err := Save(paths.Config, Config{DefaultModel: "broken"}); err == nil {
		t.Fatal("bad model saved")
	}
	loaded, err = Load(paths.Config)
	if err != nil || loaded != c {
		t.Fatal("failed save damaged config")
	}
	files, _ := filepath.Glob(filepath.Join(paths.Home, "*.tmp"))
	if len(files) != 0 {
		t.Fatal(files)
	}
	if err := os.WriteFile(paths.Config, []byte(`{"default_model":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(paths.Config); err == nil {
		t.Fatal("bad JSON accepted")
	}
}
func TestHomeAndWorkspaceSeparation(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "global")
	t.Setenv("CLAW_HOME", home)
	got, err := AgentHomeDir()
	if err != nil || got != home {
		t.Fatal(got, err)
	}
	paths, _ := NewPaths(home)
	for _, path := range []string{parent, home, filepath.Join(home, "sub")} {
		if err := paths.ValidateWorkspace(path); err == nil {
			t.Fatal("exposed home", path)
		}
	}
	if err := paths.ValidateWorkspace(filepath.Join(parent, "workspace")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	if err := paths.ValidateWorkspace(filepath.Join(link, "new")); err == nil {
		t.Fatal("symlink bypass")
	}
}
