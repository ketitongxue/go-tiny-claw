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

func integrationDocker(t *testing.T, policy SandboxPolicy) *DockerExecutor {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := DockerAvailable(ctx); err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	image := os.Getenv("CLAW_SANDBOX_TEST_IMAGE")
	if image == "" {
		image = DefaultImage
	}
	e, err := NewDockerExecutor(image, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.CheckImage(ctx); err != nil {
		t.Skipf("build the sandbox test image first: %v", err)
	}
	return e
}

func integrationExecute(t *testing.T, e *DockerExecutor, workDir, command string) Result {
	t.Helper()
	result, err := e.Execute(context.Background(), Request{Command: command, WorkDir: workDir}, nil)
	if err != nil {
		t.Fatalf("Docker execution infrastructure failed: %v\nstdout=%s\nstderr=%s", err, result.Stdout, result.Stderr)
	}
	return result
}

func TestDockerIntegrationWorkspaceAndImage(t *testing.T) {
	e := integrationDocker(t, CodingPolicy())
	workDir := filepath.Join(t.TempDir(), `project with spaces, commas "quotes"`)
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "test.txt"), []byte("workspace-evidence"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := integrationExecute(t, e, workDir, "cat /workspace/test.txt; pwd")
	if result.ExitCode != 0 || result.Stdout != "workspace-evidence/workspace\n" {
		t.Fatalf("workspace unavailable: %+v", result)
	}
	result = integrationExecute(t, e, workDir, "echo hello > /workspace/new.txt")
	content, err := os.ReadFile(filepath.Join(workDir, "new.txt"))
	if result.ExitCode != 0 || err != nil || string(content) != "hello\n" {
		t.Fatalf("coding cannot write workspace: %+v (%v) content=%q", result, err, content)
	}
	result = integrationExecute(t, e, workDir, "go version && git --version && curl --version && rg --version && echo tmpfs > /tmp/temporary")
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "go version") {
		t.Fatalf("sandbox image tools or tmpfs broken: %+v", result)
	}
}

func TestDockerIntegrationExplorerCannotModifyWorkspace(t *testing.T) {
	e := integrationDocker(t, ExplorerPolicy())
	workDir := t.TempDir()
	file := filepath.Join(workDir, "main.go")
	if err := os.WriteFile(file, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"echo hacked > main.go", "rm main.go", "echo hacked > new.txt", "mkdir new-directory"} {
		result := integrationExecute(t, e, workDir, command)
		if result.ExitCode == 0 {
			t.Fatalf("Explorer write succeeded: %q (%+v)", command, result)
		}
		content, err := os.ReadFile(file)
		if err != nil || string(content) != "unchanged" {
			t.Fatalf("Explorer modified existing file: %q (%v)", content, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("Explorer created a new file: %v", err)
	}
}

func TestDockerIntegrationRootFSAndPrivileges(t *testing.T) {
	e := integrationDocker(t, CodingPolicy())
	workDir := t.TempDir()
	result := integrationExecute(t, e, workDir, "touch /root/test")
	if result.ExitCode == 0 {
		t.Fatal("root filesystem unexpectedly writable")
	}
	// Check mount flags as well: a permission error alone is not evidence of RO.
	result = integrationExecute(t, e, workDir, `awk '$2 == "/" && $4 ~ /(^|,)ro(,|$)/ {found=1} END {exit !found}' /proc/mounts && grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status && grep -Eq '^NoNewPrivs:[[:space:]]+1$' /proc/self/status`)
	if result.ExitCode != 0 {
		t.Fatalf("rootfs/capability/privilege hardening missing: %+v", result)
	}
}

func TestDockerIntegrationNetworkDenied(t *testing.T) {
	e := integrationDocker(t, CodingPolicy())
	// Check the namespace has no IPv4 routes, then use a TEST-NET address,
	// not a public service. Some kernels expose dormant tunnel interfaces even
	// with network=none, so requiring literally only "lo" is not portable.
	result := integrationExecute(t, e, t.TempDir(), `awk 'NR > 1 {exit 1}' /proc/net/route && ! curl --noproxy '*' --connect-timeout 1 --max-time 2 http://192.0.2.1:80`)
	if result.ExitCode != 0 {
		t.Fatalf("network isolation missing: %+v", result)
	}
}

func TestDockerIntegrationHostFilesAndSecretsNotInherited(t *testing.T) {
	e := integrationDocker(t, CodingPolicy())
	t.Setenv("ZHIPU_API_KEY", "test-model-secret")
	t.Setenv("GITHUB_TOKEN", "test-github-secret")
	t.Setenv("AWS_ACCESS_KEY_ID", "test-aws-secret")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/host-agent.sock")
	t.Setenv("FEISHU_APP_SECRET", "test-feishu-secret")
	workDir := t.TempDir()
	secretPath := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(secretPath, []byte("host-secret-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	realSecret, err := filepath.EvalSymlinks(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	result := integrationExecute(t, e, workDir, "cat '"+realSecret+"'")
	if result.ExitCode == 0 || strings.Contains(result.Stdout, "host-secret-sentinel") {
		t.Fatal("host file outside workspace visible to sandbox")
	}
	result = integrationExecute(t, e, workDir, "cat ~/.ssh/id_rsa")
	if result.ExitCode == 0 {
		t.Fatal("unexpected SSH key visible in container")
	}
	result = integrationExecute(t, e, workDir, `test -z "${ZHIPU_API_KEY+x}${GITHUB_TOKEN+x}${AWS_ACCESS_KEY_ID+x}${SSH_AUTH_SOCK+x}${FEISHU_APP_SECRET+x}"`)
	if result.ExitCode != 0 {
		t.Fatal("host credentials inherited by container")
	}
}

func sandboxContainerIDs(t *testing.T, e *DockerExecutor) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "--all", "--filter", "label=go-tiny-claw.executor="+e.instanceID, "--format", "{{.ID}}").CombinedOutput()
	if err != nil {
		t.Fatalf("list test containers: %v (%s)", err, out)
	}
	ids := make(map[string]bool)
	for _, id := range strings.Fields(string(out)) {
		ids[id] = true
	}
	return ids
}

func TestDockerIntegrationCancellationLeavesNoContainer(t *testing.T) {
	e := integrationDocker(t, CodingPolicy())
	for _, mode := range []string{"cancel", "timeout", "cancel-during-start"} {
		t.Run(mode, func(t *testing.T) {
			before := sandboxContainerIDs(t, e)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req := Request{Command: "echo ready; sleep 300 & wait", WorkDir: t.TempDir()}
			want := context.Canceled
			if mode == "timeout" {
				req.Timeout = 2 * time.Second
				want = context.DeadlineExceeded
			}
			var timer *time.Timer
			if mode == "cancel-during-start" {
				timer = time.AfterFunc(50*time.Millisecond, cancel)
				defer timer.Stop()
			}
			result, err := e.Execute(ctx, req, func(p []byte) {
				if mode == "cancel" && strings.Contains(string(p), "ready") {
					cancel()
				}
			})
			if !errors.Is(err, want) {
				t.Fatalf("cancellation result=%+v error=%v want=%v", result, err, want)
			}
			for id := range sandboxContainerIDs(t, e) {
				if !before[id] {
					t.Errorf("cancellation left container %s", id)
				}
			}
		})
	}
}
