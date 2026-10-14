package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectTrustCanonicalExactRootAndRevoke(t *testing.T) {
	home, base := t.TempDir(), t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	store := NewTrustStore(home)
	if yes, err := store.IsTrusted(repo); err != nil || yes {
		t.Fatal(yes, err)
	}
	id, err := Discover(filepath.Join(alias, "child"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Trust(id.Root); err != nil {
		t.Fatal(err)
	}
	if err := store.Trust(repo); err != nil {
		t.Fatal(err)
	}
	r, err := store.read()
	if err != nil || len(r.Trusted) != 1 {
		t.Fatal(r, err)
	}
	if yes, err := store.IsTrusted(alias); err != nil || !yes {
		t.Fatal(yes, err)
	}
	if yes, err := store.IsTrusted(base); err != nil || yes {
		t.Fatal("trust leaked to ancestor", yes, err)
	}
	if yes, err := store.IsTrusted(filepath.Join(repo, "child")); err != nil || yes {
		t.Fatal("store must compare exact project identity", yes, err)
	}
	info, err := os.Stat(store.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if err := store.Revoke(alias); err != nil {
		t.Fatal(err)
	}
	if yes, err := store.IsTrusted(repo); err != nil || yes {
		t.Fatal(yes, err)
	}
}

func TestProjectTrustDamagedStoreFailsClosed(t *testing.T) {
	for _, data := range []string{`not-json`, `{"trusted":["../repo"]}`, `{"trusted":[],"unexpected":true}`, `{"trusted":[],"trusted":[]}`} {
		t.Run(data, func(t *testing.T) {
			store := NewTrustStore(t.TempDir())
			if err := os.WriteFile(store.Path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if yes, err := store.IsTrusted(t.TempDir()); err == nil || yes {
				t.Fatal(yes, err)
			}
			if err := store.Trust(t.TempDir()); err == nil {
				t.Fatal("overwrote damaged registry")
			}
		})
	}
	store := NewTrustStore(t.TempDir())
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"trusted":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, store.Path); err != nil {
		t.Fatal(err)
	}
	if yes, err := store.IsTrusted(t.TempDir()); err == nil || yes {
		t.Fatal(yes, err)
	}
}

func TestProjectTrustUpdateLock(t *testing.T) {
	store := NewTrustStore(t.TempDir())
	if err := os.Mkdir(store.Path+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Trust(t.TempDir()); err == nil {
		t.Fatal("ignored concurrent update lock")
	}
}
