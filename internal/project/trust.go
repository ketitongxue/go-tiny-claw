package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
)

// TrustStore is user-owned global configuration, never repository configuration.
// It grants prompt-resource loading, not execution rights or sandbox exemptions.
type TrustStore struct{ Path string }

type trustRecord struct {
	Trusted []string `json:"trusted"`
}

func NewTrustStore(home string) *TrustStore {
	return &TrustStore{Path: filepath.Join(home, "trusted-projects.json")}
}

func (s *TrustStore) read() (trustRecord, error) {
	r := trustRecord{Trusted: []string{}}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() {
		return r, errors.New("trust registry must be a regular file, not a symlink")
	}
	if err := os.Chmod(s.Path, 0600); err != nil {
		return r, errors.New("cannot secure trust registry permissions")
	}
	if err := fileutil.ReadJSON(s.Path, &r); err != nil {
		return r, err
	}
	seen := map[string]bool{}
	for _, path := range r.Trusted {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || seen[path] {
			return r, errors.New("invalid or duplicate path in trust registry")
		}
		seen[path] = true
	}
	return r, nil
}

func (s *TrustStore) IsTrusted(root string) (bool, error) {
	canonical, err := canonicalDir(root)
	if err != nil {
		return false, err
	}
	r, err := s.read()
	if err != nil {
		return false, err
	}
	for _, path := range r.Trusted {
		if path == canonical {
			return true, nil
		}
	}
	return false, nil
}

func (s *TrustStore) Trust(root string) error  { return s.update(root, true) }
func (s *TrustStore) Revoke(root string) error { return s.update(root, false) }

func (s *TrustStore) update(root string, trusted bool) error {
	canonical, err := canonicalDir(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	// Serialize read-modify-write across processes. A stale lock fails closed;
	// never discard another trust/revoke operation via last-writer-wins.
	lock := s.Path + ".lock"
	if err := os.Mkdir(lock, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("trust registry locked; retry, or remove stale %s after confirming no trust command is running", lock)
		}
		return err
	}
	defer os.Remove(lock)
	r, err := s.read()
	if err != nil {
		return err
	}
	paths := map[string]bool{}
	for _, path := range r.Trusted {
		paths[path] = true
	}
	if trusted {
		paths[canonical] = true
	} else {
		delete(paths, canonical)
	}
	r.Trusted = []string{}
	for path := range paths {
		r.Trusted = append(r.Trusted, path)
	}
	sort.Strings(r.Trusted)
	return fileutil.WriteJSON(s.Path, r)
}
