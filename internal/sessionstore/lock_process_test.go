//go:build darwin || linux

package sessionstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Executed only in child processes. A persistent .lock file is intentionally
// left behind; the tests prove that ownership comes from the OS, not existence.
func TestFileStoreLockProcessHelper(t *testing.T) {
	if os.Getenv("CLAW_SESSION_LOCK_HELPER") != "1" {
		return
	}
	f, err := NewFileStore(os.Getenv("CLAW_SESSION_LOCK_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	id := os.Getenv("CLAW_SESSION_LOCK_ID")
	lock, err := f.AcquireWriter(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CLAW_SESSION_LOCK_TORN") == "1" {
		file, err := os.OpenFile(f.path(id), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write([]byte(`{"version":`))
		if err := errors.Join(writeErr, file.Close()); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("writer-ready")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

type writerProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  <-chan error
}

func startWriterProcess(t *testing.T, f *FileStore, id string, torn bool) *writerProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestFileStoreLockProcessHelper$")
	cmd.Env = append(os.Environ(), "CLAW_SESSION_LOCK_HELPER=1", "CLAW_SESSION_LOCK_DIR="+f.dir, "CLAW_SESSION_LOCK_ID="+id, fmt.Sprintf("CLAW_SESSION_LOCK_TORN=%d", boolInt(torn)))
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(stdout)
		line, err := reader.ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "writer-ready" {
			err = fmt.Errorf("unexpected child output: %s", line)
		}
		ready <- err
		_, _ = io.Copy(io.Discard, reader)
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("child did not exit")
		}
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child writer did not become ready")
	}
	return &writerProcess{cmd: cmd, stdin: stdin, done: done}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (p *writerProcess) finish(t *testing.T, killed bool) {
	t.Helper()
	if killed {
		if err := p.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	} else if _, err := fmt.Fprintln(p.stdin, "release"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.done:
		if (err != nil) != killed {
			t.Fatalf("child exit = %v, killed=%t", err, killed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child writer did not exit")
	}
}

func TestCrossProcessWriterConflictAndActiveReads(t *testing.T) {
	for _, torn := range []bool{false, true} {
		t.Run(fmt.Sprintf("torn=%t", torn), func(t *testing.T) {
			ctx := context.Background()
			f := newTestFileStore(t)
			if err := f.Create(ctx, snapshot("active")); err != nil {
				t.Fatal(err)
			}
			child := startWriterProcess(t, f, "active", torn)
			if _, err := f.AcquireWriter(ctx, "active"); !errors.Is(err, ErrInUse) {
				t.Fatalf("cross-process writer conflict = %v", err)
			}
			if err := f.Append(ctx, "active", messageEvent("conflict")); !errors.Is(err, ErrInUse) {
				t.Fatal(err)
			}
			u := NewEvent("usage")
			u.Usage = &Usage{PromptTokens: 10}
			if err := f.Append(ctx, "active", u); !errors.Is(err, ErrInUse) {
				t.Fatal("usage writer conflict", err)
			}
			if err := f.Delete(ctx, "active"); !errors.Is(err, ErrInUse) {
				t.Fatal("active delete succeeded", err)
			}
			s, err := f.Load(ctx, "active")
			if err != nil || s.Metadata.ID != "active" || len(s.History) != 0 {
				t.Fatal("read-only load failed with active writer", err)
			}
			list, err := f.List(ctx)
			if err != nil || len(list) != 1 {
				t.Fatal("read-only list failed with active writer", err)
			}
			child.finish(t, false)
			if err := f.Append(ctx, "active", messageEvent("after release")); err != nil {
				t.Fatal("append after release failed", err)
			}
			if err := f.Delete(ctx, "active"); err != nil {
				t.Fatal("inactive delete failed", err)
			}
		})
	}
}

func TestProcessCrashReleasesWriterLock(t *testing.T) {
	ctx := context.Background()
	f := newTestFileStore(t)
	if err := f.Create(ctx, snapshot("crash")); err != nil {
		t.Fatal(err)
	}
	a := startWriterProcess(t, f, "crash", true)
	a.finish(t, true) // SIGKILL: no deferred Release can run
	if _, err := os.Stat(f.lockPath("crash")); err != nil {
		t.Fatal("test requires the persistent lock file", err)
	}
	b := startWriterProcess(t, f, "crash", false) // another real process can own it
	b.finish(t, false)
	if err := f.Append(ctx, "crash", messageEvent("recovered after kill")); err != nil {
		t.Fatal(err)
	}
	s, err := f.Load(ctx, "crash")
	if err != nil || len(s.History) != 1 {
		t.Fatal("crash recovery lost committed history", err)
	}
}
