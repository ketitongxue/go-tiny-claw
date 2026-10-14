package app

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/openai"
)

func runtimeOAuth(t *testing.T, r *Runtime) auth.OAuthCredential {
	t.Helper()
	id, err := r.Hosts.OpenAIHostID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return auth.OAuthCredential{ClientID: "oaiapp_test", ExtAgentHostID: id, Issuer: openai.OAuthIssuer, Subject: "subject-1", Email: "test@example.com", IDToken: "private-id", AccessToken: "private-access", RefreshToken: "private-refresh", ExpiresAt: time.Now().Add(time.Hour), Scopes: strings.Fields(openai.OAuthScopes)}
}
func openAIModelIDs(r *Runtime) string {
	var ids []string
	for _, m := range r.Models.List() {
		if m.ProviderID == "openai" {
			ids = append(ids, m.ID)
		}
	}
	return strings.Join(ids, ",")
}
func TestRuntimeHostIDAcrossLoginAttemptsAndRestart(t *testing.T) {
	paths, _ := config.NewPaths(t.TempDir())
	r, err := Load(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.OpenAILoginClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := first.Begin()
	u1, _ := url.Parse(s1.AuthorizationURL)
	r, err = Load(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.OpenAILoginClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := second.Begin()
	u2, _ := url.Parse(s2.AuthorizationURL)
	if u1.Query().Get("ext_agent_host_id") != u2.Query().Get("ext_agent_host_id") || u2.Query().Get("client_id") != auth.DynamicClientID {
		t.Fatal("host changed on returning attempt before registration")
	}
	c := runtimeOAuth(t, r)
	if err := r.Auth.Login(context.Background(), "openai", c); err != nil {
		t.Fatal(err)
	}
	client, err := r.OpenAILoginClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, _ := client.Begin()
	u, _ := url.Parse(s.AuthorizationURL)
	if u.Query().Get("client_id") != c.ClientID || u.Query().Get("id_token_hint") != c.IDToken || u.Query().Get("ext_agent_host_id") != c.ExtAgentHostID {
		t.Fatal("saved registration not used")
	}
}
func TestRuntimeUsesAuthSpecificCatalogAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	paths, _ := config.NewPaths(t.TempDir())
	env := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "api-key"
		}
		return ""
	}
	r, err := Load(paths, env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(openAIModelIDs(r), "gpt-5.2") {
		t.Fatal("API catalog absent")
	}
	c := runtimeOAuth(t, r)
	if err := r.Auth.Login(ctx, "openai", c); err != nil {
		t.Fatal(err)
	}
	r.DiscoverOpenAI = func(_ context.Context, got auth.OAuthCredential) ([]provider.ModelInfo, error) {
		if got.ClientID != c.ClientID {
			t.Error("wrong registration")
		}
		return []provider.ModelInfo{{ProviderID: "openai", ID: "model-a", Name: "Model A", ToolCalling: true, BillingMode: provider.BillingSubscription}}, nil
	}
	if err := r.RefreshOpenAICatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if openAIModelIDs(r) != "model-a" {
		t.Fatal("OAuth reused static API-key catalog")
	}
	r, err = Load(paths, env)
	if err != nil || openAIModelIDs(r) != "model-a" {
		t.Fatal("OAuth cache/restart", err)
	}
	if _, _, err := r.Manager.Inspect(ctx, provider.ModelRef{ProviderID: "openai", ModelID: "gpt-5.2"}); err == nil {
		t.Fatal("API catalog still available under OAuth")
	}
	if _, info, err := r.Manager.Inspect(ctx, provider.ModelRef{ProviderID: "openai", ModelID: "model-a"}); err != nil || info.Type != auth.CredentialOAuth {
		t.Fatal("OAuth model unavailable", err)
	}
	if err := r.Auth.Logout(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	if err := r.ClearOpenAICatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.OpenAIModels); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("logout kept account model cache")
	}
	if !strings.Contains(openAIModelIDs(r), "gpt-5.2") {
		t.Fatal("API-key env regression after logout")
	}
}
func TestLegacyOrMissingPlanOAuthDoesNotFallback(t *testing.T) {
	paths, _ := config.NewPaths(t.TempDir())
	store := auth.NewFileStore(paths.Auth)
	ctx := context.Background()
	legacy := auth.OAuthCredential{AccessToken: "legacy-access", RefreshToken: "legacy-refresh", AccountID: "legacy-account", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Save(ctx, "openai", legacy); err != nil {
		t.Fatal(err)
	}
	env := func(string) string { return "api-key" }
	r, err := Load(paths, env)
	if err != nil {
		t.Fatal("legacy record unreadable", err)
	}
	if openAIModelIDs(r) != "" {
		t.Fatal("legacy OAuth inherited API catalog")
	}
	if _, err := r.Auth.GetCredential(ctx, "openai"); err == nil {
		t.Fatal("legacy OAuth silently routed to API key")
	}
	if err := r.Auth.Logout(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	c := runtimeOAuth(t, r)
	c.Scopes = []string{"openid"}
	if err := r.Auth.Login(ctx, "openai", c); err != nil {
		t.Fatal("identity-only login rejected", err)
	}
	if err := r.LoadOpenAICatalog(ctx); err != nil || openAIModelIDs(r) != "" {
		t.Fatal("missing scope inherited models", err)
	}
	if err := r.RefreshOpenAICatalog(ctx); err != openai.ErrPlanUsageDisabled {
		t.Fatal(err)
	}
}
