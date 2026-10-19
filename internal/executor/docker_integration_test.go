package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

// Workspace/RO/path/environment/process behavior lives in the shared contract.
func TestDockerIntegrationImageTools(t *testing.T) {
	e := integrationDocker(t, CodingPolicy())
	result := integrationExecute(t, e, t.TempDir(), "go version && git --version && curl --version && rg --version && echo tmpfs > /tmp/temporary")
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "go version") {
		t.Fatalf("sandbox image tools or tmpfs broken: %+v", result)
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
