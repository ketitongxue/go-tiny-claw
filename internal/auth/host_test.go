package auth

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHostIDPersistenceAndDifferentInstallations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "host.json")
	ctx := context.Background()
	first, err := NewHostStore(path).OpenAIHostID(ctx)
	if err != nil || !ValidHostID(first) {
		t.Fatal("invalid host ID", err)
	}
	second, err := NewHostStore(path).OpenAIHostID(ctx)
	if err != nil || second != first {
		t.Fatal("host ID changed across attempts/restarts", err)
	}
	other, err := NewHostStore(filepath.Join(t.TempDir(), "host.json")).OpenAIHostID(ctx)
	if err != nil || other == first {
		t.Fatal("installations shared a host ID", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("host permissions")
	}
	if err := NewFileStore(filepath.Join(filepath.Dir(path), "auth.json")).Delete(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	after, err := NewHostStore(path).OpenAIHostID(ctx)
	if err != nil || after != first {
		t.Fatal("logout changed installation ID", err)
	}
}
func TestConcurrentHostCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := NewHostStore(path).OpenAIHostID(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent host ID changed")
		}
	}
}
func TestHostRejectsMalformedAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	if err := os.WriteFile(path, []byte(`{"openai":{"ext_agent_host_id":"not-a-uuid"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHostStore(path).OpenAIHostID(context.Background()); err == nil {
		t.Fatal("malformed host silently replaced")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHostStore(link).OpenAIHostID(context.Background()); err == nil {
		t.Fatal("host symlink followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewHostStore(path).OpenAIHostID(ctx); err != context.Canceled {
		t.Fatal(err)
	}
}
