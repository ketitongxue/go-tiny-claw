package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"

	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

type CustomAuth struct {
	Type string `json:"type"`
	Env  string `json:"env,omitempty"`
}
type CustomProvider struct {
	Name    string               `json:"name"`
	API     string               `json:"api"`
	BaseURL string               `json:"base_url"`
	Auth    CustomAuth           `json:"auth"`
	Models  []provider.ModelInfo `json:"models"`
}
type Models struct {
	Providers map[string]CustomProvider `json:"providers"`
}

var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func LoadModels(path string) (Models, error) {
	var models Models
	err := fileutil.ReadJSON(path, &models)
	if errors.Is(err, os.ErrNotExist) {
		return models, nil
	}
	if err != nil {
		return models, err
	}
	for id, p := range models.Providers {
		if err := provider.ValidateProviderID(id); err != nil {
			return models, err
		}
		if id == "openai" || id == "zai-coding-cn" {
			return models, fmt.Errorf("custom provider %s conflicts with a built-in", id)
		}
		if p.API != "openai-compatible" {
			return models, fmt.Errorf("provider %s: only openai-compatible is supported", id)
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return models, fmt.Errorf("provider %s: invalid HTTP(S) base_url (credentials/query/fragment forbidden)", id)
		}
		if p.Auth.Type != "none" && p.Auth.Type != "api_key" {
			return models, fmt.Errorf("provider %s: auth must be none or api_key", id)
		}
		if p.Auth.Type == "api_key" && !envPattern.MatchString(p.Auth.Env) {
			return models, fmt.Errorf("provider %s: invalid API key env name", id)
		}
		if p.Auth.Type == "none" && p.Auth.Env != "" {
			return models, fmt.Errorf("provider %s: auth none cannot specify env", id)
		}
		if len(p.Models) == 0 {
			return models, fmt.Errorf("provider %s has no models", id)
		}
		registry := provider.NewModelRegistry()
		for i := range p.Models {
			p.Models[i].ProviderID = id
			if p.Models[i].BillingMode == "" {
				p.Models[i].BillingMode = provider.BillingUnknown
			}
			if err := registry.Register(p.Models[i]); err != nil {
				return models, err
			}
		}
		models.Providers[id] = p
	}
	return models, nil
}
