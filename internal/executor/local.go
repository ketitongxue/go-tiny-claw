package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"
)

const DefaultTimeout = 30 * time.Second

// LocalExecutor is UNSAFE: commands have the host user's permissions. WorkDir
// only sets cwd and is NOT a filesystem or process isolation boundary.
type LocalExecutor struct{}

func NewLocalExecutor() *LocalExecutor { return &LocalExecutor{} }

func (e *LocalExecutor) Execute(ctx context.Context, req Request, onOutput OutputHandler) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{ExitCode: -1}, &InfrastructureError{Op: "local execute", Err: err}
	}
	workDir, err := canonicalWorkDir(req.WorkDir)
	if err != nil {
		return Result{ExitCode: -1}, &InfrastructureError{Op: "local working directory", Err: err}
	}
	if req.Timeout < 0 {
		return Result{ExitCode: -1}, &InfrastructureError{Op: "local timeout", Err: fmt.Errorf("timeout must not be negative")}
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", req.Command)
	cmd.Dir = workDir
	// A minimal, useful host environment, not os.Environ(). Local execution
	// remains unsafe even without inherited secrets: it can read host files.
	env := hostEnvironment([]string{"PATH", "LANG", "LC_ALL", "TERM", "HOME", "TMPDIR"})
	for key, value := range req.Env {
		env[key] = value
	}
	cmd.Env = environmentList(env)
	return runProcess(ctx, cmd, onOutput)
}

func hostEnvironment(allowed []string) map[string]string {
	env := make(map[string]string, len(allowed))
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	return env
}

func environmentList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	list := make([]string, 0, len(keys))
	for _, key := range keys {
		list = append(list, key+"="+env[key])
	}
	return list
}
