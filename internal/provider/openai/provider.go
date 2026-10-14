// Package openai implements Responses, separately from compatible Chat Completions.
package openai

import (
	"context"
	"errors"
	"net/http"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/wire"
)

const APIEndpoint = "https://api.openai.com/v1/responses"

// API keys and ChatGPT plan tokens use the same public Responses endpoint,
// but separate request contracts. Subscription requests require SSE/store=false.
const SubscriptionEndpoint = APIEndpoint

type Config struct {
	APIEndpoint, SubscriptionEndpoint string
	HTTPClient                        *http.Client
}
type Provider struct {
	model, endpoint, token string
	subscription           bool
	client                 *http.Client
}

func Definition() provider.ProviderDefinition {
	return provider.ProviderDefinition{ID: "openai", Name: "OpenAI", AuthMethods: []provider.AuthMethod{auth.CredentialOAuth, auth.CredentialAPIKey}, EnvVars: []string{"OPENAI_API_KEY"}, Models: Models(), BillingByAuth: map[provider.AuthMethod]provider.BillingMode{auth.CredentialAPIKey: provider.BillingAPI, auth.CredentialOAuth: provider.BillingSubscription}, Factory: func(_ context.Context, m provider.ModelInfo, c auth.Credential, _ provider.ProviderConfig) (provider.LLMProvider, error) {
		return New(Config{}, m, c)
	}}
}
func New(config Config, m provider.ModelInfo, c auth.Credential) (*Provider, error) {
	if err := auth.Validate(c); err != nil {
		return nil, err
	}
	if config.APIEndpoint == "" {
		config.APIEndpoint = APIEndpoint
	}
	if config.SubscriptionEndpoint == "" {
		config.SubscriptionEndpoint = config.APIEndpoint
	}
	p := &Provider{model: m.ID, client: wire.Client(config.HTTPClient)}
	switch c := c.(type) {
	case auth.APIKeyCredential:
		p.endpoint = config.APIEndpoint
		p.token = c.Key
	case auth.OAuthCredential:
		if err := CheckCredential(c); err != nil {
			return nil, err
		}
		if err := CanUse(c); err != nil {
			return nil, err
		}
		p.endpoint = config.SubscriptionEndpoint
		p.token = c.AccessToken
		p.subscription = true
	default:
		return nil, errors.New("OpenAI requires API key or OAuth")
	}
	return p, nil
}
