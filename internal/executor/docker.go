package executor

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const DefaultImage = "go-tiny-claw-sandbox:latest"

// DockerExecutor creates one disposable container per Execute. Its policy is
// immutable, and it never falls back to host execution, even after startup fails.
type DockerExecutor struct {
	image        string
	policy       SandboxPolicy
	dockerBinary string
	instanceID   string // scopes observability/cleanup checks without persistent containers
}

func NewDockerExecutor(image string, policy SandboxPolicy) (*DockerExecutor, error) {
	if err := ValidateImage(image); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if err := requireCapabilities(ModeDocker, DockerCapabilities(), policy); err != nil {
		return nil, err
	}
	policy = policy.clone()
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, &InfrastructureError{Op: "executor identifier", Err: err}
	}
	return &DockerExecutor{image: image, policy: policy, dockerBinary: "docker", instanceID: hex.EncodeToString(random[:])}, nil
}

// ValidateImage rejects empty names and CLI/shell metacharacters before startup.
func ValidateImage(image string) error {
	if image == "" || strings.HasPrefix(image, "-") {
		return fmt.Errorf("invalid sandbox image %q", image)
	}
	for _, c := range image {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("._:/@-", c) {
			return fmt.Errorf("invalid sandbox image %q", image)
		}
	}
	return nil
}

// DockerAvailable checks both CLI and daemon, not just whether docker is in PATH.
func DockerAvailable(ctx context.Context) error { return dockerAvailable(ctx, "docker") }

func dockerAvailable(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := runProcess(ctx, exec.CommandContext(ctx, binary, "version", "--format", "{{.Server.Version}}"), nil)
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) == "" {
		return &InfrastructureError{Op: "docker version", Err: errors.Join(ErrDockerUnavailable, err, fmt.Errorf("exit=%d: %s", result.ExitCode, strings.TrimSpace(result.Stderr)))}
	}
	return nil
}

// CheckResourceSupport refuses daemons which would merely warn and ignore
// resource flags (notably rootless / cgroup-less hosts). Flags alone are not
// proof of enforcement. Check before execution, never after partial effects.
func (e *DockerExecutor) CheckResourceSupport(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	format := "{{.MemoryLimit}} {{.SwapLimit}} {{.CPUCfsQuota}} {{.CPUCfsPeriod}} {{.PidsLimit}}"
	r, err := runProcess(ctx, exec.CommandContext(ctx, e.dockerBinary, "info", "--format", format), nil)
	if err != nil || r.ExitCode != 0 || strings.TrimSpace(r.Stdout) != "true true true true true" {
		return &InfrastructureError{Op: "Docker resource capability check", Err: errors.Join(ErrUnsupportedPolicy, err, fmt.Errorf("daemon cannot demonstrate memory/swap/cpu/pids enforcement"))}
	}
	return nil
}

// CheckImage never pulls images implicitly. Build the chosen, trusted image
// before starting the agent; network-denied execution must also work offline.
func (e *DockerExecutor) CheckImage(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := runProcess(ctx, exec.CommandContext(ctx, e.dockerBinary, "image", "inspect", "--format", "{{.Id}}", e.image), nil)
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) == "" {
		return &InfrastructureError{Op: "sandbox image inspect", Err: errors.Join(err, fmt.Errorf("sandbox image %q is unavailable; build it first with docker build -f Dockerfile.sandbox -t %s . (exit=%d: %s)", e.image, e.image, result.ExitCode, strings.TrimSpace(result.Stderr)))}
	}
	return nil
}

// ValidateWorkspace allows application startup to fail before contacting an LLM.
// Execute also repeats validation because workspace contents can change.
func (e *DockerExecutor) ValidateWorkspace(ctx context.Context, workDir string) error {
	ctx, cancel := context.WithTimeout(ctx, e.policy.Timeout)
	defer cancel()
	_, err := buildDockerArgsContext(ctx, Request{WorkDir: workDir}, e.policy)
	return err
}

func (e *DockerExecutor) Execute(ctx context.Context, req Request, onOutput OutputHandler) (result Result, err error) {
	start := time.Now()
	result.ExitCode = -1
	defer func() { result.Duration = time.Since(start) }()
	if err := ctx.Err(); err != nil {
		return result, &InfrastructureError{Op: "docker execute", Err: err}
	}
	timeout, err := e.policy.executionTimeout(req.Timeout)
	if err != nil {
		return result, &InfrastructureError{Op: "docker timeout", Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args, err := buildDockerArgsContext(ctx, req, e.policy)
	if err != nil {
		return result, &InfrastructureError{Op: "docker execution parameters", Err: err}
	}

	if err := e.CheckResourceSupport(ctx); err != nil {
		return result, err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return result, &InfrastructureError{Op: "container name", Err: err}
	}
	name := "go-tiny-claw-" + hex.EncodeToString(random[:])
	args = append(args, "--name", name, "--label", "go-tiny-claw.sandbox=true", "--label", "go-tiny-claw.executor="+e.instanceID, e.image, "bash", "-lc", req.Command)
	// Killing the CLI alone does not reliably stop the container. Always issue
	// a daemon-side force removal using a fresh context, including on cancel.
	defer func() {
		if cleanupErr := e.removeContainer(name, ctx.Err() != nil); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()

	result, err = runProcess(ctx, exec.CommandContext(ctx, e.dockerBinary, args...), onOutput)
	// Docker startup/daemon errors use reserved exit codes and CLI diagnostics.
	// Do not misclassify a shell's plain `exit 125`/126/127 as infrastructure failure.
	if err == nil && result.ExitCode >= 125 && result.ExitCode <= 127 && dockerDiagnostic(result.Stderr) {
		err = &InfrastructureError{Op: "docker run", Err: fmt.Errorf("container startup failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Stderr))}
	}
	return result, err
}

func dockerDiagnostic(stderr string) bool {
	message := strings.ToLower(strings.TrimSpace(stderr))
	return strings.HasPrefix(message, "docker:") || strings.Contains(message, "error response from daemon")
}

func (e *DockerExecutor) removeContainer(name string, canceled bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// On cancellation, creation may still be completing in the daemon when
	// the CLI dies. Retry briefly after "not found" to close that race.
	retryUntil := time.Now().Add(time.Second)
	for {
		result, err := runProcess(ctx, exec.CommandContext(ctx, e.dockerBinary, "rm", "--force", name), nil)
		out := result.Stdout + result.Stderr
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		if err == nil {
			err = fmt.Errorf("docker rm exited with code %d", result.ExitCode)
		}
		if strings.Contains(strings.ToLower(out), "no such container") {
			if !canceled || time.Now().After(retryUntil) || ctx.Err() != nil {
				return nil // --rm already removed it, or creation never succeeded.
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		return &InfrastructureError{Op: "docker container cleanup", Err: errors.Join(err, ctx.Err(), fmt.Errorf("container %s: %s", name, strings.TrimSpace(out)))}
	}
}

// buildDockerArgs returns only the fixed execution options, without an image or
// command. CSV encoding prevents commas/quotes in a host path from injecting
// mount options; exec's argument slice protects spaces and shell metacharacters.
func buildDockerArgs(req Request, policy SandboxPolicy) ([]string, error) {
	return buildDockerArgsContext(context.Background(), req, policy)
}

func buildDockerArgsContext(ctx context.Context, req Request, policy SandboxPolicy) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if _, err := policy.executionTimeout(req.Timeout); err != nil {
		return nil, err
	}
	if err := requireCapabilities(ModeDocker, DockerCapabilities(), policy); err != nil {
		return nil, err
	}
	workDir, err := canonicalWorkDir(req.WorkDir)
	if err != nil {
		return nil, err
	}
	if err := validateSandboxWorkDir(ctx, workDir); err != nil {
		return nil, err
	}
	env, err := policy.environment(req.Env)
	if err != nil {
		return nil, err
	}

	mount := []string{"type=bind", "src=" + workDir, "dst=/workspace", "bind-recursive=disabled", "bind-propagation=rprivate"}
	if policy.WorkspaceMode == WorkspaceReadOnly {
		mount = append(mount, "readonly")
	}
	var encoded strings.Builder
	writer := csv.NewWriter(&encoded)
	if err := writer.Write(mount); err != nil {
		return nil, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	network := "none"
	if policy.NetworkEnabled {
		network = "bridge" // Never host networking.
	}
	memory := strconv.Itoa(policy.MemoryMB) + "m"
	args := []string{
		"run", "--rm", "--pull", "never", "--init",
		"--network", network,
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--memory", memory, "--memory-swap", memory,
		"--cpus", strconv.FormatFloat(policy.CPUs, 'f', -1, 64),
		"--pids-limit", strconv.Itoa(policy.Pids),
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=128m,mode=1777",
		"--mount", strings.TrimSuffix(encoded.String(), "\n"),
		"--workdir", "/workspace",
	}
	if os.Getuid() >= 0 && os.Getgid() >= 0 {
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	for _, entry := range env {
		args = append(args, "--env", entry)
	}
	// Do not depend on a custom image's HOME/TMPDIR defaults.
	defaults := []string{"HOME=/tmp", "TMPDIR=/tmp",
		// Docker Desktop may present a different bind-mount owner UID. Trust
		// only this mount for git, not every repository on the filesystem.
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=/workspace",
		// Keep /tmp's existing noexec hardening. Go test executables must
		// live in the already executable RW workspace, not weaken tmpfs.
		"GOTMPDIR=/workspace"}
	for _, entry := range defaults {
		key, _, _ := strings.Cut(entry, "=")
		if _, supplied := req.Env[key]; !supplied {
			args = append(args, "--env", entry)
		}
	}
	// HOME/TMPDIR cannot be supplied: policy validation reserves those keys.
	return args, nil
}
