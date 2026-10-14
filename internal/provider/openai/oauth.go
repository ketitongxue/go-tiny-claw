package openai

import (
	"context"
	"errors"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
)

const (
	OAuthIssuer      = "https://auth.openai.com"
	AuthorizationURL = OAuthIssuer + "/api/accounts/authorize"
	TokenURL         = OAuthIssuer + "/api/accounts/oauth/token"
	JWKSURL          = OAuthIssuer + "/.well-known/jwks.json"
	OAuthResource    = "https://api.openai.com/v1"
	PlanScope        = "chatgpt.tokens.use.direct"
	OAuthScopes      = "openid profile email offline_access resource.invoke " + PlanScope
)

var ErrPlanUsageDisabled = errors.New("OpenAI account connected, but ChatGPT plan usage was not granted. Re-authorize ChatGPT plan usage or configure an OpenAI API key")

// OAuthClient defaults to dynamic OSS registration. The environment client ID
// is only an optional static-client override; saved registrations always win.
func OAuthClient(getenv func(string) string, hostID string, previous *auth.OAuthCredential) (*auth.OAuthClient, error) {
	return NewOAuthClient(getenv, hostID, previous, auth.NewJWKSVerifier(OAuthIssuer, JWKSURL, nil))
}

// NewOAuthClient exposes the verifier boundary for mock JWKS/transport tests.
func NewOAuthClient(getenv func(string) string, hostID string, previous *auth.OAuthCredential, verifier auth.IDTokenVerifier) (*auth.OAuthClient, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	clientID := strings.TrimSpace(getenv("OPENAI_OAUTH_CLIENT_ID"))
	if clientID == "" {
		clientID = auth.DynamicClientID
	}
	if previous != nil {
		if err := CheckCredential(*previous); err != nil {
			return nil, err
		}
		clientID = previous.ClientID
	}
	redirect := getenv("OPENAI_OAUTH_REDIRECT_URI")
	if redirect == "" {
		redirect = "http://127.0.0.1:0/auth/callback"
	}
	return auth.NewOAuthClient(auth.OAuthConfig{
		ClientID: clientID, AuthorizationURL: AuthorizationURL, TokenURL: TokenURL,
		RedirectURL: redirect, Scope: OAuthScopes, Resource: OAuthResource,
		AgentName: "go-tiny-claw", HostID: hostID, Previous: previous, IDTokenVerifier: verifier,
	})
}

// CheckCredential is local-only. Legacy OAuth records remain readable, but must
// be replaced with a verified registration before they can authorize inference.
func CheckCredential(c auth.Credential) error {
	if oauth, ok := c.(auth.OAuthCredential); ok {
		if err := auth.Validate(oauth); err != nil {
			return err
		}
		if oauth.ClientID == "" || oauth.ClientID == auth.DynamicClientID || oauth.Issuer != OAuthIssuer || oauth.Subject == "" || oauth.IDToken == "" || !auth.ValidHostID(oauth.ExtAgentHostID) {
			return errors.New("OpenAI OAuth registration/verified identity incomplete; run claw auth logout openai, then claw auth login openai")
		}
	}
	return nil
}
func CanUse(c auth.Credential) error {
	if oauth, ok := c.(auth.OAuthCredential); ok && !oauth.HasScope(PlanScope) {
		return ErrPlanUsageDisabled
	}
	return nil
}

// Refresh uses persisted registration metadata, not environment overrides. The
// runtime passes a shared verifier to reuse its JWKS cache across refreshes.
func Refresh(ctx context.Context, c auth.OAuthCredential, verifier auth.IDTokenVerifier) (auth.OAuthCredential, error) {
	client, err := NewOAuthClient(nil, c.ExtAgentHostID, &c, verifier)
	if err != nil {
		return auth.OAuthCredential{}, err
	}
	return client.Refresh(ctx, c)
}
