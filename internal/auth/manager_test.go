package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerAPIKeyPriorityAndLogout(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "auth.json"))
	env := map[string]string{"ZAI_CODING_CN_API_KEY": "new", "ZHIPU_API_KEY": "legacy"}
	m := NewManager(store, func(key string) string { return env[key] })
	if err := m.Register("zai-coding-cn", Policy{Methods: []CredentialType{CredentialAPIKey}, EnvVars: []string{"ZAI_CODING_CN_API_KEY", "ZHIPU_API_KEY"}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	check := func(want string) {
		t.Helper()
		c, err := m.GetCredential(ctx, "zai-coding-cn")
		if err != nil || c.(APIKeyCredential).Key != want {
			t.Fatal("priority failed", err)
		}
	}
	check("new")
	delete(env, "ZAI_CODING_CN_API_KEY")
	check("legacy")
	if err := m.Login(ctx, "zai-coding-cn", APIKeyCredential{Key: "stored"}); err != nil {
		t.Fatal(err)
	}
	check("stored")
	info, err := m.Inspect(ctx, "zai-coding-cn")
	if err != nil || info.Source != "store" {
		t.Fatal(info, err)
	}
	if err := m.Logout(ctx, "zai-coding-cn"); err != nil {
		t.Fatal(err)
	}
	check("legacy")
	if err := m.LoginFromEnv(ctx, "zai-coding-cn"); err != nil {
		t.Fatal(err)
	}
	check("legacy")
	_ = m.Logout(ctx, "zai-coding-cn")
	delete(env, "ZHIPU_API_KEY")
	if _, err := m.GetCredential(ctx, "zai-coding-cn"); err == nil || !strings.Contains(err.Error(), "claw auth login") {
		t.Fatal(err)
	}
	_ = m.Register("local", Policy{Methods: []CredentialType{CredentialNone}})
	if c, err := m.GetCredential(ctx, "local"); err != nil || c.Type() != CredentialNone {
		t.Fatal(c, err)
	}
}
func TestManagerSingleRefreshAndOfflineInspect(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "auth.json"))
	m := NewManager(store, nil)
	var calls atomic.Int32
	err := m.Register("openai", Policy{Methods: []CredentialType{CredentialOAuth}, Refresh: func(ctx context.Context, c OAuthCredential) (OAuthCredential, error) {
		calls.Add(1)
		c.AccessToken = "refreshed"
		c.RefreshToken = "rotated"
		c.ExpiresAt = time.Now().Add(time.Hour)
		return c, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	expired := OAuthCredential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := m.Login(ctx, "openai", expired); err != nil {
		t.Fatal(err)
	}
	if info, err := m.Inspect(ctx, "openai"); err != nil || !info.NeedsRefresh || calls.Load() != 0 {
		t.Fatal(info, err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := m.GetCredential(ctx, "openai")
			if err != nil || c.(OAuthCredential).AccessToken != "refreshed" {
				t.Error("refresh", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate refresh", calls.Load())
	}
	c, err := store.Load(ctx, "openai")
	if err != nil || c.(OAuthCredential).RefreshToken != "rotated" {
		t.Fatal("refresh not saved")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.GetCredential(cancelled, "openai"); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := m.GetCredential(ctx, "openai"); err != nil {
		t.Fatal("cancellation leaked lock", err)
	}
	expired.RefreshToken = ""
	_ = m.Login(ctx, "openai", expired)
	if _, err := m.Inspect(ctx, "openai"); err == nil {
		t.Fatal("expired credential accepted")
	}
	if _, err := m.GetCredential(ctx, "openai"); err == nil {
		t.Fatal("expired credential accepted")
	}
}

func TestEarliestRefreshAndScopeReduction(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(filepath.Join(t.TempDir(), "auth.json"))
	m := NewManager(store, nil)
	now := time.Now()
	m.now = func() time.Time { return now }
	var calls int
	permissionError := errors.New("plan usage disabled")
	if err := m.Register("openai", Policy{Methods: []CredentialType{CredentialOAuth}, CanUse: func(c Credential) error {
		if !c.(OAuthCredential).HasScope(testPlanScope) {
			return permissionError
		}
		return nil
	}, Refresh: func(ctx context.Context, c OAuthCredential) (OAuthCredential, error) {
		calls++
		c.AccessToken = "new"
		c.RefreshToken = "rotated"
		c.ExpiresAt = now.Add(time.Hour)
		c.Scopes = []string{"openid"}
		c.Scope = ""
		return c, nil
	}}); err != nil {
		t.Fatal(err)
	}
	c := OAuthCredential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: now.Add(30 * time.Second), EarliestRefreshAt: now.Add(20 * time.Second), Scopes: []string{testPlanScope}}
	if err := m.Login(ctx, "openai", c); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetCredential(ctx, "openai"); err != nil || calls != 0 {
		t.Fatal("refreshed before earliest_refresh_at", err)
	}
	if info, err := m.Inspect(ctx, "openai"); err != nil || !info.RefreshBlocked {
		t.Fatal("offline refresh policy", err)
	}
	now = now.Add(21 * time.Second)
	if _, err := m.GetCredential(ctx, "openai"); !errors.Is(err, permissionError) || calls != 1 {
		t.Fatal("scope removal not enforced", err)
	}
	stored, _ := store.Load(ctx, "openai")
	if saved := stored.(OAuthCredential); saved.RefreshToken != "rotated" || saved.AccessToken != "new" || saved.HasScope(testPlanScope) {
		t.Fatal("rotation discarded on permission reduction")
	}
	// Identity-only login is accepted, but cannot request inference/refresh.
	if err := m.Login(ctx, "openai", stored); err != nil {
		t.Fatal(err)
	}
	if info, err := m.Inspect(ctx, "openai"); !errors.Is(err, permissionError) || info.Available {
		t.Fatal("missing plan grant accepted", err)
	}
	c.EarliestRefreshAt = now.Add(time.Hour)
	c.ExpiresAt = now.Add(-time.Second)
	if err := m.Login(ctx, "openai", c); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetCredential(ctx, "openai"); err == nil || calls != 1 {
		t.Fatal("expired token refreshed too early")
	}
}

func TestManagerRejectsReturningIdentityChange(t *testing.T) {
	ctx := context.Background()
	m := NewManager(NewFileStore(filepath.Join(t.TempDir(), "auth.json")), nil)
	_ = m.Register("openai", Policy{Methods: []CredentialType{CredentialOAuth}})
	c := OAuthCredential{ClientID: "oaiapp_test", Issuer: testIssuer, Subject: "subject-1", AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour)}
	if err := m.Login(ctx, "openai", c); err != nil {
		t.Fatal(err)
	}
	changed := c
	changed.Subject = "other"
	if err := m.Login(ctx, "openai", changed); err == nil {
		t.Fatal("changed subject overwrote stored identity")
	}
	changed = c
	changed.ClientID = "oaiapp_other"
	if err := m.Login(ctx, "openai", changed); err == nil {
		t.Fatal("changed registration overwrote credentials")
	}
	stored, _ := m.StoredCredential(ctx, "openai")
	if stored.(OAuthCredential).Subject != c.Subject {
		t.Fatal("credential overwritten")
	}
}
