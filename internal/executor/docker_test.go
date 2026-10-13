package executor

import (
	"context"
	"encoding/csv"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func optionValue(args []string, option string) string {
	for i, arg := range args {
		if arg == option && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestBuildDockerArgsPolicies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy SandboxPolicy
		memory string
		cpu    string
		pids   string
		ro     bool
	}{
		{"coding", CodingPolicy(), "1024m", "2", "128", false},
		{"explorer", ExplorerPolicy(), "512m", "1", "64", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			args, err := buildDockerArgs(Request{WorkDir: workDir}, tc.policy)
			if err != nil {
				t.Fatal(err)
			}
			for flag, want := range map[string]string{
				"--network": "none", "--cap-drop": "ALL", "--security-opt": "no-new-privileges=true",
				"--memory": tc.memory, "--memory-swap": tc.memory, "--cpus": tc.cpu, "--pids-limit": tc.pids,
				"--tmpfs": "/tmp:rw,nosuid,nodev,size=128m,mode=1777", "--workdir": "/workspace", "--pull": "never",
			} {
				if got := optionValue(args, flag); got != want {
					t.Errorf("%s=%q want=%q", flag, got, want)
				}
			}
			for _, flag := range []string{"run", "--rm", "--init", "--read-only"} {
				if !containsArg(args, flag) {
					t.Errorf("missing %s in %v", flag, args)
				}
			}
			mount, err := csv.NewReader(strings.NewReader(optionValue(args, "--mount"))).Read()
			realDir, _ := filepath.EvalSymlinks(workDir)
			want := []string{"type=bind", "src=" + realDir, "dst=/workspace", "bind-recursive=disabled", "bind-propagation=rprivate"}
			if tc.ro {
				want = append(want, "readonly")
			}
			if err != nil || !reflect.DeepEqual(mount, want) {
				t.Fatalf("mount=%v want=%v error=%v", mount, want, err)
			}
			for _, forbidden := range []string{"--privileged", "--volume", "-v", "--env-file"} {
				if containsArg(args, forbidden) {
					t.Errorf("unsafe option: %s", forbidden)
				}
			}
		})
	}
}

func containsArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func TestBuildDockerArgsCanonicalPathAndCSV(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), `project with spaces,readonly,"quoted"`)
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(workDir, link); err != nil {
		t.Fatal(err)
	}
	args, err := buildDockerArgs(Request{WorkDir: link}, CodingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	mount, err := csv.NewReader(strings.NewReader(optionValue(args, "--mount"))).Read()
	realDir, _ := filepath.EvalSymlinks(workDir)
	if err != nil || len(mount) != 5 || mount[1] != "src="+realDir || containsArg(mount, "readonly") {
		t.Fatalf("mount path/options corrupted: %v (%v)", mount, err)
	}
}

func TestBuildDockerArgsRejectsUnsafeWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	secret := filepath.Join(home, ".ssh", "nested")
	if err := os.MkdirAll(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "/", home, filepath.Dir(home), secret, file, filepath.Join(t.TempDir(), "missing")} {
		if _, err := buildDockerArgs(Request{WorkDir: path}, CodingPolicy()); !errors.Is(err, ErrInvalidWorkDir) {
			t.Errorf("path=%q should be rejected: %v", path, err)
		}
	}
	// A normal project below HOME is allowed; cwd is not mistaken for HOME.
	project := filepath.Join(home, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := buildDockerArgs(Request{WorkDir: project}, CodingPolicy()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "secret-link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, err := buildDockerArgs(Request{WorkDir: link}, CodingPolicy()); !errors.Is(err, ErrInvalidWorkDir) {
		t.Fatalf("symlink bypassed sensitive-directory guard: %v", err)
	}
}

func TestBuildDockerArgsRejectsHostSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket workspace guard")
	}
	// Unix sockets have a short path limit (notably on macOS); t.TempDir's
	// test-name prefix can exceed it before the socket name is appended.
	workDir, err := os.MkdirTemp("", "claw-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workDir)
	listener, err := net.Listen("unix", filepath.Join(workDir, "docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := buildDockerArgs(Request{WorkDir: workDir}, CodingPolicy()); !errors.Is(err, ErrInvalidWorkDir) {
		t.Fatalf("host socket was exposed: %v", err)
	}
}

func TestDockerArgsEnvironmentAndNetwork(t *testing.T) {
	t.Setenv("ZHIPU_API_KEY", "host-model-secret")
	t.Setenv("GITHUB_TOKEN", "host-github-secret")
	req := Request{WorkDir: t.TempDir(), Env: map[string]string{"LANG": "C.UTF-8", "TERM": "xterm"}}
	args, err := buildDockerArgs(req, CodingPolicy())
	if err != nil || strings.Contains(strings.Join(args, " "), "host-") || optionValue(args, "--env") != "LANG=C.UTF-8" || !containsArg(args, "TERM=xterm") {
		t.Fatalf("args=%v error=%v", args, err)
	}
	req.Env["ZHIPU_API_KEY"] = "explicit-but-not-allowed"
	if _, err := buildDockerArgs(req, CodingPolicy()); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("disallowed request env accepted: %v", err)
	}
	policy := CodingPolicy()
	policy.NetworkEnabled = true
	args, err = buildDockerArgs(Request{WorkDir: req.WorkDir}, policy)
	if err != nil || optionValue(args, "--network") != "bridge" {
		t.Fatalf("explicit network policy=%v (%v)", args, err)
	}
}

func fakeDocker(t *testing.T, policy SandboxPolicy, script string) *DockerExecutor {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fake-docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	e, err := NewDockerExecutor(DefaultImage, policy)
	if err != nil {
		t.Fatal(err)
	}
	e.dockerBinary = binary
	return e
}

func TestDockerAvailableChecksDaemon(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		wantError    bool
	}{
		{"available", "echo 28.0.0", false},
		{"daemon-down", "echo 'Cannot connect to the Docker daemon' >&2; exit 1", true},
		{"client-only", "exit 0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fakeDocker(t, CodingPolicy(), tc.script)
			err := dockerAvailable(context.Background(), e.dockerBinary)
			if (err != nil) != tc.wantError || (tc.wantError && !errors.Is(err, ErrDockerUnavailable)) {
				t.Fatalf("availability error=%v", err)
			}
		})
	}
	if err := dockerAvailable(context.Background(), filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrDockerUnavailable) {
		t.Fatalf("missing CLI accepted: %v", err)
	}
}

func TestDockerImageCheckAndConstructorValidation(t *testing.T) {
	for _, image := range []string{"", "-bad", "image with spaces", "image;echo hacked", "image\n"} {
		if _, err := NewDockerExecutor(image, CodingPolicy()); err == nil {
			t.Errorf("invalid image %q accepted", image)
		}
	}
	if _, err := NewDockerExecutor(DefaultImage, SandboxPolicy{}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("invalid policy accepted: %v", err)
	}
	for _, tc := range []struct {
		script    string
		wantError bool
	}{
		{"echo sha256:test", false}, {"echo missing >&2; exit 1", true},
	} {
		e := fakeDocker(t, CodingPolicy(), tc.script)
		err := e.CheckImage(context.Background())
		if (err != nil) != tc.wantError || (tc.wantError && !strings.Contains(err.Error(), "docker build -f Dockerfile.sandbox")) {
			t.Fatalf("image check error=%v", err)
		}
	}
	policy := CodingPolicy()
	e, err := NewDockerExecutor("custom-image:v1", policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.AllowedEnv[0] = "ZHIPU_API_KEY"
	if e.image != "custom-image:v1" || e.policy.AllowedEnv[0] != "PATH" {
		t.Fatal("executor image/policy configuration was lost or mutated")
	}
}

func TestDockerExecutePreservesCommandAndExitCode(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "arguments")
	t.Setenv("FAKE_DOCKER_LOG", logFile)
	e := fakeDocker(t, CodingPolicy(), `printf '%s\n' "$@" >> "$FAKE_DOCKER_LOG"
case "$1" in
  run) printf hello; printf problem >&2; exit 7 ;;
  rm) exit 0 ;;
  *) exit 2 ;;
esac`)
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	command := "echo hello; touch '" + marker + "'; $(false)"
	result, err := e.Execute(context.Background(), Request{Command: command, WorkDir: t.TempDir()}, nil)
	if err != nil || result.ExitCode != 7 || result.Stdout != "hello" || result.Stderr != "problem" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	log, err := os.ReadFile(logFile)
	if err != nil || !strings.Contains(string(log), e.image+"\nbash\n-lc\n"+command+"\nrm\n--force\ngo-tiny-claw-") {
		t.Fatalf("command was not passed as one argument, or cleanup missing: %s (%v)", log, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell injection on host: %v", err)
	}
}

func TestDockerReservedExitCodesAndInfrastructureErrors(t *testing.T) {
	for _, tc := range []struct {
		name, run string
		wantError bool
	}{
		{"command-125", "exit 125", false},
		{"command-126", "exit 126", false},
		{"command-127", "exit 127", false},
		{"daemon", "echo 'docker: Error response from daemon: unavailable' >&2; exit 125", true},
		{"container-start", "echo 'docker: Error response from daemon: exec failed' >&2; exit 127", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fakeDocker(t, CodingPolicy(), "case \"$1\" in run) "+tc.run+" ;; rm) exit 0 ;; esac")
			result, err := e.Execute(context.Background(), Request{Command: "exit", WorkDir: t.TempDir()}, nil)
			if (err != nil) != tc.wantError || result.ExitCode < 125 || result.ExitCode > 127 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestDockerCancelAndTimeoutRemoveContainer(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "timeout"}[timeout], func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "container-alive")
			t.Setenv("FAKE_CONTAINER_STATE", state)
			e := fakeDocker(t, CodingPolicy(), `case "$1" in
  run) touch "$FAKE_CONTAINER_STATE"; echo ready; sleep 10 & wait ;;
  rm) rm -f "$FAKE_CONTAINER_STATE"; exit 0 ;;
esac`)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := Request{Command: "sleep 10", WorkDir: t.TempDir()}
			if timeout {
				req.Timeout = 500 * time.Millisecond
			}
			result, err := e.Execute(ctx, req, func(p []byte) {
				if !timeout && strings.Contains(string(p), "ready") {
					cancel()
				}
			})
			want := context.Canceled
			if timeout {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) || result.Duration > 3*time.Second || !strings.Contains(result.Stdout, "ready") {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("container left alive after cancellation: %v", err)
			}
		})
	}
}

func TestDockerCleanupFailureIsNotSilent(t *testing.T) {
	e := fakeDocker(t, CodingPolicy(), `case "$1" in run) echo hello ;; rm) echo 'daemon unavailable' >&2; exit 1 ;; esac`)
	result, err := e.Execute(context.Background(), Request{Command: "echo hello", WorkDir: t.TempDir()}, nil)
	if err == nil || !strings.Contains(err.Error(), "cleanup") || result.Stdout != "hello\n" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestDockerWorkspaceValidationRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildDockerArgsContext(ctx, Request{WorkDir: t.TempDir()}, CodingPolicy()); !errors.Is(err, context.Canceled) {
		t.Fatalf("workspace validation ignored cancellation: %v", err)
	}
	e := fakeDocker(t, CodingPolicy(), "exit 0")
	if err := e.ValidateWorkspace(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup validation ignored cancellation: %v", err)
	}
}

func TestDockerCleanupStopsDescendantsHoldingPipes(t *testing.T) {
	e := fakeDocker(t, CodingPolicy(), `case "$1" in
  run) echo hello ;;
  rm) sleep 30 & exit 0 ;;
esac`)
	start := time.Now()
	result, err := e.Execute(context.Background(), Request{Command: "echo hello", WorkDir: t.TempDir()}, nil)
	if err == nil || !strings.Contains(err.Error(), "cleanup") || time.Since(start) > 3*time.Second || result.Stdout != "hello\n" {
		t.Fatalf("cleanup not bounded: result=%+v error=%v duration=%v", result, err, time.Since(start))
	}
}

func TestDockerCanceledBeforeStart(t *testing.T) {
	e := fakeDocker(t, CodingPolicy(), "echo unexpected >&2; exit 1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := e.Execute(ctx, Request{Command: "echo hello", WorkDir: t.TempDir()}, nil)
	if !errors.Is(err, context.Canceled) || result.ExitCode != -1 || result.Stdout != "" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
