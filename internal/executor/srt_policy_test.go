package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSRTFilesystemPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAW_HOME", filepath.Join(home, "agent-home"))
	for _, policy := range []SandboxPolicy{CodingPolicy(), ExplorerPolicy()} {
		work, temp := filepath.Join(home, `项目, 'quoted'`), t.TempDir()
		settings, err := buildSRTSettings(work, temp, policy)
		if err != nil {
			t.Fatal(err)
		}
		if len(settings.Git.SafeDirectories) != 1 || settings.Git.SafeDirectories[0] != work {
			t.Fatal("Git trust must be scoped to the canonical workspace")
		}
		if settings.Filesystem.DenyRead[0].Path != "/" || len(settings.Network.AllowedDomains) != 0 || settings.Network.AllowAllUnixSockets || settings.EnableWeakerNetworkIsolation {
			t.Fatalf("unsafe settings: %+v", settings)
		}
		write := false
		for _, path := range settings.Filesystem.AllowWrite {
			if path.Path == work {
				write = true
			}
		}
		if write != (policy.WorkspaceMode == WorkspaceReadWrite) {
			t.Fatal("wrong workspace permissions")
		}
		for _, path := range settings.Filesystem.AllowRead {
			if path.Path == home || path.Path == "/tmp" || path.Path == "/var" || path.Path == "/private/var" || path.Path == "/etc" || path.Path == "/private/etc" || path.Path == "/private/etc/ssl" || path.Path == "/Library" || path.Path == "/System" || path.Path == "/opt/homebrew" {
				t.Fatalf("broad allow: %s", path.Path)
			}
			if !path.Literal {
				t.Fatal("path is interpreted as a glob")
			}
		}
		data, err := json.Marshal(settings)
		if err != nil || !strings.Contains(string(data), `"literal":true`) {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatal(err)
	}
}
