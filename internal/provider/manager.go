package provider

import (
	"context"
	"fmt"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type Manager struct {
	providers *Registry
	models    *ModelRegistry
	auth      *auth.Manager
}

func NewManager(providers *Registry, models *ModelRegistry, authManager *auth.Manager) (*Manager, error) {
	if providers == nil || models == nil || authManager == nil {
		return nil, fmt.Errorf("provider manager requires registries and auth manager")
	}
	return &Manager{providers: providers, models: models, auth: authManager}, nil
}
func (m *Manager) lookup(ref ModelRef) (ProviderDefinition, ModelInfo, error) {
	if _, err := ParseModelRef(ref.String()); err != nil {
		return ProviderDefinition{}, ModelInfo{}, err
	}
	model, ok := m.models.Get(ref)
	if !ok {
		return ProviderDefinition{}, model, fmt.Errorf("未知模型: %s。运行 claw models 查看已注册模型", ref)
	}
	definition, ok := m.providers.Get(ref.ProviderID)
	if !ok {
		return definition, model, fmt.Errorf("未知 Provider: %s", ref.ProviderID)
	}
	if !model.ToolCalling {
		return definition, model, fmt.Errorf("model %s does not support tool calling", ref)
	}
	return definition, model, nil
}
func billingModel(model ModelInfo, d ProviderDefinition, method auth.CredentialType) ModelInfo {
	if mode, ok := d.BillingByAuth[method]; ok {
		model.BillingMode = mode
	}
	if model.BillingMode != BillingAPI {
		model.Pricing = nil
	}
	return model
}

// Inspect performs exactly the offline checks needed by model set/check-config.
func (m *Manager) Inspect(ctx context.Context, ref ModelRef) (ModelInfo, auth.CredentialInfo, error) {
	if _, err := ParseModelRef(ref.String()); err != nil {
		return ModelInfo{}, auth.CredentialInfo{}, err
	}
	info, err := m.auth.Inspect(ctx, ref.ProviderID)
	if err != nil {
		return ModelInfo{}, info, err
	}
	d, model, err := m.lookup(ref)
	if err != nil {
		return model, info, err
	}
	return billingModel(model, d, info.Type), info, nil
}
func (m *Manager) ListAvailable(ctx context.Context) []ModelInfo {
	var models []ModelInfo
	for _, model := range m.models.List() {
		if available, _, err := m.Inspect(ctx, model.Ref()); err == nil {
			models = append(models, available)
		}
	}
	return models
}
func (m *Manager) Create(ctx context.Context, ref ModelRef) (LLMProvider, ModelInfo, error) {
	if _, _, err := m.Inspect(ctx, ref); err != nil {
		return nil, ModelInfo{}, err
	}
	d, model, err := m.lookup(ref)
	if err != nil {
		return nil, model, err
	}
	credential, err := m.auth.GetCredential(ctx, ref.ProviderID)
	if err != nil {
		return nil, model, err
	}
	model = billingModel(model, d, credential.Type())
	// Construct once now so invalid factory configuration fails before Agent.Run.
	p, err := d.Factory(ctx, model, credential, d.Config)
	if err != nil {
		return nil, model, err
	}
	if p == nil {
		return nil, model, fmt.Errorf("provider %s factory returned nil", ref.ProviderID)
	}
	managed := &managedProvider{manager: m, definition: d, model: model, method: credential.Type()}
	if oauth, ok := credential.(auth.OAuthCredential); ok {
		managed.clientID, managed.issuer, managed.subject = oauth.ClientID, oauth.Issuer, oauth.Subject
	}
	return managed, model, nil
}

// Credentials are resolved before EVERY call, not just at process startup, so a
// long-running Feishu process can refresh without changing AgentEngine.
type managedProvider struct {
	manager                   *Manager
	definition                ProviderDefinition
	model                     ModelInfo
	method                    auth.CredentialType
	clientID, issuer, subject string
}

func (p *managedProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	credential, err := p.manager.auth.GetCredential(ctx, p.definition.ID)
	if err != nil {
		return nil, err
	}
	if credential.Type() != p.method {
		return nil, fmt.Errorf("provider authentication method changed; restart claw to update billing metadata")
	}
	if oauth, ok := credential.(auth.OAuthCredential); ok && (oauth.ClientID != p.clientID || oauth.Issuer != p.issuer || oauth.Subject != p.subject) {
		return nil, fmt.Errorf("provider OAuth account/registration changed; restart claw to discover its models")
	}
	next, err := p.definition.Factory(ctx, p.model, credential, p.definition.Config)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return nil, fmt.Errorf("provider factory returned nil")
	}
	return next.Generate(ctx, msgs, tools)
}
