package auth

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global", "auth.json")
	store := NewFileStore(path)
	ctx := context.Background()
	if _, err := store.Load(ctx, "openai"); err != ErrNotFound {
		t.Fatal(err)
	}
	c := OAuthCredential{AccessToken: "secret-access", RefreshToken: "secret-refresh", IDToken: "secret-id", ExpiresAt: time.Now().Add(-time.Hour).UTC(), ExpiresIn: 3600, EarliestRefreshAt: time.Now().Add(time.Minute).UTC(), ClientID: "oaiapp_test", ExtAgentHostID: testHostID, Issuer: testIssuer, Subject: "subject", Email: "test@example.com", Scopes: strings.Fields(testScopes), TokenType: "Bearer"}
	if err := store.Save(ctx, "openai", c); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx, "openai")
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatal("load failed", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("permission repair")
	}
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Save(ctx, id, APIKeyCredential{Key: "test-key"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := store.Load(ctx, id); err != nil {
			t.Fatal("lost concurrent save", err)
		}
	}
	if err := store.Delete(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "openai"); err != ErrNotFound {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if len(files) != 0 {
		t.Fatal(files)
	}
	original, _ := os.ReadFile(path)
	if err := store.Save(ctx, "a", APIKeyCredential{}); err == nil {
		t.Fatal("invalid saved")
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != string(original) {
		t.Fatal("failed save damaged store")
	}
	if err := store.Save(ctx, "openai", OAuthCredential{AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour), ClientID: DynamicClientID}); err == nil {
		t.Fatal("registration entry point persisted")
	}
	for _, bad := range []string{`{"secret-access":`, `null`, `{"openai":{"type":"oauth","access_token":"secret-access"}}`, `{"openai":{"type":"unknown","api_key":"secret-access"}}`} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(ctx, "openai"); err == nil || strings.Contains(err.Error(), "secret-access") {
			t.Fatal("unsafe malformed error", err)
		}
	}
}
func TestFileStoreRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(target, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := NewFileStore(path).Save(context.Background(), "openai", APIKeyCredential{Key: "secret"}); err == nil {
		t.Fatal("followed symlink")
	}
}
