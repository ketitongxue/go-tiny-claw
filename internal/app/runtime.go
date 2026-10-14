// Package app is the composition root. Neither engine nor executors depend on it.
package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"sync"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/compatible"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/openai"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/zai"
)

type Runtime struct {
	Paths     config.Paths
	Config    config.Config
	Providers *provider.Registry
	Models    *provider.ModelRegistry
	Auth      *auth.Manager
	Manager   *provider.Manager
	Hosts     *auth.HostStore
	getenv    func(string) string
	catalogMu sync.Mutex
	// Injectable discovery boundary; default is the real OAuth HTTP catalog.
	DiscoverOpenAI     func(context.Context, auth.OAuthCredential) ([]provider.ModelInfo, error)
	OpenAICatalogError error
}

func Load(paths config.Paths, getenv func(string) string) (*Runtime, error) {
	c, err := config.Load(paths.Config)
	if err != nil {
		return nil, err
	}
	custom, err := config.LoadModels(paths.Models)
	if err != nil {
		return nil, err
	}
	r := &Runtime{Paths: paths, Config: c, Providers: provider.NewRegistry(), Models: provider.NewModelRegistry(), Auth: auth.NewManager(auth.NewFileStore(paths.Auth), getenv), Hosts: auth.NewHostStore(paths.Host), getenv: getenv}
	r.DiscoverOpenAI = func(ctx context.Context, c auth.OAuthCredential) ([]provider.ModelInfo, error) {
		return openai.DiscoverModels(ctx, c, "", nil)
	}
	definitions := []provider.ProviderDefinition{openai.Definition(), zai.Definition()}
	ids := make([]string, 0, len(custom.Providers))
	for id := range custom.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := custom.Providers[id]
		method := auth.CredentialType(p.Auth.Type)
		var env []string
		if p.Auth.Env != "" {
			env = []string{p.Auth.Env}
		}
		definitions = append(definitions, compatible.Definition(compatible.Config{ID: id, Name: p.Name, BaseURL: p.BaseURL}, []provider.AuthMethod{method}, env, p.Models))
	}
	for _, definition := range definitions {
		if err := r.Providers.Register(definition); err != nil {
			return nil, err
		}
		for _, model := range definition.Models {
			if err := r.Models.Register(model); err != nil {
				return nil, err
			}
		}
		policy := auth.Policy{Methods: definition.AuthMethods, EnvVars: definition.EnvVars}
		if definition.ID == "openai" {
			policy.Check = openai.CheckCredential
			policy.CanUse = openai.CanUse
			verifier := auth.NewJWKSVerifier(openai.OAuthIssuer, openai.JWKSURL, &http.Client{})
			policy.Refresh = func(ctx context.Context, c auth.OAuthCredential) (auth.OAuthCredential, error) {
				return openai.Refresh(ctx, c, verifier)
			}
		}
		if err := r.Auth.Register(definition.ID, policy); err != nil {
			return nil, err
		}
	}
	if err := r.LoadOpenAICatalog(context.Background()); err != nil {
		return nil, err
	}
	r.Manager, err = provider.NewManager(r.Providers, r.Models, r.Auth)
	return r, err
}

func (r *Runtime) OpenAILoginClient(ctx context.Context) (*auth.OAuthClient, error) {
	hostID, err := r.Hosts.OpenAIHostID(ctx)
	if err != nil {
		return nil, err
	}
	stored, err := r.Auth.StoredCredential(ctx, "openai")
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return nil, err
	}
	var previous *auth.OAuthCredential
	if c, ok := stored.(auth.OAuthCredential); ok {
		previous = &c
	}
	return openai.OAuthClient(r.getenv, hostID, previous)
}

// LoadOpenAICatalog is offline, including when called by check-config. API-key
// and OAuth catalogs never mix; legacy/unauthorized OAuth has an empty catalog.
func (r *Runtime) LoadOpenAICatalog(ctx context.Context) error {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	return r.loadOpenAICatalog(ctx)
}
func (r *Runtime) loadOpenAICatalog(ctx context.Context) error {
	c, err := r.Auth.StoredCredential(ctx, "openai")
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return err
	}
	models := openai.Models()
	r.OpenAICatalogError = nil
	if oauth, ok := c.(auth.OAuthCredential); ok {
		models = nil
		if openai.CheckCredential(oauth) == nil && openai.CanUse(oauth) == nil {
			models, r.OpenAICatalogError = openai.LoadCatalog(r.Paths.OpenAIModels, oauth)
			// A damaged cache must not prevent logout or claw models from repairing it.
		}
	}
	return r.Models.ReplaceProvider("openai", models)
}

func (r *Runtime) RefreshOpenAICatalog(ctx context.Context) error {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	stored, err := r.Auth.StoredCredential(ctx, "openai")
	if errors.Is(err, auth.ErrNotFound) {
		return r.Models.ReplaceProvider("openai", openai.Models())
	}
	if err != nil {
		_ = r.Models.ReplaceProvider("openai", nil)
		return err
	}
	if stored.Type() != auth.CredentialOAuth {
		return r.Models.ReplaceProvider("openai", openai.Models())
	}
	// Never retain an API-key/stale entitlement catalog after an OAuth change.
	if err := r.Models.ReplaceProvider("openai", nil); err != nil {
		return err
	}
	credential, err := r.Auth.GetCredential(ctx, "openai")
	if err != nil {
		return err
	}
	c, ok := credential.(auth.OAuthCredential)
	if !ok {
		return errors.New("OpenAI authentication changed during model discovery; retry")
	}
	models, err := r.DiscoverOpenAI(ctx, c)
	if err != nil {
		return err
	}
	current, err := r.Auth.StoredCredential(ctx, "openai")
	if err != nil {
		return errors.New("OpenAI credentials changed during model discovery; retry")
	}
	latest, ok := current.(auth.OAuthCredential)
	if !ok || latest.ClientID != c.ClientID || latest.Issuer != c.Issuer || latest.Subject != c.Subject || latest.ExtAgentHostID != c.ExtAgentHostID || !latest.HasScope(openai.PlanScope) {
		return errors.New("OpenAI authorization changed during model discovery; retry")
	}
	if err := openai.SaveCatalog(r.Paths.OpenAIModels, latest, models); err != nil {
		return err
	}
	if err := r.Models.ReplaceProvider("openai", models); err != nil {
		return err
	}
	r.OpenAICatalogError = nil
	return nil
}

func (r *Runtime) ClearOpenAICatalog(ctx context.Context) error {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	if err := r.Models.ReplaceProvider("openai", nil); err != nil {
		return err
	}
	if err := os.Remove(r.Paths.OpenAIModels); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return r.loadOpenAICatalog(ctx)
}
