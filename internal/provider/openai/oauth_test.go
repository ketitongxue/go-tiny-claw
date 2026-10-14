package openai

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
)

func testOAuthCredential() auth.OAuthCredential {
	return auth.OAuthCredential{AccessToken: "test-secret", RefreshToken: "test-refresh", IDToken: "test-id-token", ClientID: "oaiapp_test", ExtAgentHostID: "urn:uuid:123e4567-e89b-42d3-a456-426614174000", Issuer: OAuthIssuer, Subject: "subject-1", Email: "test@example.com", Scopes: strings.Fields(OAuthScopes), TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour)}
}
func TestOAuthDefaultsAndStaticOverride(t *testing.T) {
	c := testOAuthCredential()
	for _, mode := range []string{"dynamic", "static", "returning"} {
		t.Run(mode, func(t *testing.T) {
			var previous *auth.OAuthCredential
			want := auth.DynamicClientID
			getenv := func(string) string { return "" }
			if mode != "dynamic" {
				getenv = func(key string) string {
					if key == "OPENAI_OAUTH_CLIENT_ID" {
						return "static-client"
					}
					return ""
				}
				want = "static-client"
			}
			if mode == "returning" {
				previous, want = &c, c.ClientID
			}
			client, err := OAuthClient(getenv, c.ExtAgentHostID, previous)
			if err != nil {
				t.Fatal(err)
			}
			s, err := client.Begin()
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(s.AuthorizationURL)
			q := u.Query()
			if u.Scheme+"://"+u.Host+u.Path != AuthorizationURL || q.Get("client_id") != want || q.Get("scope") != OAuthScopes || q.Get("resource") != OAuthResource || q.Get("agent_name_hint") != "go-tiny-claw" || q.Get("redirect_uri") != "http://127.0.0.1:0/auth/callback" || q.Get("nonce") == "" {
				t.Fatal("incorrect OpenAI OAuth contract")
			}
			if previous != nil && q.Get("id_token_hint") != c.IDToken {
				t.Fatal("missing returning hint")
			}
		})
	}
	if _, err := OAuthClient(nil, "bad-host", nil); err == nil {
		t.Fatal("dynamic host validation skipped")
	}
	if _, err := OAuthClient(nil, "urn:uuid:00000000-0000-4000-8000-000000000000", &c); err == nil {
		t.Fatal("returning host changed")
	}
}
func TestMissingPlanScopeDisablesOAuthInferenceButNotIdentity(t *testing.T) {
	c := testOAuthCredential()
	c.Scopes = []string{"openid", "profile"}
	if err := CheckCredential(c); err != nil {
		t.Fatal("valid identity rejected", err)
	}
	if err := CanUse(c); err != ErrPlanUsageDisabled {
		t.Fatal("missing plan permission accepted", err)
	}
	if _, err := New(Config{}, Models()[0], c); err != ErrPlanUsageDisabled {
		t.Fatal("inference was not blocked", err)
	}
	if APIEndpoint != "https://api.openai.com/v1/responses" || SubscriptionEndpoint != APIEndpoint {
		t.Fatal("OAuth uses wrong inference endpoint")
	}
}
