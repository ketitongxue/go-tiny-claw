package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
)

var hostIDPattern = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func ValidHostID(id string) bool { return hostIDPattern.MatchString(id) }

type hostRecord struct {
	OpenAI struct {
		ID string `json:"ext_agent_host_id"`
	} `json:"openai"`
}

// HostStore contains installation metadata, not credentials. Its path belongs
// to Global Agent Home, never to a workspace. It survives auth logout.
type HostStore struct{ path string }

func NewHostStore(path string) *HostStore { return &HostStore{path: path} }

func (s *HostStore) read() (string, error) {
	info, err := os.Lstat(s.path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("host store must be a regular file, not a symlink")
	}
	if err := os.Chmod(s.path, 0600); err != nil {
		return "", errors.New("cannot secure host store permissions")
	}
	var record hostRecord
	if err := fileutil.ReadJSON(s.path, &record); err != nil {
		return "", err
	}
	if !ValidHostID(record.OpenAI.ID) {
		return "", errors.New("invalid OpenAI host ID; restore host.json rather than changing an existing registration")
	}
	return record.OpenAI.ID, nil
}

func (s *HostStore) OpenAIHostID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id, err := s.read(); !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return "", err
	}
	// Serialize initial creation across processes, then write via atomic rename.
	// A crashed creator may leave a lock directory; never silently replace its ID.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lock := s.path + ".lock"
	for {
		if err := os.Mkdir(lock, 0700); err == nil {
			break
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return "", parent.Err()
			}
			return "", errors.New("host store creation locked; retry or remove a stale host.json.lock after confirming no login is running")
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer os.Remove(lock)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id, err := s.read(); !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", err
	}
	uuid[6] = uuid[6]&0x0f | 0x40
	uuid[8] = uuid[8]&0x3f | 0x80
	id := fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
	var record hostRecord
	record.OpenAI.ID = id
	if err := fileutil.WriteJSON(s.path, record); err != nil {
		return "", err
	}
	return id, nil
}
