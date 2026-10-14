package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
)

type AuthMethod = auth.CredentialType

type ProviderConfig struct{ BaseURL string }

type ProviderFactory func(context.Context, ModelInfo, auth.Credential, ProviderConfig) (LLMProvider, error)

type ProviderDefinition struct {
	ID, Name      string
	AuthMethods   []AuthMethod
	EnvVars       []string
	Models        []ModelInfo
	Config        ProviderConfig
	BillingByAuth map[AuthMethod]BillingMode
	Factory       ProviderFactory
}

// Registry is assembled once at startup, then shared read-only across sessions.
type Registry struct{ providers map[string]ProviderDefinition }

func NewRegistry() *Registry { return &Registry{providers: make(map[string]ProviderDefinition)} }

func (r *Registry) Register(p ProviderDefinition) error {
	if err := ValidateProviderID(p.ID); err != nil {
		return err
	}
	if _, exists := r.providers[p.ID]; exists {
		return fmt.Errorf("duplicate provider %s (built-ins cannot be overridden)", p.ID)
	}
	if p.Factory == nil {
		return fmt.Errorf("provider %s has no factory", p.ID)
	}
	if len(p.AuthMethods) == 0 {
		return fmt.Errorf("provider %s has no auth methods", p.ID)
	}
	seen := map[AuthMethod]bool{}
	for _, method := range p.AuthMethods {
		if method != auth.CredentialAPIKey && method != auth.CredentialOAuth && method != auth.CredentialNone || seen[method] {
			return fmt.Errorf("invalid auth methods for provider %s", p.ID)
		}
		seen[method] = true
	}
	if seen[auth.CredentialNone] && len(seen) != 1 {
		return fmt.Errorf("provider %s cannot mix none with authenticated methods", p.ID)
	}
	for method, mode := range p.BillingByAuth {
		if !seen[method] || mode != BillingAPI && mode != BillingSubscription && mode != BillingUnknown {
			return fmt.Errorf("invalid auth billing mode for provider %s", p.ID)
		}
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	models := NewModelRegistry()
	for _, model := range p.Models {
		if model.ProviderID != p.ID {
			return fmt.Errorf("model %s belongs to a different provider", model.ID)
		}
		if err := models.Register(model); err != nil {
			return err
		}
	}
	r.providers[p.ID] = cloneDefinition(p)
	return nil
}

func cloneModel(m ModelInfo) ModelInfo {
	if m.Pricing != nil {
		price := *m.Pricing
		m.Pricing = &price
	}
	return m
}
func cloneDefinition(p ProviderDefinition) ProviderDefinition {
	if p.BillingByAuth != nil {
		billing := make(map[AuthMethod]BillingMode, len(p.BillingByAuth))
		for method, mode := range p.BillingByAuth {
			billing[method] = mode
		}
		p.BillingByAuth = billing
	}
	p.AuthMethods = append([]AuthMethod(nil), p.AuthMethods...)
	p.EnvVars = append([]string(nil), p.EnvVars...)
	p.Models = append([]ModelInfo(nil), p.Models...)
	for i := range p.Models {
		p.Models[i] = cloneModel(p.Models[i])
	}
	return p
}
func (r *Registry) Get(id string) (ProviderDefinition, bool) {
	p, ok := r.providers[id]
	return cloneDefinition(p), ok
}
func (r *Registry) List() []ProviderDefinition {
	result := make([]ProviderDefinition, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, cloneDefinition(p))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
