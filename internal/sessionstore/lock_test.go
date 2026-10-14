package sessionstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreWriterOwnershipAndReadOnly(t *testing.T) {
	ctx := context.Background()
	f := newTestFileStore(t)
	id := "../../active-session"
	lock, err := f.AcquireWriter(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	if err := f.Create(ctx, snapshot(id)); err != nil {
		t.Fatal(err)
	}
	other, err := NewFileStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []*FileStore{f, other} {
		if _, err := store.AcquireWriter(ctx, id); !errors.Is(err, ErrInUse) {
			t.Fatalf("second writer error = %v", err)
		}
		if err := store.Delete(ctx, id); !errors.Is(err, ErrInUse) {
			t.Fatalf("active delete error = %v", err)
		}
		if _, err := store.Load(ctx, id); err != nil {
			t.Fatal("active read rejected", err)
		}
		if _, err := store.List(ctx); err != nil {
			t.Fatal("active list rejected", err)
		}
	}
	for _, mutation := range []func() error{
		func() error { return other.Create(ctx, snapshot(id)) },
		func() error { return other.Append(ctx, id, messageEvent("conflict")) },
		func() error {
			e := NewEvent("usage")
			e.Usage = &Usage{PromptTokens: 1}
			return other.Append(ctx, id, e)
		},
	} {
		if err := mutation(); !errors.Is(err, ErrInUse) {
			t.Fatalf("mutation error = %v", err)
		}
	}
	if filepath.Dir(f.lockPath(id)) != f.dir || len(filepath.Base(f.lockPath(id))) != 64+len(".lock") {
		t.Fatal("lock path not SHA256-based")
	}
	info, err := os.Stat(f.lockPath(id))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("lock permissions", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal("Release is not idempotent", err)
	}
	if err := other.Delete(ctx, id); err != nil {
		t.Fatal("inactive delete rejected", err)
	}
	if _, err := os.Stat(f.lockPath(id)); err != nil {
		t.Fatal("Delete must retain the lock inode", err)
	}
	if err := f.Create(ctx, snapshot(id)); err != nil {
		t.Fatal("recreate rejected", err)
	}
	if err := f.Append(ctx, id, messageEvent("recreated")); err != nil {
		t.Fatal(err)
	}
	s, err := f.Load(ctx, id)
	if err != nil || len(s.History) != 1 {
		t.Fatal("stale cache after recreate", err)
	}
}

func TestWriterLockCancellationAndSymlink(t *testing.T) {
	f := newTestFileStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.AcquireWriter(ctx, "canceled"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.Create(ctx, snapshot("canceled")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.Append(ctx, "canceled", messageEvent("not written")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.lockPath("canceled")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled operation created a lock file", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("not a lock"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.lockPath("symlink")); err != nil {
		t.Fatal(err)
	}
	if lock, err := f.AcquireWriter(context.Background(), "symlink"); err == nil {
		_ = lock.Release()
		t.Fatal("followed lock symlink")
	}
}
