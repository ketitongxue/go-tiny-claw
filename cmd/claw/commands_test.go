package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/app"
	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/openai"
)

func TestCommands(t *testing.T) {
	paths, _ := config.NewPaths(t.TempDir())
	env := map[string]string{"ZAI_CODING_CN_API_KEY": "secret-env"}
	r, err := app.Load(paths, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	r.DiscoverOpenAI = func(context.Context, auth.OAuthCredential) ([]provider.ModelInfo, error) {
		return []provider.ModelInfo{{ProviderID: "openai", ID: "model-a", Name: "Model A", ToolCalling: true, BillingMode: provider.BillingSubscription}}, nil
	}
	var out bytes.Buffer
	cio := commandIO{output: &out, readSecret: func() (string, error) { return "secret-input", nil }, oauthLogin: func(context.Context, auth.LoginOptions) (auth.OAuthCredential, error) {
		hostID, err := r.Hosts.OpenAIHostID(context.Background())
		return auth.OAuthCredential{AccessToken: "secret-access", RefreshToken: "secret-refresh", IDToken: "secret-id", ClientID: "oaiapp_test", ExtAgentHostID: hostID, Issuer: openai.OAuthIssuer, Subject: "subject-1", Scopes: strings.Fields(openai.OAuthScopes), ExpiresAt: time.Now().Add(time.Hour)}, err
	}}
	ctx := context.Background()
	for _, args := range [][]string{{"auth", "login"}, {"auth", "login", "zai-coding-cn", "--from-env"}, {"auth", "login", "openai", "--method", "api-key"}, {"models"}, {"model", "set", "zai-coding-cn/glm-5.3"}, {"model", "current"}, {"auth", "logout", "openai"}, {"auth", "login", "openai"}, {"model", "set", "openai/model-a"}} {
		out.Reset()
		if err := runCommand(ctx, args, r, cio); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(out.String(), "secret-") {
			t.Fatal("credential logged")
		}
		if args[0] == "model" && args[1] == "set" {
			c, err := config.Load(paths.Config)
			if err != nil || c.DefaultModel != args[2] {
				t.Fatal(c, err)
			}
			r.Config = c
		}
	}
	out.Reset()
	if err := runCommand(ctx, []string{"models"}, r, cio); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "subscription") || !strings.Contains(out.String(), "oauth") {
		t.Fatal(out.String())
	}
	before, _ := os.ReadFile(paths.Config)
	for _, args := range [][]string{{"model", "set", "openai/unknown"}, {"model", "set", "bad"}, {"auth", "logout", "unknown"}, {"auth", "login", "unknown"}, {"auth", "login", "zai-coding-cn", "--method", "oauth"}, {"auth", "login", "openai", "--method", "bad"}, {"model"}, {"models", "unexpected"}} {
		if err := runCommand(ctx, args, r, cio); err == nil {
			t.Fatal("invalid command accepted", args)
		}
	}
	after, _ := os.ReadFile(paths.Config)
	if string(before) != string(after) {
		t.Fatal("invalid command changed default")
	}
}
func TestDispatchAndMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("ZAI_CODING_CN_API_KEY", "")
	t.Setenv("ZHIPU_API_KEY", "legacy-secret")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"auth", "login", "zai-coding-cn", "--from-env"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"model", "set", "zai-coding-cn/glm-5.3"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"-mode", "demo", "-check-config", "-model", "glm-5.3", "-workdir", t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "已弃用") || !strings.Contains(out.String(), "Credential: available (store)") || strings.Contains(out.String(), "legacy-secret") {
		t.Fatal(out.String())
	}
}
func TestCustomProviderCLIAndNoToolsRefusal(t *testing.T) {
	paths, _ := config.NewPaths(t.TempDir())
	data := `{"providers":{"local":{"api":"openai-compatible","name":"Local","base_url":"http://localhost:11434/v1","auth":{"type":"none"},"models":[{"id":"qwen","tool_calling":true},{"id":"no-tools","tool_calling":false}]}}}`
	if err := os.WriteFile(paths.Models, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := app.Load(paths, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cio := commandIO{output: &out}
	if err := runCommand(context.Background(), []string{"model", "set", "local/qwen"}, r, cio); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"local/no-tools", "openai/gpt-5.2"} {
		if err := runCommand(context.Background(), []string{"model", "set", ref}, r, cio); err == nil {
			t.Fatal("unavailable model saved", ref)
		}
	}
	c, _ := config.Load(filepath.Join(paths.Home, "config.json"))
	if c.DefaultModel != "local/qwen" {
		t.Fatal(c)
	}
}

func TestOAuthIdentityOnlyLoginAndLogout(t *testing.T) {
	paths, _ := config.NewPaths(t.TempDir())
	r, err := app.Load(paths, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := r.Hosts.OpenAIHostID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := auth.OAuthCredential{ClientID: "oaiapp_test", ExtAgentHostID: hostID, Issuer: openai.OAuthIssuer, Subject: "subject-1", IDToken: "private-id", AccessToken: "private-access", RefreshToken: "private-refresh", ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"openid"}}
	r.DiscoverOpenAI = func(context.Context, auth.OAuthCredential) ([]provider.ModelInfo, error) {
		t.Error("missing plan scope made discovery request")
		return nil, nil
	}
	var out bytes.Buffer
	cio := commandIO{output: &out, oauthLogin: func(context.Context, auth.LoginOptions) (auth.OAuthCredential, error) { return c, nil }}
	if err := runCommand(context.Background(), []string{"auth", "login", "openai", "--method", "oauth"}, r, cio); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "account connected") || !strings.Contains(out.String(), "disabled") || strings.Contains(out.String(), "private-") || strings.Contains(out.String(), hostID) {
		t.Fatal("unsafe/missing identity-only UX")
	}
	stored, err := r.Auth.StoredCredential(context.Background(), "openai")
	if err != nil || stored.(auth.OAuthCredential).Subject != c.Subject {
		t.Fatal("identity-only credential not saved", err)
	}
	for _, model := range r.Models.List() {
		if model.ProviderID == "openai" {
			t.Fatal("API-key catalog retained for OAuth")
		}
	}
	if _, _, err := r.Manager.Create(context.Background(), provider.ModelRef{ProviderID: "openai", ModelID: "gpt-5.2"}); err != openai.ErrPlanUsageDisabled {
		t.Fatal("ungranted inference accepted", err)
	}
	if err := runCommand(context.Background(), []string{"auth", "logout", "openai"}, r, cio); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Auth.StoredCredential(context.Background(), "openai"); err != auth.ErrNotFound {
		t.Fatal("logout retained credential")
	}
	after, err := r.Hosts.OpenAIHostID(context.Background())
	if err != nil || after != hostID {
		t.Fatal("logout changed host", err)
	}
}

func TestOAuthCheckConfigIsOfflineAndNoClientIDRequired(t *testing.T) {
	paths, _ := config.NewPaths(t.TempDir())
	t.Setenv("CLAW_HOME", paths.Home)
	t.Setenv("OPENAI_OAUTH_CLIENT_ID", "")
	t.Setenv("OPENAI_API_KEY", "")
	r, err := app.Load(paths, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := r.Hosts.OpenAIHostID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := auth.OAuthCredential{ClientID: "oaiapp_test", ExtAgentHostID: hostID, Issuer: openai.OAuthIssuer, Subject: "subject-1", IDToken: "private-id", AccessToken: "private-access", RefreshToken: "private-refresh", ExpiresAt: time.Now().Add(time.Hour), Scopes: strings.Fields(openai.OAuthScopes)}
	if err := r.Auth.Login(context.Background(), "openai", c); err != nil {
		t.Fatal(err)
	}
	if err := openai.SaveCatalog(paths.OpenAIModels, c, []provider.ModelInfo{{ProviderID: "openai", ID: "model-a", ToolCalling: true, BillingMode: provider.BillingSubscription}}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-mode=demo", "-check-config", "-model=openai/model-a", "-workdir=" + t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dynamic registration supported") || !strings.Contains(out.String(), "Host ID: available") || !strings.Contains(out.String(), "ChatGPT Plan Usage: enabled") || strings.Contains(out.String(), "private-") || strings.Contains(out.String(), hostID) {
		t.Fatal("unsafe/incomplete OAuth config status")
	}
}

func TestOAuthDiscoveryFailureDoesNotHideOtherProvidersOrReuseOldGrant(t *testing.T) {
	ctx := context.Background()
	paths, _ := config.NewPaths(t.TempDir())
	r, err := app.Load(paths, func(key string) string {
		if key == "ZAI_CODING_CN_API_KEY" {
			return "zai-key"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := r.Hosts.OpenAIHostID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := auth.OAuthCredential{ClientID: "oaiapp_test", ExtAgentHostID: hostID, Issuer: openai.OAuthIssuer, Subject: "subject-1", IDToken: "private-id", AccessToken: "private-access", RefreshToken: "private-refresh", ExpiresAt: time.Now().Add(time.Hour), Scopes: strings.Fields(openai.OAuthScopes)}
	if err := r.Auth.Login(ctx, "openai", c); err != nil {
		t.Fatal(err)
	}
	if err := openai.SaveCatalog(paths.OpenAIModels, c, []provider.ModelInfo{{ProviderID: "openai", ID: "old-grant-model", ToolCalling: true, BillingMode: provider.BillingSubscription}}); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadOpenAICatalog(ctx); err != nil {
		t.Fatal(err)
	}
	r.DiscoverOpenAI = func(context.Context, auth.OAuthCredential) ([]provider.ModelInfo, error) {
		return nil, errors.New("mock discovery unavailable")
	}
	var out bytes.Buffer
	cio := commandIO{output: &out, oauthLogin: func(context.Context, auth.LoginOptions) (auth.OAuthCredential, error) { return c, nil }}
	if err := runCommand(ctx, []string{"auth", "login", "openai"}, r, cio); err == nil {
		t.Fatal("discovery failure was not reported after login")
	}
	if _, err := os.Stat(paths.OpenAIModels); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new authorization retained previous grant's cache")
	}
	out.Reset()
	if err := runCommand(ctx, []string{"models"}, r, cio); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "models unavailable") || !strings.Contains(out.String(), "zai-coding-cn") || strings.Contains(out.String(), "old-grant-model") || strings.Contains(out.String(), "gpt-5.2") {
		t.Fatal("OpenAI outage affected other providers or reused old models")
	}
}
