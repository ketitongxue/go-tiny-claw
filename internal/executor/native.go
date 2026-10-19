package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var ErrNativeUnavailable = errors.New("Native sandbox unavailable")

const srtInstallHint = "Install Anthropic Sandbox Runtime: npm install -g @anthropic-ai/sandbox-runtime"

// NativeExecutor delegates ALL OS enforcement to the external, trusted SRT CLI.
// It owns no alternate executor and cannot replay a failed command elsewhere.
type NativeExecutor struct {
	binary          string
	policy          SandboxPolicy
	runtimeReadRoot string // verified SRT npm package, for Linux's vendored helper
	runtimeBase     string // tests may scope cleanup; production uses short /tmp paths for SRT's Unix sockets
}

// NewNativeExecutor validates the installation against workDir before executing
// any SRT code, including --version and the disposable enforcement probes.
func NewNativeExecutor(ctx context.Context, workDir string, policy SandboxPolicy) (*NativeExecutor, error) {
	return newNativeExecutor(ctx, "srt", workDir, policy)
}

func newNativeExecutor(ctx context.Context, binary, workDir string, policy SandboxPolicy) (*NativeExecutor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, fmt.Errorf("%w: native backend is unverified on %s", ErrNativeUnavailable, runtime.GOOS)
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if err := requireCapabilities(ModeNative, NativeCapabilities(), policy); err != nil {
		return nil, err
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("%w: srt executable not found. %s", ErrNativeUnavailable, srtInstallHint)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	e := &NativeExecutor{binary: path, policy: policy.clone()}
	if filepath.Base(path) == "cli.js" && filepath.Base(filepath.Dir(path)) == "dist" {
		e.runtimeReadRoot = filepath.Dir(filepath.Dir(path))
	}
	// Reject project-writable installations before even reading their manifest
	// or running --version. PATH and workspace aliases have been canonicalized.
	if err := e.ValidateWorkspace(ctx, workDir); err != nil {
		return nil, err
	}
	if e.runtimeReadRoot != "" {
		data, err := os.ReadFile(filepath.Join(e.runtimeReadRoot, "package.json"))
		var manifest struct {
			Name string `json:"name"`
		}
		if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.Name != "@anthropic-ai/sandbox-runtime" {
			return nil, fmt.Errorf("%w: cannot verify SRT package root", ErrNativeUnavailable)
		}
	}
	if err := srtAvailable(ctx, path); err != nil {
		return nil, err
	}
	if err := e.checkIsolation(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

// SRTAvailable checks a workspace-safe installation and its enforcement probes,
// not just PATH or a guessed minimum semver.
func SRTAvailable(ctx context.Context, workDir string) error {
	_, err := NewNativeExecutor(ctx, workDir, CodingPolicy())
	return err
}

// Call only after validating the installation; even --version executes host code.
func srtAvailable(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Env = environmentList(nativeHostEnvironment())
	result, err := runProcess(ctx, cmd, nil)
	if err != nil || result.ExitCode != 0 || !regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][a-zA-Z0-9.-]+)?$`).MatchString(strings.TrimSpace(result.Stdout)) {
		return &InfrastructureError{Op: "srt --version", Err: errors.Join(ErrNativeUnavailable, err, fmt.Errorf("version check failed (exit %d); %s", result.ExitCode, srtInstallHint))}
	}
	return nil
}

func nativeHostEnvironment() map[string]string {
	env := hostEnvironment([]string{"LANG", "LC_ALL", "TERM"})
	// No '.' / workspace / home tool directories in the trusted runtime's
	// PATH. Request PATH overrides are applied only after entering SRT.
	env["PATH"] = "/usr/local/go/bin:/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin"
	if runtime.GOOS == "darwin" {
		// Apple's git/clang launchers otherwise consult /var/select symlink
		// metadata outside the read allowlist. Select an already-whitelisted
		// SDK explicitly, rather than opening /var or inheriting host config.
		for _, sdk := range []string{"/Library/Developer/CommandLineTools", "/Applications/Xcode.app/Contents/Developer"} {
			if info, err := os.Stat(sdk); err == nil && info.IsDir() {
				if resolved, err := filepath.EvalSymlinks(sdk); err == nil {
					env["DEVELOPER_DIR"] = resolved
					break
				}
			}
		}
	}
	return env
}

func (e *NativeExecutor) ValidateWorkspace(ctx context.Context, workDir string) error {
	ctx, cancel := context.WithTimeout(ctx, e.policy.Timeout)
	defer cancel()
	path, err := canonicalWorkDir(workDir)
	if err != nil {
		return err
	}
	if err := validateSandboxWorkDir(ctx, path); err != nil {
		return err
	}
	// Installing the security runtime in a model-writable project would let
	// one invocation replace the next invocation's trusted enforcement code.
	if pathContains(path, e.binary) || e.runtimeReadRoot != "" && (pathContains(path, e.runtimeReadRoot) || pathContains(e.runtimeReadRoot, path)) {
		return fmt.Errorf("%w: workspace overlaps the SRT installation", ErrInvalidWorkDir)
	}
	if e.runtimeReadRoot != "" {
		protected, err := protectedHostPaths()
		if err != nil {
			return err
		}
		for _, path := range protected {
			if pathContains(path, e.runtimeReadRoot) || pathContains(e.runtimeReadRoot, path) {
				return fmt.Errorf("%w: SRT install overlaps credentials", ErrInvalidPolicy)
			}
		}
	}
	return nil
}

func (e *NativeExecutor) Execute(ctx context.Context, req Request, onOutput OutputHandler) (Result, error) {
	return e.execute(ctx, req, onOutput, nil)
}

// prepare is used ONLY by the trusted startup probe to place a socket inside
// the private runtime. It is not an application/model policy escape hatch.
func (e *NativeExecutor) execute(ctx context.Context, req Request, onOutput OutputHandler, prepare func(string) error) (result Result, err error) {
	start := time.Now()
	result.ExitCode = -1
	defer func() {
		result.Duration = time.Since(start)
		var infrastructure *InfrastructureError
		if err != nil && !errors.As(err, &infrastructure) {
			err = &InfrastructureError{Op: "native execute", Err: err}
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, &InfrastructureError{Op: "native execute", Err: err}
	}
	timeout, err := e.policy.executionTimeout(req.Timeout)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := e.ValidateWorkspace(ctx, req.WorkDir); err != nil {
		return result, &InfrastructureError{Op: "native workspace", Err: err}
	}
	workDir, err := canonicalWorkDir(req.WorkDir)
	if err != nil {
		return result, err
	}
	if _, err := e.policy.environment(req.Env); err != nil {
		return result, err
	}
	runtimeBase := e.runtimeBase
	if runtimeBase == "" {
		runtimeBase = "/tmp"
	}
	runtimeDir, err := os.MkdirTemp(runtimeBase, "go-tiny-claw-sandbox-")
	if err != nil {
		return result, &InfrastructureError{Op: "native runtime directory", Err: err}
	}
	createdDir := runtimeDir
	defer func() {
		if cleanupErr := os.RemoveAll(createdDir); cleanupErr != nil {
			err = errors.Join(err, &InfrastructureError{Op: "native runtime cleanup", Err: cleanupErr})
		}
	}()
	runtimeDir, err = filepath.EvalSymlinks(runtimeDir)
	if err != nil {
		return result, err
	}
	if pathContains(workDir, runtimeDir) || pathContains(runtimeDir, workDir) {
		return result, fmt.Errorf("%w: workspace overlaps runtime", ErrInvalidWorkDir)
	}
	for _, path := range []string{runtimeDir, filepath.Join(runtimeDir, "home"), filepath.Join(runtimeDir, "tmp")} {
		if path != runtimeDir {
			if err := os.Mkdir(path, 0o700); err != nil {
				return result, err
			}
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return result, err
		}
	}
	protected, err := protectedHostPaths()
	if err != nil {
		return result, err
	}
	for _, path := range protected {
		if pathContains(path, runtimeDir) || pathContains(runtimeDir, path) {
			return result, fmt.Errorf("%w: runtime overlaps protected path", ErrInvalidWorkDir)
		}
	}
	if prepare != nil {
		if err := prepare(runtimeDir); err != nil {
			return result, err
		}
	}
	settings, err := buildSRTSettings(workDir, runtimeDir, e.policy)
	if err != nil {
		return result, &InfrastructureError{Op: "native policy", Err: err}
	}
	if e.runtimeReadRoot != "" {
		settings.Filesystem.AllowRead = append(settings.Filesystem.AllowRead, srtPath{e.runtimeReadRoot, true})
		settings.Filesystem.DenyWrite = append(settings.Filesystem.DenyWrite, srtPath{e.runtimeReadRoot, true})
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return result, err
	}
	settingsPath := filepath.Join(runtimeDir, "settings.json")
	file, err := os.OpenFile(settingsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return result, &InfrastructureError{Op: "native settings", Err: err}
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return result, &InfrastructureError{Op: "native settings", Err: err}
	}

	// Pin a regular inode BEFORE the sandbox starts. Never reopen this path
	// after running untrusted code: it could have become a symlink, device or
	// FIFO. The marker classifies failures; it does not authorize execution.
	statusFile, err := os.OpenFile(filepath.Join(runtimeDir, "exit-status"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return result, &InfrastructureError{Op: "native completion file", Err: err}
	}
	defer func() {
		if closeErr := statusFile.Close(); closeErr != nil {
			err = errors.Join(err, &InfrastructureError{Op: "native completion close", Err: closeErr})
		}
	}()
	env := nativeHostEnvironment()
	env["HOME"], env["TMPDIR"] = filepath.Join(runtimeDir, "home"), filepath.Join(runtimeDir, "tmp")
	// SRT otherwise replaces TMPDIR with its shared /tmp/claude default.
	env["CLAUDE_CODE_TMPDIR"] = env["TMPDIR"]
	// Global Git configs can contain credentials/credential helpers. Do not
	// reopen host config merely to make git status work under denyRead '/'.
	env["GIT_CONFIG_NOSYSTEM"], env["GIT_CONFIG_GLOBAL"] = "1", "/dev/null"
	// Explicit overrides belong INSIDE the sandbox. Passing an allowlisted
	// NODE_OPTIONS/BASH_ENV/etc to the trusted SRT process could run host code.
	// SRT excludes localhost from proxying by default; clear that for local
	// allowlisted endpoints. Explicit app-allowlisted overrides still apply
	// below; the OS fence, not these env defaults, prevents proxy bypass.
	inner := "export HOME=" + shellQuote(env["HOME"]) + " TMPDIR=" + shellQuote(env["TMPDIR"]) + " NO_PROXY='' no_proxy=''; "
	inner += "/usr/bin/env "
	for _, entry := range environmentList(req.Env) {
		inner += shellQuote(entry) + " "
	}
	inner += "/bin/bash --noprofile --norc -c " + shellQuote(req.Command)
	inner += "; claw_status=$?; printf '%s\\n' \"$claw_status\" > " + shellQuote(filepath.Join(runtimeDir, "exit-status")) + "; exit \"$claw_status\""
	cmd := exec.CommandContext(ctx, e.binary, "--settings", settingsPath, "-c", inner)
	cmd.Dir, cmd.Env = workDir, environmentList(env)
	result, err = runProcess(ctx, cmd, onOutput)
	if err != nil {
		return result, err
	}
	status, readErr := io.ReadAll(io.LimitReader(statusFile, 16))
	exit, parseErr := strconv.Atoi(strings.TrimSpace(string(status)))
	if readErr != nil || parseErr != nil || exit != result.ExitCode {
		return result, &InfrastructureError{Op: "srt execution", Err: fmt.Errorf("sandbox did not report command completion (SRT exit %d)", result.ExitCode)}
	}
	return result, nil
}

// The CLI has no stable feature-negotiation API. Validate the literal-path
// schema, denyRead precedence and RO enforcement with disposable fixtures,
// before any model command. Unsupported schemas/default fallback fail here.
func (e *NativeExecutor) checkIsolation(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "claw-srt-check-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
			err = errors.Join(err, &InfrastructureError{Op: "SRT probe cleanup", Err: cleanupErr})
		}
	}()
	work := filepath.Join(root, "workspace")
	if err := os.Mkdir(work, 0o700); err != nil {
		return err
	}
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("private-probe"), 0o600); err != nil {
		return err
	}
	if err := e.checkSettingsFailure(ctx, root, work); err != nil {
		return err
	}
	probe := *e
	probe.policy.Timeout = 5 * time.Second
	probe.policy.WorkspaceMode = WorkspaceReadWrite
	r, err := probe.Execute(ctx, Request{WorkDir: work, Command: "printf workspace-probe > readable; cat readable"}, nil)
	if err != nil || r.ExitCode != 0 || r.Stdout != "workspace-probe" {
		return &InfrastructureError{Op: "SRT RW compatibility probe", Err: errors.Join(ErrNativeUnavailable, err, ErrUnsupportedPolicy)}
	}
	probe.policy.WorkspaceMode = WorkspaceReadOnly
	command := "cat readable >/dev/null && ! cat " + shellQuote(canonicalPotentialPath(secret)) + " >/dev/null 2>&1 && ! (echo changed > forbidden) 2>/dev/null && printf enforcement-ok"
	r, err = probe.Execute(ctx, Request{WorkDir: work, Command: command}, nil)
	if err != nil || r.ExitCode != 0 || r.Stdout != "enforcement-ok" {
		return &InfrastructureError{Op: "SRT compatibility/enforcement probe", Err: errors.Join(ErrNativeUnavailable, err, fmt.Errorf("required filesystem isolation not demonstrated (exit %d)", r.ExitCode))}
	}
	if err := probe.checkNetworkIsolation(ctx, work); err != nil {
		return &InfrastructureError{Op: "SRT network/socket enforcement probe", Err: errors.Join(ErrNativeUnavailable, err)}
	}
	return nil
}

// A runtime which falls back to defaults on missing/empty/invalid explicit
// settings is incompatible even if it understands our valid JSON schema.
// All probe commands are fixed harmless strings, never model commands.
func (e *NativeExecutor) checkSettingsFailure(ctx context.Context, root, work string) error {
	env := nativeHostEnvironment()
	for key, name := range map[string]string{"HOME": "home", "TMPDIR": "tmp"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		env[key] = path
	}
	env["CLAUDE_CODE_TMPDIR"] = env["TMPDIR"]
	for _, name := range []string{"missing.json", "empty.json", "invalid.json"} {
		path := filepath.Join(root, name)
		if name != "missing.json" {
			data := []byte{}
			if name == "invalid.json" {
				data = []byte("{")
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return err
			}
		}
		cmd := exec.CommandContext(ctx, e.binary, "--settings", path, "-c", "printf invalid-settings-command-ran")
		cmd.Dir, cmd.Env = work, environmentList(env)
		r, err := runProcess(ctx, cmd, nil)
		if err != nil || r.ExitCode == 0 || strings.Contains(r.Stdout, "invalid-settings-command-ran") {
			return &InfrastructureError{Op: "SRT explicit-settings fail-closed probe", Err: errors.Join(ErrNativeUnavailable, err, ErrUnsupportedPolicy)}
		}
	}
	return nil
}

func (e *NativeExecutor) checkNetworkIsolation(ctx context.Context, work string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	var hits atomic.Int32
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("network-probe"))
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	address := listener.Addr().String()
	url := "http://" + address
	curl := "curl --fail --silent --connect-timeout 1 --max-time 2 "
	probe := *e
	probe.policy = e.policy.clone()
	probe.policy.NetworkEnabled, probe.policy.AllowedDomains = false, nil
	r, err := probe.Execute(ctx, Request{WorkDir: work, Command: "! " + curl + shellQuote(url)}, nil)
	if err != nil || r.ExitCode != 0 || hits.Load() != 0 {
		return fmt.Errorf("network deny not demonstrated: %w", errors.Join(err, ErrUnsupportedPolicy))
	}
	probe.policy.NetworkEnabled, probe.policy.AllowedDomains = true, []string{address}
	command := curl + shellQuote(url) + " && ! " + curl + "--noproxy '*' " + shellQuote(url)
	command += " && ! bash -c " + shellQuote("exec 3<>/dev/tcp/"+strings.Replace(address, ":", "/", 1))
	command += " && ! " + curl + "--noproxy '*' --unix-socket \"$TMPDIR/s\" http://localhost"
	r, err = probe.execute(ctx, Request{WorkDir: work, Command: command}, nil, func(dir string) error {
		unix, err := net.Listen("unix", filepath.Join(dir, "tmp", "s"))
		if err != nil {
			return err
		}
		go func() { _ = server.Serve(unix) }()
		return nil
	})
	if err != nil || r.ExitCode != 0 || r.Stdout != "network-probe" || hits.Load() != 1 {
		return fmt.Errorf("allowlist or direct TCP/Unix socket fence not demonstrated: %w", errors.Join(err, ErrUnsupportedPolicy))
	}
	return nil
}
