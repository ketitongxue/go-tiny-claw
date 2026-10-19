package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeSRT(t *testing.T, policy SandboxPolicy, script string) *NativeExecutor {
	t.Helper()
	path := filepath.Join(t.TempDir(), "srt")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 0.0.80; exit; fi\ntrap 'x=$?; printf \"%s\\n\" \"$x\" > \"${2%/*}/exit-status\"' EXIT\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := srtAvailable(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	// Fake CLI tests validate invocation, not OS enforcement. Production
	// construction additionally requires the real compatibility probe.
	return &NativeExecutor{binary: path, policy: policy, runtimeBase: os.TempDir()}
}

func TestSRTAvailable(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		fail         bool
	}{
		{"valid", "echo 0.0.80", false},
		{"broken", "exit 1", true},
		{"empty", "exit 0", true},
		{"invalid-version", "echo unsupported", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "srt")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script), 0o700); err != nil {
				t.Fatal(err)
			}
			err := srtAvailable(context.Background(), path)
			if (err != nil) != tc.fail || tc.fail && !errors.Is(err, ErrNativeUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := srtAvailable(context.Background(), filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrNativeUnavailable) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SRTAvailable(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSRTMustRefuseBadExplicitSettings(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		fail         bool
	}{
		{"refused", "echo invalid-settings >&2; exit 1", false},
		{"default-fallback", "printf invalid-settings-command-ran; exit 0", true},
		{"ran-then-failed", "printf invalid-settings-command-ran; exit 1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fakeSRT(t, CodingPolicy(), tc.script)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := e.checkSettingsFailure(ctx, t.TempDir(), t.TempDir())
			if (err != nil) != tc.fail || tc.fail && !errors.Is(err, ErrNativeUnavailable) {
				t.Fatalf("settings fail-closed check: %v", err)
			}
		})
	}
}

func TestSRTVersionTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "srt")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 300 & wait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := srtAvailable(ctx, path); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("unbounded version probe: %v", err)
	}
}

func TestNativeRejectsWorkspaceInstallBeforeProbes(t *testing.T) {
	for _, layout := range []string{"binary", "binary-symlink", "workspace-symlink", "npm-in-workspace", "workspace-in-npm"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "workspace")
			binary := filepath.Join(work, "srt")
			var packageRoot string
			switch layout {
			case "npm-in-workspace":
				packageRoot = filepath.Join(work, "node_modules", "@anthropic-ai", "sandbox-runtime")
			case "workspace-in-npm":
				packageRoot = filepath.Join(root, "sandbox-runtime")
				work = filepath.Join(packageRoot, "project")
			}
			if packageRoot != "" {
				binary = filepath.Join(packageRoot, "dist", "cli.js")
			}
			for _, dir := range []string{work, filepath.Dir(binary)} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(root, "srt-executed-on-host")
			script := "#!/bin/sh\nprintf unexpected > " + shellQuote(marker) + "\necho 0.0.80\n"
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if packageRoot != "" {
				if err := os.WriteFile(filepath.Join(packageRoot, "package.json"), []byte(`{"name":"@anthropic-ai/sandbox-runtime"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if layout == "binary-symlink" || layout == "workspace-symlink" {
				target := binary
				if layout == "workspace-symlink" {
					target = work
				}
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(target, alias); err != nil {
					t.Fatal(err)
				}
				if layout == "binary-symlink" {
					binary = alias
				} else {
					work = alias
				}
			}
			e, err := newNativeExecutor(context.Background(), binary, work, CodingPolicy())
			if e != nil || !errors.Is(err, ErrInvalidWorkDir) {
				t.Fatalf("unsafe installation accepted: %v (%v)", e, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("unsafe SRT ran before path validation: %v", err)
			}
		})
	}
}

func TestNativeRejectsUnverifiedPackageBeforeProbes(t *testing.T) {
	for _, tc := range []struct {
		name, manifest string
		protected      bool
		want           error
	}{
		{"missing-manifest", "", false, ErrNativeUnavailable},
		{"invalid-manifest", "{", false, ErrNativeUnavailable},
		{"wrong-package", `{"name":"untrusted-package"}`, false, ErrNativeUnavailable},
		{"protected-install", `{"name":"@anthropic-ai/sandbox-runtime"}`, true, ErrInvalidPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "dist", "cli.js")
			if err := os.Mkdir(filepath.Dir(binary), 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "srt-executed-on-host")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf unexpected > "+shellQuote(marker)+"\necho 0.0.80\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(tc.manifest), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.protected {
				t.Setenv("CLAW_HOME", root)
			}
			e, err := newNativeExecutor(context.Background(), binary, t.TempDir(), CodingPolicy())
			if e != nil || !errors.Is(err, tc.want) {
				t.Fatalf("unverified package accepted: %v (%v)", e, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("SRT ran before package validation: %v", err)
			}
		})
	}
}

func TestNativeRejectsInvalidWorkspaceBeforeProbes(t *testing.T) {
	for _, tc := range []struct{ name, work string }{
		{"empty", ""}, {"missing", filepath.Join(t.TempDir(), "missing")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binary, marker := filepath.Join(root, "srt"), filepath.Join(root, "srt-executed-on-host")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf unexpected > "+shellQuote(marker)+"\necho 0.0.80\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if e, err := newNativeExecutor(context.Background(), binary, tc.work, CodingPolicy()); e != nil || !errors.Is(err, ErrInvalidWorkDir) {
				t.Fatalf("invalid workspace accepted: %v (%v)", e, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("SRT ran before workspace validation: %v", err)
			}
		})
	}
}

func TestNativeExplicitEnvOnlyAppliesInsideSandbox(t *testing.T) {
	p := CodingPolicy()
	p.AllowedEnv = append(p.AllowedEnv, "NODE_OPTIONS")
	e := fakeSRT(t, p, `test -z "${NODE_OPTIONS+x}" || exit 90; printf '%s' "$4"`)
	r, err := e.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "echo inside", Env: map[string]string{"NODE_OPTIONS": "--require /workspace/untrusted.js"}}, nil)
	if err != nil || r.ExitCode != 0 || !strings.Contains(r.Stdout, "/usr/bin/env 'NODE_OPTIONS=--require /workspace/untrusted.js'") {
		t.Fatalf("trusted SRT environment contaminated: %+v (%v)", r, err)
	}
}

func TestNativePrivateRuntimeAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "nonzero", "srt-failure", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			t.Setenv("TMPDIR", parent)
			t.Setenv("GITHUB_TOKEN", "must-not-inherit")
			script := `test "$1" = --settings && test "$3" = -c || exit 99
p="$2"
r="${p%/*}"
for d in "$r" "$r/home" "$r/tmp"; do test -d "$d" || exit 98; done
test "$HOME" = "$r/home" && test "$TMPDIR" = "$r/tmp" || exit 97
test -z "${GITHUB_TOKEN+x}${UNKNOWN_VALUE+x}" || exit 96
printf runtime-ready
printf diagnostic >&2
`
			switch mode {
			case "nonzero":
				script += "exit 7"
			case "srt-failure":
				script += "trap - EXIT; exit 1"
			case "timeout", "cancel":
				script += "sleep 10 & wait"
			}
			e := fakeSRT(t, CodingPolicy(), script)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := Request{Command: "echo hello", WorkDir: t.TempDir()}
			if mode == "timeout" {
				req.Timeout = 200 * time.Millisecond
			}
			result, err := e.Execute(ctx, req, func(p []byte) {
				if strings.Contains(string(p), "runtime-ready") {
					runtimes, _ := filepath.Glob(filepath.Join(parent, "go-tiny-claw-sandbox-*"))
					if len(runtimes) != 1 {
						t.Errorf("runtimes=%v", runtimes)
						return
					}
					for name, want := range map[string]os.FileMode{"": 0o700, "home": 0o700, "tmp": 0o700, "settings.json": 0o600, "exit-status": 0o600} {
						info, statErr := os.Stat(filepath.Join(runtimes[0], name))
						if statErr != nil || info.Mode().Perm() != want {
							t.Errorf("permissions %s: %v", name, statErr)
						}
					}
					if mode == "cancel" {
						cancel()
					}
				}
			})
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) || mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if mode == "success" && (err != nil || result.ExitCode != 0 || result.Stdout != "runtime-ready" || result.Stderr != "diagnostic") {
				t.Fatalf("%+v (%v)", result, err)
			}
			if mode == "srt-failure" && err == nil {
				t.Fatal("SRT failure not classified as infrastructure error")
			}
			if mode == "nonzero" && (err != nil || result.ExitCode != 7) {
				t.Fatalf("%+v (%v)", result, err)
			}
			if result.Duration <= 0 {
				t.Fatal("missing duration")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatalf("runtime leaked: %v", entries)
			}
		})
	}
}

func TestNativeCompletionDoesNotFollowUntrustedPaths(t *testing.T) {
	for _, target := range []string{"fixture", "device", "fifo"} {
		t.Run(target, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "host-file")
			if err := os.WriteFile(fixture, []byte("0\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			script := "trap - EXIT; p=\"${2%/*}/exit-status\"; rm -f \"$p\"; "
			switch target {
			case "fixture":
				script += "ln -s " + shellQuote(fixture) + " \"$p\""
			case "device":
				script += "ln -s /dev/zero \"$p\""
			case "fifo":
				script += "mkfifo \"$p\""
			}
			e := fakeSRT(t, CodingPolicy(), script)
			start := time.Now()
			_, err := e.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "untrusted"}, nil)
			if err == nil || time.Since(start) > 3*time.Second {
				t.Fatalf("completion followed unsafe path: %v", err)
			}
			data, err := os.ReadFile(fixture)
			if err != nil || string(data) != "0\n" {
				t.Fatal("host fixture changed")
			}
		})
	}
}

func TestNativeCommandWorkdirAndRequestEnvironment(t *testing.T) {
	e := fakeSRT(t, CodingPolicy(), `printf '%s\n' "$PWD" "$4"`)
	workDir := filepath.Join(t.TempDir(), `项目 with spaces, "quotes"`)
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	command := "touch " + shellQuote(marker) + "; $(false)"
	result, err := e.Execute(context.Background(), Request{WorkDir: workDir, Command: command, Env: map[string]string{"LANG": "test-value"}}, nil)
	realDir, _ := filepath.EvalSymlinks(workDir)
	if err != nil || !strings.HasPrefix(result.Stdout, realDir+"\n") || !strings.Contains(result.Stdout, shellQuote(command)) || !strings.Contains(result.Stdout, "LANG=test-value") {
		t.Fatalf("%+v (%v)", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command executed on host")
	}
	if _, err := e.Execute(context.Background(), Request{WorkDir: workDir, Env: map[string]string{"UNKNOWN_VALUE": "bad"}}, nil); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal(err)
	}
}
