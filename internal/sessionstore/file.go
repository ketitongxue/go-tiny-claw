package sessionstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Locks are per path, shared between FileStore instances in this process.
// They synchronize goroutines, not cross-process writer ownership.
var fileLocks sync.Map

func pathLock(path string) *sync.Mutex {
	if v, ok := fileLocks.Load(path); ok {
		return v.(*sync.Mutex)
	}
	v, _ := fileLocks.LoadOrStore(path, &sync.Mutex{})
	return v.(*sync.Mutex)
}

type fileSessionState struct {
	mu              *sync.Mutex
	loaded          bool
	snapshot        Snapshot
	committedOffset int64
	fileSize        int64
	info            os.FileInfo
	needsSeparator  bool
	needsRecovery   bool // a failed commit must roll back before replay/append
	writer          *fileSessionLock
}

type appendFile interface {
	Write([]byte) (int, error)
	Seek(int64, int) (int64, error)
	Truncate(int64) error
	Chmod(os.FileMode) error
	Stat() (os.FileInfo, error)
	Sync() error
	Close() error
}

type FileStore struct {
	dir    string
	states sync.Map
	// Instance-scoped I/O seams for deterministic replay/failure tests.
	read       func(string) ([]byte, error)
	openAppend func(string) (appendFile, error)
}

func NewFileStore(dir string) (*FileStore, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{filepath.Dir(dir), dir} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("session directory must not be a symlink")
		}
		if err := os.Chmod(p, 0700); err != nil {
			return nil, err
		}
	}
	return &FileStore{dir: dir, read: readFile, openAppend: func(p string) (appendFile, error) {
		return os.OpenFile(p, os.O_WRONLY, 0600)
	}}, nil
}
func (f *FileStore) path(id string) string {
	return filepath.Join(f.dir, fmt.Sprintf("%x.jsonl", sha256.Sum256([]byte(id))))
}
func (f *FileStore) state(id string) *fileSessionState {
	p := f.path(id)
	if v, ok := f.states.Load(p); ok {
		return v.(*fileSessionState)
	}
	v, _ := f.states.LoadOrStore(p, &fileSessionState{mu: pathLock(p)})
	return v.(*fileSessionState)
}
func encode(events []Event) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
func (f *FileStore) Create(ctx context.Context, s Snapshot) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Metadata.ID == "" {
		return errors.New("session ID required")
	}
	p := f.path(s.Metadata.ID)
	st := f.state(s.Metadata.ID)
	st.mu.Lock()
	defer st.mu.Unlock()
	release, err := f.mutationGuard(ctx, s.Metadata.ID, st)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, release()) }()
	events := creation(s)
	var committed Snapshot
	for _, e := range events {
		if err := apply(&committed, e); err != nil {
			return err
		}
	}
	data, err := encode(events)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Publish only a complete, synced creation record.
	tmp, err := os.CreateTemp(f.dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = tmp.Chmod(0600)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		var n int
		n, err = tmp.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if err = os.Link(tmp.Name(), p); errors.Is(err, os.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	if err := syncDirectory(f.dir); err != nil {
		return err
	}
	info, err := sessionFileInfo(p)
	if err != nil {
		return err
	}
	st.snapshot = committed
	st.committedOffset = int64(len(data))
	st.fileSize = int64(len(data))
	st.info = info
	st.needsSeparator = false
	st.needsRecovery = false
	st.loaded = true
	return nil
}

// replay returns the last committed byte offset. A torn final record is ignored,
// and removed before the next append, so future records cannot hide corruption.
func replay(data []byte) (Snapshot, int, error) {
	var s Snapshot
	offset := 0
	lines := bytes.Split(data, []byte{'\n'})
	for i, line := range lines {
		if len(line) == 0 && i == len(lines)-1 {
			break
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			if i == len(lines)-1 || i == len(lines)-2 && len(lines[len(lines)-1]) == 0 {
				break
			}
			return s, offset, ErrCorrupted
		}
		if err := apply(&s, e); err != nil {
			return s, offset, err
		}
		offset += len(line) + 1
	}
	if s.Metadata.ID == "" {
		return s, offset, ErrCorrupted
	}
	return s, min(offset, len(data)), nil
}
func sessionFileInfo(p string) (os.FileInfo, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("session file must be regular")
	}
	if err := os.Chmod(p, 0600); err != nil {
		return nil, err
	}
	return info, nil
}
func readFile(p string) ([]byte, error) {
	if _, err := sessionFileInfo(p); err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Caller holds st.mu. Size, identity and mtime detect changes outside this cache.
func (f *FileStore) ensureLoaded(ctx context.Context, id string, st *fileSessionState, fresh bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if st.needsRecovery {
		if err := f.recoverFailedCommit(id, st); err != nil {
			return err
		}
	}
	p := f.path(id)
	info, err := sessionFileInfo(p)
	if err != nil {
		st.loaded = false
		return err
	}
	if !fresh && st.loaded && info.Size() == st.fileSize && os.SameFile(info, st.info) && info.ModTime().Equal(st.info.ModTime()) {
		return ctx.Err()
	}
	st.loaded = false
	data, err := f.read(p)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s, n, err := replay(data)
	if err != nil {
		return err
	}
	if s.Metadata.ID != id {
		return ErrCorrupted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	st.snapshot = s
	st.committedOffset = int64(n)
	st.fileSize = int64(len(data))
	st.info = info
	st.needsSeparator = n > 0 && data[n-1] != '\n'
	st.loaded = true
	return nil
}

// Recovery is synchronous and deliberately not cancelable once a commit started.
// If rollback itself fails, retain the old snapshot/offset and retry it before
// the next operation; never replay a failed batch into committed memory.
func (f *FileStore) recoverFailedCommit(id string, st *fileSessionState) error {
	file, err := f.openAppend(f.path(id))
	if err != nil {
		return fmt.Errorf("session commit recovery: %w", err)
	}
	err = file.Truncate(st.committedOffset)
	if err == nil {
		err = file.Sync()
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if err = errors.Join(err, statErr, closeErr); err != nil {
		return fmt.Errorf("session commit recovery: %w", err)
	}
	st.fileSize = st.committedOffset
	st.info = info
	st.needsRecovery = false
	return nil
}

func (f *FileStore) Append(ctx context.Context, id string, events ...Event) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	st := f.state(id)
	st.mu.Lock()
	defer st.mu.Unlock()
	release, err := f.mutationGuard(ctx, id, st)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, release()) }()
	if err := f.ensureLoaded(ctx, id, st, false); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	next := mutationSnapshot(st.snapshot, len(events))
	for _, e := range events {
		if err := apply(&next, e); err != nil {
			return err
		}
	}
	encoded, err := encode(events)
	if err != nil {
		return err
	}
	if st.needsSeparator {
		encoded = append([]byte{'\n'}, encoded...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := f.openAppend(f.path(id))
	if err != nil {
		return err
	}
	// Last cancellation checkpoint: after mutation starts, finish sync/close or
	// rollback, even if ctx is canceled in the meantime.
	err = ctx.Err()
	if err == nil {
		err = file.Chmod(0600)
	}
	started := err == nil
	if err == nil && st.fileSize != st.committedOffset {
		err = file.Truncate(st.committedOffset)
	}
	if err == nil {
		_, err = file.Seek(st.committedOffset, io.SeekStart)
	}
	if err == nil {
		var n int
		n, err = file.Write(encoded)
		if err == nil && n != len(encoded) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = file.Sync()
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if err = errors.Join(err, statErr, closeErr); err != nil {
		if started {
			st.needsRecovery = true
			err = errors.Join(err, f.recoverFailedCommit(id, st))
		}
		return err
	}
	// Disk commit succeeded. Only now publish the candidate snapshot and offset.
	st.snapshot = next
	st.committedOffset += int64(len(encoded))
	st.fileSize = st.committedOffset
	st.info = info
	st.needsSeparator = false
	return nil
}
func (f *FileStore) Load(ctx context.Context, id string) (snap Snapshot, retErr error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	st := f.state(id)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.needsRecovery {
		release, err := f.mutationGuard(ctx, id, st)
		if err != nil {
			return Snapshot{}, err
		}
		defer func() { retErr = errors.Join(retErr, release()) }()
	}
	// Without writer ownership another process can change the file; always
	// replay a fresh read-only view. Cached Load is reserved for the owner.
	if err := f.ensureLoaded(ctx, id, st, st.writer == nil); err != nil {
		return Snapshot{}, err
	}
	return clone(st.snapshot), nil
}
func (f *FileStore) List(ctx context.Context) ([]Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}
	out := []Metadata{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(f.dir, entry.Name())
		mu := pathLock(p)
		mu.Lock()
		data, err := f.read(p)
		var s Snapshot
		if err == nil {
			s, _, err = replay(data)
		}
		mu.Unlock()
		if errors.Is(err, ErrNotFound) {
			continue // another process may have deleted it after ReadDir
		}
		if err != nil {
			return nil, err
		}
		if f.path(s.Metadata.ID) != p {
			return nil, ErrCorrupted
		}
		out = append(out, s.Metadata)
	}
	return out, ctx.Err()
}
func (f *FileStore) Delete(ctx context.Context, id string) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	st := f.state(id)
	st.mu.Lock()
	defer st.mu.Unlock()
	// Even this store's own active lifecycle lease prevents Delete.
	if st.writer != nil {
		return ErrInUse
	}
	release, err := f.mutationGuard(ctx, id, st)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, release()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	err = os.Remove(f.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	st.loaded = false
	st.needsRecovery = false
	return syncDirectory(f.dir)
}
