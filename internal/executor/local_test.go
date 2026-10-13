package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalExecute(t *testing.T) {
	for _, tc := range []struct {
		name, command, stdout, stderr string
		exit                          int
	}{
		{"success", "echo hello", "hello\n", "", 0},
		{"failure", "exit 7", "", "", 7},
		{"stderr", "echo error >&2; exit 1", "", "error\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := NewLocalExecutor().Execute(context.Background(), Request{Command: tc.command, WorkDir: t.TempDir()}, nil)
			if err != nil || result.ExitCode != tc.exit || result.Stdout != tc.stdout || result.Stderr != tc.stderr || result.Duration <= 0 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestLocalWorkDirAndEnvironment(t *testing.T) {
	t.Setenv("ZHIPU_API_KEY", "must-not-inherit")
	t.Setenv("GITHUB_TOKEN", "must-not-inherit")
	workDir := filepath.Join(t.TempDir(), "path with spaces")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := NewLocalExecutor().Execute(context.Background(), Request{
		Command: `printf '%s\n' "$PWD" "$EXPLICIT_VALUE" "${ZHIPU_API_KEY-unset}" "${GITHUB_TOKEN-unset}"`,
		WorkDir: workDir, Env: map[string]string{"EXPLICIT_VALUE": "hello world"},
	}, nil)
	realDir, _ := filepath.EvalSymlinks(workDir)
	if err != nil || result.Stdout != realDir+"\nhello world\nunset\nunset\n" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestLocalInvalidWorkDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"", file, filepath.Join(t.TempDir(), "missing")} {
		result, err := NewLocalExecutor().Execute(context.Background(), Request{Command: "echo hello", WorkDir: dir}, nil)
		if !errors.Is(err, ErrInvalidWorkDir) || result.ExitCode != -1 {
			t.Fatalf("dir=%q result=%+v error=%v", dir, result, err)
		}
	}
}

func TestLocalTimeout(t *testing.T) {
	start := time.Now()
	result, err := NewLocalExecutor().Execute(context.Background(), Request{
		Command: "echo started; sleep 10", WorkDir: t.TempDir(), Timeout: 100 * time.Millisecond,
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(result.Stdout, "started") || time.Since(start) > 3*time.Second {
		t.Fatalf("result=%+v error=%v duration=%v", result, err, time.Since(start))
	}
}

func TestLocalCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := NewLocalExecutor().Execute(ctx, Request{
		Command: "echo started; sleep 10", WorkDir: t.TempDir(),
	}, func(p []byte) {
		if strings.Contains(string(p), "started") {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || !strings.Contains(result.Stdout, "started") || result.Duration > 3*time.Second {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestLocalCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewLocalExecutor().Execute(ctx, Request{Command: "echo unexpected", WorkDir: t.TempDir()}, nil)
	if !errors.Is(err, context.Canceled) || result.ExitCode != -1 || result.Stdout != "" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestLocalStreamingPreservesSeparateCompleteOutput(t *testing.T) {
	var chunks [][]byte
	result, err := NewLocalExecutor().Execute(context.Background(), Request{
		Command: "printf stdout; printf stderr >&2", WorkDir: t.TempDir(),
	}, func(p []byte) { chunks = append(chunks, p) })
	var streamed strings.Builder
	for _, chunk := range chunks {
		streamed.Write(chunk)
	}
	if err != nil || result.Stdout != "stdout" || result.Stderr != "stderr" || len(streamed.String()) != 12 || !strings.Contains(streamed.String(), "stderr") {
		t.Fatalf("result=%+v error=%v chunks=%q", result, err, chunks)
	}
}

func TestLocalCancelStopsChildProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cancellation requires Unix")
	}
	workDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The child writes a marker if it survives cancellation. Its output pipes
	// are redirected so this checks child lifetime, not just pipe draining.
	result, err := NewLocalExecutor().Execute(ctx, Request{
		Command: `(sleep 1; echo survived > child-survived) >/dev/null 2>&1 & echo ready; wait`,
		WorkDir: workDir,
	}, func(p []byte) {
		if strings.Contains(string(p), "ready") {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workDir, "child-survived")); !os.IsNotExist(err) {
		t.Fatalf("child survived cancellation: %v", err)
	}
}
