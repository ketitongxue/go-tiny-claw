package provider

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type fakeProvider struct{ content string }

func (p fakeProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: p.content}, nil
}
func TestManager(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry()
	models := NewModelRegistry()
	store := auth.NewFileStore(filepath.Join(t.TempDir(), "auth.json"))
	am := auth.NewManager(store, nil)
	for _, id := range []string{"a", "b"} {
		d := testDefinition(id)
		d.Factory = func(_ context.Context, m ModelInfo, c auth.Credential, _ ProviderConfig) (LLMProvider, error) {
			if c.(auth.APIKeyCredential).Key != "key-"+m.ProviderID {
				t.Error("wrong credential")
			}
			return fakeProvider{content: m.Ref().String()}, nil
		}
		if err := registry.Register(d); err != nil {
			t.Fatal(err)
		}
		for _, m := range d.Models {
			if err := models.Register(m); err != nil {
				t.Fatal(err)
			}
		}
		_ = am.Register(id, auth.Policy{Methods: []auth.CredentialType{auth.CredentialAPIKey}})
		if err := am.Login(ctx, id, auth.APIKeyCredential{Key: "key-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewManager(registry, models, am)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		ref := ModelRef{ProviderID: id, ModelID: "model"}
		p, m, err := manager.Create(ctx, ref)
		if err != nil || m.Ref() != ref {
			t.Fatal(m, err)
		}
		msg, err := p.Generate(ctx, nil, nil)
		if err != nil || msg.Content != ref.String() {
			t.Fatal("wrong factory", err)
		}
	}
	if len(manager.ListAvailable(ctx)) != 2 {
		t.Fatal("available models")
	}
	_ = am.Logout(ctx, "a")
	if _, _, err := manager.Create(ctx, ModelRef{"a", "model"}); err == nil {
		t.Fatal("credential missing accepted")
	}
	if _, _, err := manager.Create(ctx, ModelRef{"a", "missing"}); err == nil {
		t.Fatal("unknown model")
	}
	_ = models.Register(ModelInfo{ProviderID: "b", ID: "no-tools"})
	if _, _, err := manager.Create(ctx, ModelRef{"b", "no-tools"}); err == nil {
		t.Fatal("no-tools accepted")
	}
	_ = models.Register(ModelInfo{ProviderID: "unregistered", ID: "model", ToolCalling: true})
	if _, _, err := manager.Create(ctx, ModelRef{"unregistered", "model"}); err == nil {
		t.Fatal("missing provider accepted")
	}
}
func TestManagedProviderRefreshesAfterCreation(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	models := NewModelRegistry()
	store := auth.NewFileStore(filepath.Join(t.TempDir(), "auth.json"))
	am := auth.NewManager(store, nil)
	d := testDefinition("oauth")
	d.AuthMethods = []AuthMethod{auth.CredentialOAuth}
	d.BillingByAuth = map[AuthMethod]BillingMode{auth.CredentialOAuth: BillingSubscription}
	d.Factory = func(_ context.Context, m ModelInfo, c auth.Credential, _ ProviderConfig) (LLMProvider, error) {
		return fakeProvider{content: c.(auth.OAuthCredential).AccessToken}, nil
	}
	_ = r.Register(d)
	_ = models.Register(d.Models[0])
	_ = am.Register(d.ID, auth.Policy{Methods: []auth.CredentialType{auth.CredentialOAuth}, Refresh: func(ctx context.Context, c auth.OAuthCredential) (auth.OAuthCredential, error) {
		c.AccessToken = "new"
		c.ExpiresAt = time.Now().Add(time.Hour)
		return c, nil
	}})
	c := auth.OAuthCredential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}
	_ = am.Login(ctx, d.ID, c)
	manager, _ := NewManager(r, models, am)
	p, m, err := manager.Create(ctx, d.Models[0].Ref())
	if err != nil || m.BillingMode != BillingSubscription {
		t.Fatal(err)
	}
	c.ExpiresAt = time.Now().Add(-time.Hour)
	_ = am.Login(ctx, d.ID, c)
	msg, err := p.Generate(ctx, nil, nil)
	if err != nil || msg.Content != "new" {
		t.Fatal("stale startup token", err)
	}
}
