package sessionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func newTestFileStore(t testing.TB) *FileStore {
	t.Helper()
	f, err := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func ownSession(t testing.TB, f *FileStore, id string) {
	t.Helper()
	lock, err := f.AcquireWriter(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Error(err)
		}
	})
}

func messageEvent(content string) Event {
	e := NewEvent("message")
	e.Message = &schema.Message{Role: schema.RoleUser, Content: content}
	return e
}

func TestFileStoreAppendFastPath(t *testing.T) {
	ctx := context.Background()
	f := newTestFileStore(t)
	reads := 0
	f.read = func(p string) ([]byte, error) { reads++; return readFile(p) }
	lock, err := f.AcquireWriter(ctx, "fast")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if err := f.Create(ctx, snapshot("fast")); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if err := f.Append(ctx, "fast", messageEvent("tool output")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Load(ctx, "fast"); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 0 {
		t.Fatalf("Create + 100 Append/Load did %d full history reads, want 0", reads)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	resumed, err := NewFileStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	reads = 0
	resumed.read = func(p string) ([]byte, error) { reads++; return readFile(p) }
	ownSession(t, resumed, "fast")
	if _, err := resumed.Load(ctx, "fast"); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if err := resumed.Append(ctx, "fast", messageEvent("more output")); err != nil {
			t.Fatal(err)
		}
		if _, err := resumed.Load(ctx, "fast"); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("Resume + 100 Append/Load did %d full reads, want 1", reads)
	}
	st := resumed.state("fast")
	info, err := os.Stat(resumed.path("fast"))
	if err != nil || st.committedOffset != info.Size() || st.fileSize != info.Size() || len(st.snapshot.History) != 200 {
		t.Fatal("snapshot/offset cache not in sync", err)
	}
}

func TestFileStoreCacheDoesNotAliasCallers(t *testing.T) {
	ctx := context.Background()
	f := newTestFileStore(t)
	ownSession(t, f, "clone")
	s := snapshot("clone")
	s.History = []schema.Message{{Role: schema.RoleAssistant, Usage: &schema.Usage{PromptTokens: 1}, ToolCalls: []schema.ToolCall{{ID: "call", Arguments: []byte(`{"path":"x"}`)}}}}
	if err := f.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.History[0].Usage.PromptTokens = 999
	s.History[0].ToolCalls[0].Arguments[0] = '!'
	e := messageEvent("original")
	if err := f.Append(ctx, "clone", e); err != nil {
		t.Fatal(err)
	}
	e.Message.Content = "modified"
	first, err := f.Load(ctx, "clone")
	if err != nil {
		t.Fatal(err)
	}
	first.History[0].Usage.PromptTokens = 999
	first.History[0].ToolCalls[0].Arguments[0] = '!'
	first.History[1].Content = "modified"
	second, err := f.Load(ctx, "clone")
	if err != nil || second.History[0].Usage.PromptTokens != 1 || second.History[0].ToolCalls[0].Arguments[0] != '{' || second.History[1].Content != "original" {
		t.Fatalf("cache aliases caller: %+v (%v)", second, err)
	}
}

func TestFileStoreValidatesOnlyCandidateBeforeWrite(t *testing.T) {
	ctx := context.Background()
	f := newTestFileStore(t)
	ownSession(t, f, "validation")
	if err := f.Create(ctx, snapshot("validation")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.path("validation"))
	if err != nil {
		t.Fatal(err)
	}
	bad := messageEvent("bad version")
	bad.Version = 2
	if err := f.Append(ctx, "validation", messageEvent("valid prefix"), bad); !errors.Is(err, ErrCorrupted) {
		t.Fatalf("invalid candidate error = %v", err)
	}
	bad = messageEvent("bad arguments")
	bad.Message.ToolCalls = []schema.ToolCall{{Arguments: []byte(`{"torn":`)}}
	if err := f.Append(ctx, "validation", bad); err == nil {
		t.Fatal("invalid JSON encoded successfully")
	}
	after, _ := os.ReadFile(f.path("validation"))
	if !bytes.Equal(before, after) || len(f.state("validation").snapshot.History) != 0 {
		t.Fatal("invalid batch changed committed state")
	}
}

func TestFileStoreExternalChangeReplaysAndRejectsCorruption(t *testing.T) {
	ctx := context.Background()
	f := newTestFileStore(t)
	ownSession(t, f, "external")
	if err := f.Create(ctx, snapshot("external")); err != nil {
		t.Fatal(err)
	}
	reads := 0
	f.read = func(p string) ([]byte, error) { reads++; return readFile(p) }
	data, _ := encode([]Event{messageEvent("external append")})
	file, err := os.OpenFile(f.path("external"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Append(ctx, "external", messageEvent("own append")); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(f.state("external").snapshot.History) != 2 {
		t.Fatalf("external size change not replayed: reads=%d", reads)
	}
	original, _ := os.ReadFile(f.path("external"))
	broken := bytes.Replace(original, []byte(`"version":1`), []byte(`"version":9`), 1)
	if len(broken) != len(original) {
		t.Fatal("test must also cover same-size changes")
	}
	if err := os.WriteFile(f.path("external"), broken, 0600); err != nil {
		t.Fatal(err)
	}
	changed := time.Now().Add(time.Second)
	if err := os.Chtimes(f.path("external"), changed, changed); err != nil {
		t.Fatal(err)
	}
	if err := f.Append(ctx, "external", messageEvent("must reject")); !errors.Is(err, ErrCorrupted) {
		t.Fatalf("corruption error = %v", err)
	}
	if reads != 2 {
		t.Fatalf("corruption not replayed: reads=%d", reads)
	}
}

type faultFile struct {
	*os.File
	phase  string
	fault  error
	cancel context.CancelFunc
	onSync func()
}

func (f *faultFile) Write(data []byte) (int, error) {
	if f.phase == "write" || f.phase == "short" {
		n, err := f.File.Write(data[:len(data)/2])
		if f.phase == "write" {
			err = f.fault
		}
		return n, err
	}
	n, err := f.File.Write(data)
	if f.cancel != nil {
		f.cancel()
	}
	return n, err
}
func (f *faultFile) Sync() error {
	if f.onSync != nil {
		f.onSync()
	}
	if f.phase == "sync" {
		return f.fault
	}
	return f.File.Sync()
}
func (f *faultFile) Close() error {
	err := f.File.Close()
	if f.phase == "close" {
		return errors.Join(err, f.fault)
	}
	return err
}

func TestFileStoreWriteSyncCloseFailuresDoNotPublish(t *testing.T) {
	for _, phase := range []string{"write", "short", "sync", "close"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			f := newTestFileStore(t)
			ownSession(t, f, "failure")
			if err := f.Create(ctx, snapshot("failure")); err != nil {
				t.Fatal(err)
			}
			if err := f.Append(ctx, "failure", messageEvent("committed")); err != nil {
				t.Fatal(err)
			}
			st := f.state("failure")
			before, offset := clone(st.snapshot), st.committedOffset
			disk, _ := os.ReadFile(f.path("failure"))
			opener := f.openAppend
			fault := errors.New("injected " + phase + " failure")
			f.openAppend = func(p string) (appendFile, error) {
				file, err := os.OpenFile(p, os.O_WRONLY, 0600)
				if err != nil {
					return nil, err
				}
				return &faultFile{File: file, phase: phase, fault: fault}, nil
			}
			err := f.Append(ctx, "failure", messageEvent("failed batch"))
			if phase == "short" {
				if !errors.Is(err, io.ErrShortWrite) {
					t.Fatalf("error = %v", err)
				}
			} else if !errors.Is(err, fault) {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(st.snapshot, before) || st.committedOffset != offset {
				t.Fatal("failed commit advanced snapshot/offset")
			}
			// Persistent sync/close failure also makes rollback fail. The cache
			// must keep the old state, then repair it when the disk works again.
			if (phase == "sync" || phase == "close") && !st.needsRecovery {
				t.Fatal("failed rollback was forgotten")
			}
			f.openAppend = opener
			if err := f.Append(ctx, "failure", messageEvent("next commit")); err != nil {
				t.Fatal(err)
			}
			if len(st.snapshot.History) != 2 || st.snapshot.History[1].Content != "next commit" {
				t.Fatal("next commit started from failed state")
			}
			reader, _ := NewFileStore(f.dir)
			loaded, err := reader.Load(ctx, "failure")
			// Disk timestamps do not retain time.Time's monotonic component.
			got, _ := json.Marshal(loaded)
			want, _ := json.Marshal(st.snapshot)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("memory/disk diverged: %v\ngot %s\nwant %s", err, got, want)
			}
			after, _ := os.ReadFile(f.path("failure"))
			if !bytes.HasPrefix(after, disk) || bytes.Contains(after, []byte("failed batch")) {
				t.Fatal("failed batch remained on disk")
			}
		})
	}
}

func TestFileStoreCancellationCommitBoundary(t *testing.T) {
	f := newTestFileStore(t)
	ownSession(t, f, "cancel")
	if err := f.Create(context.Background(), snapshot("cancel")); err != nil {
		t.Fatal(err)
	}
	opener := f.openAppend
	ctx, cancel := context.WithCancel(context.Background())
	f.openAppend = func(p string) (appendFile, error) {
		file, err := opener(p)
		cancel() // canceled just before the first write
		return file, err
	}
	if err := f.Append(ctx, "cancel", messageEvent("not written")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled append error = %v", err)
	}
	if len(f.state("cancel").snapshot.History) != 0 {
		t.Fatal("canceled operation committed")
	}
	ctx, cancel = context.WithCancel(context.Background())
	syncs := 0
	f.openAppend = func(p string) (appendFile, error) {
		file, err := os.OpenFile(p, os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		return &faultFile{File: file, cancel: cancel, onSync: func() { syncs++ }}, nil
	}
	if err := f.Append(ctx, "cancel", messageEvent("write started")); err != nil {
		t.Fatal("cancellation interrupted a started commit", err)
	}
	if ctx.Err() != context.Canceled || syncs != 1 || len(f.state("cancel").snapshot.History) != 1 {
		t.Fatal("commit did not finish sync/publish after cancel")
	}
	if err := f.Append(ctx, "cancel", messageEvent("after cancel")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFileStoreDifferentSessionsDoNotShareMutex(t *testing.T) {
	f := newTestFileStore(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		ownSession(t, f, id)
		if err := f.Create(ctx, snapshot(id)); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseA := sync.OnceFunc(func() { close(release) })
	defer releaseA()
	f.openAppend = func(p string) (appendFile, error) {
		file, err := os.OpenFile(p, os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if p == f.path("a") {
			return &faultFile{File: file, onSync: func() { close(entered); <-release }}, nil
		}
		return file, nil
	}
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { doneA <- f.Append(ctx, "a", messageEvent("a")) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("session A did not reach sync")
	}
	go func() { doneB <- f.Append(ctx, "b", messageEvent("b")) }()
	select {
	case err := <-doneB:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session B blocked behind session A's mutex")
	}
	// Release A before cleanup tries to release its writer.
	releaseA()
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreConcurrentAppendAndLoad(t *testing.T) {
	f := newTestFileStore(t)
	ownSession(t, f, "concurrent")
	ctx := context.Background()
	if err := f.Create(ctx, snapshot("concurrent")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			if err := f.Append(ctx, "concurrent", messageEvent(fmt.Sprint(i))); err != nil {
				t.Error(err)
			}
			s, err := f.Load(ctx, "concurrent")
			if err != nil {
				t.Error(err)
				return
			}
			s.History[0].Content = "caller-owned copy"
		})
	}
	wg.Wait()
	s, err := f.Load(ctx, "concurrent")
	if err != nil || len(s.History) != 40 || s.Metadata.Messages != 40 {
		t.Fatalf("lost concurrent appends: %+v %v", s.Metadata, err)
	}
}
