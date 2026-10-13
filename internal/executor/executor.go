// Package executor separates command execution from tool and agent logic.
package executor

import (
	"context"
	"time"
)

// Request describes one command. WorkDir must name an existing directory.
// Env contains explicit overrides, not a copy of the host environment.
// Timeout is an execution budget; sandbox executors may impose a tighter limit.
type Request struct {
	Command string
	WorkDir string
	Env     map[string]string
	Timeout time.Duration
}

// Result retains the complete output, even on command or infrastructure failure.
// ExitCode is -1 when no normal exit status is available. A nonzero command exit
// code is NOT an infrastructure error; callers must inspect it explicitly.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// OutputHandler receives stdout/stderr chunks in arrival order. Callbacks are
// serialized within an Execute call and must return promptly. The byte slice
// belongs to the callback and may be retained. A nil handler disables streaming.
type OutputHandler func([]byte)

// Executor owns process startup, cwd, environment, timeout and cancellation.
// Cancellation must stop execution rather than just stop waiting for output.
// Implementations must never silently switch execution backends on failure.
// Session-scoped executors can implement this same interface in the future.
type Executor interface {
	Execute(ctx context.Context, req Request, onOutput OutputHandler) (Result, error)
}
