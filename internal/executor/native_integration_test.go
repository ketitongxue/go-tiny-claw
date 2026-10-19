package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Platform smoke is separate from the backend-agnostic security contract:
// a usable system Go SDK is a workload dependency, not an Executor capability.
func TestNativeIntegrationToolchain(t *testing.T) {
	e := integrationNative(t, CodingPolicy())
	work := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":      "module nativesmoke\n\ngo 1.26.2\n",
		"sum.go":      "package nativesmoke\nfunc Sum(a,b int) int { return a+b }\n",
		"sum_test.go": "package nativesmoke\nimport \"testing\"\nfunc TestSum(t *testing.T) { if Sum(1,2)!=3 { t.Fatal(\"sum\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "init", "--quiet", "--template=", work).CombinedOutput(); err != nil {
		t.Fatalf("git fixture: %v (%s)", err, out)
	}
	r, err := e.Execute(context.Background(), Request{WorkDir: work, Command: `pwd && ls && git status --porcelain && go test ./... && printf created > note && printf changed > note && test "$(cat note)" = changed`}, nil)
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("native system toolchain: %+v (%v)", r, err)
	}
}

func TestNativeIntegrationIgnoresLongHostTemp(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("long-host-temp-", 12))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	e := integrationNative(t, CodingPolicy())
	r, err := e.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: `printf '%s\n%s\n' "$HOME" "$TMPDIR"`}, nil)
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("long host TMPDIR broke SRT: %+v (%v)", r, err)
	}
	paths := strings.Fields(r.Stdout)
	if len(paths) != 2 || !strings.HasPrefix(paths[0], canonicalPotentialPath("/tmp")+"/go-tiny-claw-sandbox-") || filepath.Dir(paths[0]) != filepath.Dir(paths[1]) || strings.Contains(r.Stdout, long) {
		t.Fatalf("host temp inherited: %+v", r)
	}
	if _, err := os.Stat(filepath.Dir(paths[0])); !os.IsNotExist(err) {
		t.Fatalf("private runtime leaked: %v", err)
	}
}

func TestNativeIntegrationRuntimeCleanup(t *testing.T) {
	e := integrationNative(t, CodingPolicy())
	for _, mode := range []string{"success", "nonzero", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			// Keep SRT's internal Unix socket paths within macOS sockaddr_un.
			parent, err := os.MkdirTemp("/tmp", "claw-clean-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(parent)
			e.runtimeBase = parent
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := Request{WorkDir: t.TempDir(), Command: "echo ready; echo private > \"$HOME/file\"; echo temporary > \"$TMPDIR/file\""}
			if mode == "nonzero" {
				req.Command += "; exit 7"
			}
			if mode == "timeout" || mode == "cancel" {
				req.Command += "; sleep 300 & wait"
			}
			if mode == "timeout" {
				req.Timeout = 2 * time.Second
			}
			r, err := e.Execute(ctx, req, func(p []byte) {
				if mode == "cancel" && strings.Contains(string(p), "ready") {
					cancel()
				}
			})
			switch mode {
			case "success":
				if err != nil || r.ExitCode != 0 {
					t.Fatalf("%+v (%v)", r, err)
				}
			case "nonzero":
				if err != nil || r.ExitCode != 7 {
					t.Fatalf("%+v (%v)", r, err)
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			entries, err := filepath.Glob(filepath.Join(parent, "go-tiny-claw-sandbox-*"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("runtime leaked: %v (%v)", entries, err)
			}
		})
	}
}

func TestNativeRuntimeStartupAndSettingsFailuresCleanup(t *testing.T) {
	for _, mode := range []string{"missing-executable", "unsafe-settings"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			t.Setenv("TMPDIR", parent)
			e := fakeSRT(t, CodingPolicy(), "exit 0")
			var prepare func(string) error
			if mode == "missing-executable" {
				if err := os.Remove(e.binary); err != nil {
					t.Fatal(err)
				}
			} else {
				prepare = func(dir string) error { return os.Symlink("/dev/null", filepath.Join(dir, "settings.json")) }
			}
			_, err := e.execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "echo never"}, nil, prepare)
			if err == nil {
				t.Fatal("startup/settings failure accepted")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatalf("runtime leaked: %v", entries)
			}
		})
	}
}
