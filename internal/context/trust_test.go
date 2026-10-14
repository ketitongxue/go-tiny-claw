package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/project"
)

func TestProjectTrustResourceGate(t *testing.T) {
	workspace, home := t.TempDir(), t.TempDir()
	putResource(t, filepath.Join(home, "SYSTEM.md"), "USER SYSTEM")
	putResource(t, filepath.Join(home, "APPEND_SYSTEM.md"), "USER APPEND")
	putResource(t, filepath.Join(workspace, "AGENTS.md"), "REPO INSTRUCTIONS")
	putResource(t, filepath.Join(workspace, ".claw", "SYSTEM.md"), "PROJECT SYSTEM")
	putResource(t, filepath.Join(workspace, ".claw", "APPEND_SYSTEM.md"), "PROJECT APPEND")
	putSkill(t, filepath.Join(workspace, ".claw", "skills", "a"), "a", "test skill", "NEVER EAGER BODY")
	loader := ResourceLoader{Home: home}
	check := func(trusted bool) {
		t.Helper()
		r, err := loader.Load(workspace)
		if err != nil || r.ProjectTrusted != trusted || len(r.ProjectContext) != 1 || len(r.AppendPrompts) != 1+btoi(trusted) || len(r.Skills) != btoi(trusted) {
			t.Fatal(r, err)
		}
		if trusted {
			if r.SystemPrompt.Content != "PROJECT SYSTEM" || len(r.Diagnostics) != 0 {
				t.Fatal(r)
			}
		} else {
			if r.SystemPrompt.Content != "USER SYSTEM" || len(r.Diagnostics) != 3 {
				t.Fatal(r)
			}
			for _, d := range r.Diagnostics {
				if d.Level != "warning" || !strings.Contains(d.Message, "claw trust") {
					t.Fatal(d)
				}
			}
		}
	}
	check(false)
	trustWorkspace(t, home, workspace)
	check(true)
	if err := project.NewTrustStore(home).Revoke(workspace); err != nil {
		t.Fatal(err)
	}
	check(false)
	// Broken registry never re-enables project resources.
	putResource(t, filepath.Join(home, "trusted-projects.json"), "bad JSON")
	r, err := loader.Load(workspace)
	if err != nil || r.ProjectTrusted || len(r.Skills) != 0 || len(r.Diagnostics) != 4 {
		t.Fatal(r, err)
	}
}
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestProjectTrustNoUntrustedFileReads(t *testing.T) {
	workspace, home := t.TempDir(), t.TempDir()
	// Even invalid resources are not parsed or followed until trust is granted.
	if err := os.MkdirAll(filepath.Join(workspace, ".claw", "SYSTEM.md"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := (ResourceLoader{Home: home}).Load(workspace)
	if err != nil || len(r.Diagnostics) != 1 || r.Diagnostics[0].Level != "warning" {
		t.Fatal(r, err)
	}
	trustWorkspace(t, home, workspace)
	r, err = (ResourceLoader{Home: home}).Load(workspace)
	if err == nil || len(r.Diagnostics) != 1 || r.Diagnostics[0].Level != "error" {
		t.Fatal(r, err)
	}
}
