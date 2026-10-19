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

type sandboxFactory func(*testing.T, SandboxPolicy) Executor

func integrationNative(t *testing.T, policy SandboxPolicy) *NativeExecutor {
	t.Helper()
	if _, err := exec.LookPath("srt"); err != nil {
		t.Skip("SKIPPED: srt not installed; npm install -g @anthropic-ai/sandbox-runtime")
	}
	e, err := NewNativeExecutor(context.Background(), t.TempDir(), policy)
	if err != nil {
		t.Fatalf("installed SRT is unusable (not a skip): %v", err)
	}
	return e
}

func TestSandboxContract(t *testing.T) {
	for _, backend := range []struct {
		name    string
		factory sandboxFactory
	}{
		{"Docker", func(t *testing.T, p SandboxPolicy) Executor { return integrationDocker(t, p) }},
		{"Native", func(t *testing.T, p SandboxPolicy) Executor { return integrationNative(t, p) }},
	} {
		t.Run(backend.name, func(t *testing.T) {
			// Probe once before making fixtures, so an unavailable backend has
			// an explicit SKIP, rather than many nominally successful subtests.
			backend.factory(t, CodingPolicy())
			sandboxContract(t, backend.factory)
		})
	}
}

func sandboxContract(t *testing.T, factory sandboxFactory) {
	t.Helper()
	run := func(t *testing.T, e Executor, work, command string) Result {
		t.Helper()
		r, err := e.Execute(context.Background(), Request{WorkDir: work, Command: command}, nil)
		if err != nil {
			t.Fatalf("infrastructure: %v; result=%+v", err, r)
		}
		return r
	}
	t.Run("Filesystem", func(t *testing.T) {
		root := t.TempDir()
		work, sibling := filepath.Join(root, "workspace"), filepath.Join(root, "sibling")
		for _, dir := range []string{work, sibling} {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(work, "read.txt"), []byte("workspace-evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
		e := factory(t, CodingPolicy())
		for _, tc := range []struct {
			name, command, stdout string
			success               bool
		}{
			{"WorkspaceRead", "cat read.txt", "workspace-evidence", true},
			{"CodingWrite", "echo written > created; cat created", "written\n", true},
			{"NestedShell", "bash -c 'cat read.txt'", "workspace-evidence", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := run(t, e, work, tc.command)
				if (r.ExitCode == 0) != tc.success || r.Stdout != tc.stdout {
					t.Fatalf("%+v", r)
				}
			})
		}
		for _, target := range []string{filepath.Join(root, "parent-write"), filepath.Join(sibling, "sibling-write")} {
			run(t, e, work, "echo escaped > "+shellQuote(canonicalPotentialPath(target)))
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("host write escaped: %v", err)
			}
			// Some OS runtimes replace read-denied directories with anonymous
			// tmpfs. Exit status alone is not the host-isolation oracle: verify
			// both creation and overwrite cannot affect the real parent/sibling.
			if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			run(t, e, work, "echo escaped > "+shellQuote(canonicalPotentialPath(target)))
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "unchanged" {
				t.Fatalf("host overwrite escaped: %q (%v)", data, err)
			}
			if r := run(t, e, work, "cat "+shellQuote(canonicalPotentialPath(target))); r.ExitCode == 0 || strings.Contains(r.Stdout, "unchanged") {
				t.Fatalf("parent/sibling read escaped: %+v", r)
			}
		}
		ro := factory(t, ExplorerPolicy())
		for _, command := range []string{"echo hacked > read.txt", "rm read.txt", "mkdir directory", "echo hacked > new-file", "bash -c 'echo hacked > nested'"} {
			if r := run(t, ro, work, command); r.ExitCode == 0 {
				t.Fatalf("Explorer wrote: %s %+v", command, r)
			}
		}
		for _, name := range []string{"new-file", "directory", "nested"} {
			if _, err := os.Stat(filepath.Join(work, name)); !os.IsNotExist(err) {
				t.Fatalf("Explorer created %s: %v", name, err)
			}
		}
		content, err := os.ReadFile(filepath.Join(work, "read.txt"))
		if err != nil || string(content) != "workspace-evidence" {
			t.Fatal("Explorer changed workspace")
		}
	})
	t.Run("Paths", func(t *testing.T) {
		e := factory(t, CodingPolicy())
		for _, name := range []string{"has spaces", "has,comma", `has'and"quotes`, "中文项目", "literal[glob]*?", `综合 spaces, 'and"quotes"`} {
			t.Run(name, func(t *testing.T) {
				work := filepath.Join(t.TempDir(), name)
				if err := os.Mkdir(work, 0o700); err != nil {
					t.Fatal(err)
				}
				r := run(t, e, work, "echo evidence > file; cat file")
				if r.ExitCode != 0 || r.Stdout != "evidence\n" {
					t.Fatalf("%+v", r)
				}
			})
		}
	})
	t.Run("Output", func(t *testing.T) {
		e := factory(t, CodingPolicy())
		for _, tc := range []struct {
			name, command, stdout, stderr string
			exit                          int
		}{
			{"ExitZero", "exit 0", "", "", 0}, {"ExitNonzero", "exit 7", "", "", 7},
			{"Stdout", "printf hello", "hello", "", 0}, {"Stderr", "printf problem >&2", "", "problem", 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := run(t, e, t.TempDir(), tc.command)
				if r.ExitCode != tc.exit || r.Stdout != tc.stdout || r.Stderr != tc.stderr || r.Duration <= 0 {
					t.Fatalf("%+v", r)
				}
			})
		}
		var streamed strings.Builder
		r, err := e.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "printf out; printf err >&2"}, func(p []byte) { streamed.Write(p) })
		if err != nil || r.Stdout != "out" || r.Stderr != "err" || streamed.Len() != 6 || !strings.Contains(streamed.String(), "out") || !strings.Contains(streamed.String(), "err") {
			t.Fatalf("%+v (%v), streamed=%q", r, err, streamed.String())
		}
	})
	t.Run("Environment", func(t *testing.T) {
		for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ZHIPU_API_KEY", "DEEPSEEK_API_KEY", "GITHUB_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "SSH_AUTH_SOCK", "FEISHU_APP_SECRET", "UNKNOWN_VALUE"} {
			t.Setenv(key, "test-secret-not-inherited")
		}
		p := CodingPolicy()
		p.AllowedEnv = append(p.AllowedEnv, "CONTRACT_VALUE")
		e := factory(t, p)
		command := `test "$CONTRACT_VALUE" = 'explicit value' && test -z "${OPENAI_API_KEY+x}${ANTHROPIC_API_KEY+x}${ZHIPU_API_KEY+x}${DEEPSEEK_API_KEY+x}${GITHUB_TOKEN+x}${AWS_ACCESS_KEY_ID+x}${AWS_SECRET_ACCESS_KEY+x}${AWS_SESSION_TOKEN+x}${SSH_AUTH_SOCK+x}${FEISHU_APP_SECRET+x}${UNKNOWN_VALUE+x}" && test -n "$HOME" && test -n "$TMPDIR" && test -d "$HOME" && test -d "$TMPDIR" && echo home > "$HOME/file" && echo tmp > "$TMPDIR/file" && printf '%s\n' "$HOME" "$TMPDIR"`
		r, err := e.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: command, Env: map[string]string{"CONTRACT_VALUE": "explicit value"}}, nil)
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("environment: %+v (%v)", r, err)
		}
		home, _ := os.UserHomeDir()
		if strings.Contains(r.Stdout, home+"\n") {
			t.Fatal("real HOME inherited")
		}
		if _, err := e.Execute(context.Background(), Request{WorkDir: t.TempDir(), Env: map[string]string{"UNKNOWN_VALUE": "explicit"}}, nil); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("non-allowlisted env accepted: %v", err)
		}
	})
	t.Run("CredentialsAndSymlinks", func(t *testing.T) {
		e := factory(t, CodingPolicy())
		// Fixtures only; never open the developer's real credentials. Preserve
		// Docker's context config while using a synthetic protected HOME.
		if os.Getenv("DOCKER_CONFIG") == "" {
			home, _ := os.UserHomeDir()
			t.Setenv("DOCKER_CONFIG", filepath.Join(home, ".docker"))
		}
		home, claw, work := t.TempDir(), t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAW_HOME", claw)
		for i, rel := range []string{".claw/auth.json", ".claw/host.json", ".claw/sessions/session.jsonl", ".ssh/id_rsa", ".aws/credentials", ".kube/config", ".config/credentials", "private-file"} {
			path := filepath.Join(home, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("fixture-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			path = canonicalPotentialPath(path)
			link := filepath.Join(work, "leak"+string(rune('a'+i)))
			if err := os.Symlink(path, link); err != nil {
				t.Fatal(err)
			}
			for _, target := range []string{path, filepath.Base(link)} {
				r := run(t, e, work, "cat "+shellQuote(target))
				if r.ExitCode == 0 || strings.Contains(r.Stdout, "fixture-secret") {
					t.Fatalf("credential visible via %s: %+v", target, r)
				}
			}
		}
		path := filepath.Join(claw, "auth.json")
		if err := os.WriteFile(path, []byte("fixture-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if r := run(t, e, work, "cat "+shellQuote(canonicalPotentialPath(path))); r.ExitCode == 0 {
			t.Fatal("CLAW_HOME visible")
		}
	})
	t.Run("Cancellation", func(t *testing.T) {
		e := factory(t, CodingPolicy())
		for _, mode := range []string{"timeout", "cancel", "child"} {
			t.Run(mode, func(t *testing.T) {
				work := t.TempDir()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				req := Request{WorkDir: work, Command: "echo ready; sleep 300 & wait"}
				want := context.Canceled
				if mode == "timeout" {
					req.Timeout = 2 * time.Second
					want = context.DeadlineExceeded
				}
				if mode == "child" {
					req.Command = "(sleep 1; echo survived > child-survived) >/dev/null 2>&1 & echo ready; wait"
				}
				r, err := e.Execute(ctx, req, func(p []byte) {
					if mode != "timeout" && strings.Contains(string(p), "ready") {
						cancel()
					}
				})
				if !errors.Is(err, want) || r.Duration > 9*time.Second {
					t.Fatalf("%+v (%v), want %v", r, err, want)
				}
				if mode == "child" {
					time.Sleep(1200 * time.Millisecond)
					if _, err := os.Stat(filepath.Join(work, "child-survived")); !os.IsNotExist(err) {
						t.Fatal("child survived cancellation")
					}
				}
			})
		}
	})
}
