package sessionstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Locks are per path, shared between FileStore instances in this process.
// A store is intended to have a single owning process.
var fileLocks sync.Map

func pathLock(path string) *sync.Mutex {
	v, _ := fileLocks.LoadOrStore(path, &sync.Mutex{})
	return v.(*sync.Mutex)
}

type FileStore struct{ dir string }

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
	return &FileStore{dir: dir}, nil
}
func (f *FileStore) path(id string) string {
	return filepath.Join(f.dir, fmt.Sprintf("%x.jsonl", sha256.Sum256([]byte(id))))
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
func (f *FileStore) Create(ctx context.Context, s Snapshot) error {
	if s.Metadata.ID == "" {
		return errors.New("session ID required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p := f.path(s.Metadata.ID)
	mu := pathLock(p)
	mu.Lock()
	defer mu.Unlock()
	data, err := encode(creation(s))
	if err != nil {
		return err
	}
	// Publish only a complete, synced creation record.
	tmp, err := os.CreateTemp(f.dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(tmp.Name(), p); errors.Is(err, os.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	return syncDirectory(f.dir)
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
func readFile(p string) ([]byte, error) {
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
	return os.ReadFile(p)
}
func (f *FileStore) Append(ctx context.Context, id string, events ...Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := f.path(id)
	mu := pathLock(p)
	mu.Lock()
	defer mu.Unlock()
	data, err := readFile(p)
	if err != nil {
		return err
	}
	s, n, err := replay(data)
	if err != nil {
		return err
	}
	if s.Metadata.ID != id {
		return ErrCorrupted
	}
	for _, e := range events {
		if err := apply(&s, e); err != nil {
			return err
		}
	}
	encoded, err := encode(events)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(p, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if err := file.Truncate(int64(n)); err != nil {
		return err
	}
	if _, err := file.Seek(int64(n), 0); err != nil {
		return err
	}
	// A valid final event without a newline still needs a separator.
	if n > 0 && data[n-1] != '\n' {
		encoded = append([]byte{'\n'}, encoded...)
	}
	if _, err = file.Write(encoded); err != nil {
		_ = file.Truncate(int64(n))
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Truncate(int64(n))
		_ = file.Sync()
		return err
	}
	return file.Close()
}
func (f *FileStore) Load(ctx context.Context, id string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	p := f.path(id)
	mu := pathLock(p)
	mu.Lock()
	defer mu.Unlock()
	data, err := readFile(p)
	if err != nil {
		return Snapshot{}, err
	}
	s, _, err := replay(data)
	if err == nil && s.Metadata.ID != id {
		err = ErrCorrupted
	}
	return s, err
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
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(f.dir, entry.Name())
		mu := pathLock(p)
		mu.Lock()
		data, err := readFile(p)
		var s Snapshot
		if err == nil {
			s, _, err = replay(data)
		}
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		if f.path(s.Metadata.ID) != p {
			return nil, ErrCorrupted
		}
		out = append(out, s.Metadata)
	}
	return out, nil
}
func (f *FileStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := f.path(id)
	mu := pathLock(p)
	mu.Lock()
	defer mu.Unlock()
	err := os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return syncDirectory(f.dir)
}
