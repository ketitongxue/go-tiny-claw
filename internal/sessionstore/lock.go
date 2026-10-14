package sessionstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SessionLock holds single-writer ownership until Release (or process exit).
// Release is idempotent. Read-only Load/List never need this ownership.
type SessionLock interface {
	Release() error
}

type fileSessionLock struct {
	state    *fileSessionState
	file     *os.File
	released bool // protected by state.mu
}

func (l *fileSessionLock) Release() error {
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	l.state.writer = nil
	return releaseWriterFile(l.file)
}

func (f *FileStore) lockPath(id string) string {
	return filepath.Join(f.dir, fmt.Sprintf("%x.lock", sha256.Sum256([]byte(id))))
}

// AcquireWriter is nonblocking: contention returns ErrInUse, never waits for an
// active bot to finish. It may precede Create, protecting load/create/delete races.
func (f *FileStore) AcquireWriter(ctx context.Context, id string) (SessionLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st := f.state(id)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if st.writer != nil {
		return nil, ErrInUse
	}
	file, err := acquireWriterFile(f.lockPath(id))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, releaseWriterFile(file))
	}
	lock := &fileSessionLock{state: st, file: file}
	st.writer = lock
	return lock, nil
}

// Caller holds st.mu. Low-level Store mutations are also protected when called
// without a lifecycle lease, but the runtime holds a lease across the entire run.
func (f *FileStore) mutationGuard(ctx context.Context, id string, st *fileSessionState) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if st.writer != nil {
		return func() error { return nil }, nil
	}
	file, err := acquireWriterFile(f.lockPath(id))
	if err != nil {
		return nil, err
	}
	return func() error { return releaseWriterFile(file) }, nil
}
