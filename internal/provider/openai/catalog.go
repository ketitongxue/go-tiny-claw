package openai

import (
	"errors"
	"os"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

type cachedCatalog struct {
	AuthType  auth.CredentialType  `json:"auth_type"`
	ClientID  string               `json:"client_id"`
	HostID    string               `json:"ext_agent_host_id"`
	Issuer    string               `json:"issuer"`
	Subject   string               `json:"subject"`
	UpdatedAt time.Time            `json:"updated_at"`
	Models    []provider.ModelInfo `json:"models"`
}

// Catalog cache contains no tokens or hints and is bound to verified identity,
// registration, installation and auth type. It cannot authorize inference itself.
func LoadCatalog(path string, c auth.OAuthCredential) ([]provider.ModelInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("OpenAI model cache must be a regular file")
	}
	var cache cachedCatalog
	if err := fileutil.ReadJSON(path, &cache); err != nil {
		return nil, err
	}
	if cache.AuthType != auth.CredentialOAuth || cache.ClientID != c.ClientID || cache.HostID != c.ExtAgentHostID || cache.Issuer != c.Issuer || cache.Subject != c.Subject || !c.HasScope(PlanScope) {
		return nil, nil
	}
	registry := provider.NewModelRegistry()
	for _, model := range cache.Models {
		model.ProviderID = "openai"
		model.BillingMode = provider.BillingSubscription
		model.Pricing = nil
		if err := registry.Register(model); err != nil {
			return nil, errors.New("invalid OpenAI OAuth model cache; run claw models")
		}
	}
	return registry.List(), nil
}
func SaveCatalog(path string, c auth.OAuthCredential, models []provider.ModelInfo) error {
	if err := CheckCredential(c); err != nil {
		return err
	}
	if err := CanUse(c); err != nil {
		return err
	}
	registry := provider.NewModelRegistry()
	for _, model := range models {
		if model.ProviderID != "openai" || model.BillingMode != provider.BillingSubscription {
			return errors.New("not an OpenAI OAuth catalog")
		}
		if err := registry.Register(model); err != nil {
			return err
		}
	}
	return fileutil.WriteJSON(path, cachedCatalog{AuthType: auth.CredentialOAuth, ClientID: c.ClientID, HostID: c.ExtAgentHostID, Issuer: c.Issuer, Subject: c.Subject, UpdatedAt: time.Now().UTC(), Models: registry.List()})
}
