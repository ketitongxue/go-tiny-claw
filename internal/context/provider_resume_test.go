package context_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

type resumedProvider struct{ ref string }

func (p resumedProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: p.ref}, nil
}
func TestProviderResumeResolvesCredentials(t *testing.T) {
	for _, id := range []string{"openai", "zai-coding-cn", "custom"} {
		t.Run(id, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			authPath := filepath.Join(home, "auth.json")
			fs, err := sessionstore.NewFileStore(filepath.Join(home, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			method := auth.CredentialAPIKey
			if id == "openai" {
				method = auth.CredentialOAuth
			}
			model := provider.ModelInfo{ProviderID: id, ID: "model-a", ToolCalling: true}
			refreshes := 0
			setup := func() *provider.Manager {
				am := auth.NewManager(auth.NewFileStore(authPath), nil)
				if err := am.Register(id, auth.Policy{Methods: []auth.CredentialType{method}, Refresh: func(_ context.Context, c auth.OAuthCredential) (auth.OAuthCredential, error) {
					refreshes++
					c.AccessToken = "refreshed-secret"
					c.ExpiresAt = time.Now().Add(time.Hour)
					return c, nil
				}}); err != nil {
					t.Fatal(err)
				}
				r := provider.NewRegistry()
				models := provider.NewModelRegistry()
				if err := models.Register(model); err != nil {
					t.Fatal(err)
				}
				if err := r.Register(provider.ProviderDefinition{ID: id, Name: id, AuthMethods: []provider.AuthMethod{method}, Models: []provider.ModelInfo{model}, Factory: func(_ context.Context, m provider.ModelInfo, c auth.Credential, _ provider.ProviderConfig) (provider.LLMProvider, error) {
					if c.Type() != method {
						t.Fatal("wrong auth method")
					}
					return resumedProvider{ref: m.Ref().String()}, nil
				}}); err != nil {
					t.Fatal(err)
				}
				mgr, err := provider.NewManager(r, models, am)
				if err != nil {
					t.Fatal(err)
				}
				return mgr
			}
			am := auth.NewManager(auth.NewFileStore(authPath), nil)
			_ = am.Register(id, auth.Policy{Methods: []auth.CredentialType{method}})
			var cred auth.Credential = auth.APIKeyCredential{Key: "api-secret"}
			if method == auth.CredentialOAuth {
				cred = auth.OAuthCredential{AccessToken: "oauth-secret", RefreshToken: "refresh-secret", ExpiresAt: time.Now().Add(time.Hour)}
			}
			if err := am.Login(ctx, id, cred); err != nil {
				t.Fatal(err)
			}
			sess, err := ctxpkg.NewSessionService(fs).Create(ctx, "s", t.TempDir(), model, string(method))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := setup().Create(ctx, sess.ModelRef()); err != nil {
				t.Fatal(err)
			}
			if method == auth.CredentialOAuth {
				c := cred.(auth.OAuthCredential)
				c.ExpiresAt = time.Now().Add(-time.Hour)
				if err := am.Login(ctx, id, c); err != nil {
					t.Fatal(err)
				}
			}
			if err := sess.Close(); err != nil {
				t.Fatal(err)
			}
			resumed, err := ctxpkg.NewSessionService(fs).Resume(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = resumed.Close() })
			p, _, err := setup().Create(ctx, resumed.ModelRef())
			if err != nil {
				t.Fatal(err)
			}
			msg, err := p.Generate(ctx, nil, nil)
			if err != nil || msg.Content != id+"/model-a" {
				t.Fatal(err)
			}
			if method == auth.CredentialOAuth && refreshes != 1 {
				t.Fatal("resume did not use AuthManager refresh")
			}
			entries, _ := os.ReadDir(filepath.Join(home, "sessions"))
			for _, e := range entries {
				data, _ := os.ReadFile(filepath.Join(home, "sessions", e.Name()))
				if strings.Contains(string(data), "secret") {
					t.Fatal("credential in session")
				}
			}
			if _, _, err := setup().Create(ctx, provider.ModelRef{ProviderID: id, ModelID: "missing"}); err == nil {
				t.Fatal("missing model accepted")
			}
			if err := am.Logout(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, _, err := setup().Create(ctx, resumed.ModelRef()); err == nil {
				t.Fatal("missing credential accepted")
			}
		})
	}
}
