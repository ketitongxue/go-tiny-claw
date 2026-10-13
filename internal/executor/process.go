package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func canonicalWorkDir(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("%w: path is empty", ErrInvalidWorkDir)
	}
	absolute, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidWorkDir, err)
	}
	realPath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidWorkDir, err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidWorkDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrInvalidWorkDir, realPath)
	}
	return realPath, nil
}

type outputStream struct {
	mu      sync.Mutex
	handler OutputHandler
}

type outputWriter struct {
	buffer *bytes.Buffer
	stream *outputStream
}

func (w outputWriter) Write(p []byte) (int, error) {
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	n, err := w.buffer.Write(p)
	if w.stream.handler != nil {
		w.stream.handler(append([]byte(nil), p...))
	}
	return n, err
}

// runProcess captures complete, separate streams and preserves partial output
// on failure. Platform helpers kill a process group on Unix; this also stops
// ordinary child processes which would otherwise keep the output pipes open.
func runProcess(ctx context.Context, cmd *exec.Cmd, onOutput OutputHandler) (result Result, err error) {
	start := time.Now()
	result.ExitCode = -1
	var stdout, stderr bytes.Buffer
	stream := &outputStream{handler: onOutput}
	cmd.Stdout = outputWriter{buffer: &stdout, stream: stream}
	cmd.Stderr = outputWriter{buffer: &stderr, stream: stream}
	configureProcessCancellation(cmd)
	// Bound pipe-draining time even if a descendant escaped the process group.
	cmd.WaitDelay = 500 * time.Millisecond
	defer func() {
		if cmd.Process != nil {
			_ = killProcessTree(cmd)
		}
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.Duration = time.Since(start)
	}()

	runErr := cmd.Run()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		return result, &InfrastructureError{Op: "execute canceled or timed out", Err: ctx.Err()}
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return result, &InfrastructureError{Op: "start or wait for process", Err: runErr}
		}
	}
	return result, nil
}
