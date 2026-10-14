package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

type RefreshFunc func(context.Context, OAuthCredential) (OAuthCredential, error)
type Policy struct {
	Methods []CredentialType
	EnvVars []string
	Refresh RefreshFunc
	Check   func(Credential) error // provider-specific, local-only credential validation
	CanUse  func(Credential) error // entitlement check; login may save identity without it
}

type CredentialInfo struct {
	Type           CredentialType
	Source         string
	Available      bool
	ExpiresAt      time.Time
	NeedsRefresh   bool
	PlanUsage      bool
	RefreshBlocked bool
}

type Manager struct {
	store    Store
	getenv   func(string) string
	now      func() time.Time
	mu       sync.Mutex
	policies map[string]Policy
	locks    map[string]chan struct{}
}

func NewManager(store Store, getenv func(string) string) *Manager {
	return &Manager{store: store, getenv: getenv, now: time.Now, policies: map[string]Policy{}, locks: map[string]chan struct{}{}}
}
func (m *Manager) Register(id string, p Policy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !idPattern.MatchString(id) {
		return errors.New("invalid provider ID")
	}
	if _, ok := m.policies[id]; ok {
		return fmt.Errorf("duplicate auth policy for %s", id)
	}
	p.Methods = slices.Clone(p.Methods)
	p.EnvVars = slices.Clone(p.EnvVars)
	m.policies[id] = p
	m.locks[id] = make(chan struct{}, 1)
	return nil
}
func (m *Manager) policy(id string) (Policy, chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.policies[id]
	if !ok {
		return p, nil, fmt.Errorf("unknown provider %s", id)
	}
	return p, m.locks[id], nil
}
func acquire(ctx context.Context, lock chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) resolve(ctx context.Context, id string, p Policy) (Credential, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if slices.Contains(p.Methods, CredentialNone) {
		return NoCredential{}, "none", nil
	}
	c, err := m.store.Load(ctx, id)
	if err == nil {
		if !slices.Contains(p.Methods, c.Type()) {
			return nil, "store", fmt.Errorf("provider %s does not accept stored auth type %s", id, c.Type())
		}
		if err := Validate(c); err != nil {
			return nil, "store", err
		}
		if p.Check != nil {
			if err := p.Check(c); err != nil {
				return nil, "store", err
			}
		}
		return c, "store", nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, "store", err
	}
	if slices.Contains(p.Methods, CredentialAPIKey) && m.getenv != nil {
		for _, env := range p.EnvVars {
			if key := strings.TrimSpace(m.getenv(env)); key != "" {
				c := APIKeyCredential{Key: key}
				return c, "env:" + env, Validate(c)
			}
		}
	}
	return nil, "", fmt.Errorf("Provider %s 缺少凭据。请运行 claw auth login %s 或设置 %s", id, id, strings.Join(p.EnvVars, " / "))
}

// Inspect is offline: it never refreshes or validates credentials over HTTP.
func (m *Manager) Inspect(ctx context.Context, id string) (CredentialInfo, error) {
	p, _, err := m.policy(id)
	if err != nil {
		return CredentialInfo{}, err
	}
	c, source, err := m.resolve(ctx, id, p)
	if err != nil {
		return CredentialInfo{Source: source}, err
	}
	info := CredentialInfo{Type: c.Type(), Source: source, Available: true}
	if oauth, ok := c.(OAuthCredential); ok {
		info.ExpiresAt = oauth.ExpiresAt
		info.PlanUsage = oauth.HasScope("chatgpt.tokens.use.direct")
		info.NeedsRefresh = oauth.Expiring(m.now())
		info.RefreshBlocked = info.NeedsRefresh && !oauth.RefreshAllowed(m.now())
		if info.RefreshBlocked && oauth.Expired(m.now()) {
			info.Available = false
			return info, errors.New("OAuth token expired; refresh deferred until earliest_refresh_at; sign in again")
		}
		if info.NeedsRefresh && !info.RefreshBlocked && (oauth.RefreshToken == "" || p.Refresh == nil) {
			info.Available = false
			return info, fmt.Errorf("Provider %s OAuth token expired/expiring; run claw auth login %s", id, id)
		}
	}
	if p.CanUse != nil {
		if err := p.CanUse(c); err != nil {
			info.Available = false
			return info, err
		}
	}
	return info, nil
}

// StoredCredential is offline and bypasses entitlement checks so returning login
// can re-authorize a connected account whose plan scope was not granted.
func (m *Manager) StoredCredential(ctx context.Context, id string) (Credential, error) {
	_, lock, err := m.policy(id)
	if err != nil {
		return nil, err
	}
	if err := acquire(ctx, lock); err != nil {
		return nil, err
	}
	defer func() { <-lock }()
	return m.store.Load(ctx, id)
}
func (m *Manager) GetCredential(ctx context.Context, id string) (Credential, error) {
	p, lock, err := m.policy(id)
	if err != nil {
		return nil, err
	}
	if err := acquire(ctx, lock); err != nil {
		return nil, err
	}
	defer func() { <-lock }()
	c, _, err := m.resolve(ctx, id, p)
	if err != nil {
		return nil, err
	}
	if p.CanUse != nil {
		if err := p.CanUse(c); err != nil {
			return nil, err
		}
	}
	if oauth, ok := c.(OAuthCredential); ok && oauth.Expiring(m.now()) {
		if !oauth.RefreshAllowed(m.now()) {
			if oauth.Expired(m.now()) {
				return nil, errors.New("OAuth token expired; refresh deferred until earliest_refresh_at; sign in again")
			}
			return c, nil
		}
		if oauth.RefreshToken == "" || p.Refresh == nil {
			return nil, fmt.Errorf("Provider %s OAuth refresh unavailable; run claw auth login %s", id, id)
		}
		updated, err := p.Refresh(ctx, oauth)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("Provider %s OAuth refresh failed; run claw auth login %s", id, id)
		}
		if !sameRegistration(oauth, updated) {
			return nil, errors.New("OAuth refresh changed the account/registration; sign in again")
		}
		if err := Validate(updated); err != nil || updated.Expired(m.now()) {
			return nil, errors.New("OAuth refresh returned invalid/expired credentials")
		}
		if p.Check != nil {
			if err := p.Check(updated); err != nil {
				return nil, err
			}
		}
		if err := m.store.Save(ctx, id, updated); err != nil {
			return nil, err
		}
		// Persist rotation even when the new grant removed plan permission.
		if p.CanUse != nil {
			if err := p.CanUse(updated); err != nil {
				return nil, err
			}
		}
		return updated, nil
	}
	return c, nil
}
func (m *Manager) Login(ctx context.Context, id string, c Credential) error {
	p, lock, err := m.policy(id)
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("missing login credential")
	}
	if !slices.Contains(p.Methods, c.Type()) || c.Type() == CredentialNone {
		return fmt.Errorf("provider %s does not accept this login method", id)
	}
	if err := acquire(ctx, lock); err != nil {
		return err
	}
	defer func() { <-lock }()
	if err := Validate(c); err != nil {
		return err
	}
	if p.Check != nil {
		if err := p.Check(c); err != nil {
			return err
		}
	}
	if next, ok := c.(OAuthCredential); ok {
		previous, err := m.store.Load(ctx, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if saved, ok := previous.(OAuthCredential); ok && (!sameRegistration(saved, next)) {
			return errors.New("OAuth registration/identity changed; logout explicitly before switching accounts")
		}
	}
	return m.store.Save(ctx, id, c)
}
func sameRegistration(saved, next OAuthCredential) bool {
	return (saved.ClientID == "" || saved.ClientID == next.ClientID) && (saved.Subject == "" || saved.Subject == next.Subject && saved.Issuer == next.Issuer) && (saved.ExtAgentHostID == "" || saved.ExtAgentHostID == next.ExtAgentHostID)
}
func (m *Manager) Logout(ctx context.Context, id string) error {
	_, lock, err := m.policy(id)
	if err != nil {
		return err
	}
	if err := acquire(ctx, lock); err != nil {
		return err
	}
	defer func() { <-lock }()
	return m.store.Delete(ctx, id)
}
func (m *Manager) LoginFromEnv(ctx context.Context, id string) error {
	p, _, err := m.policy(id)
	if err != nil {
		return err
	}
	if m.getenv != nil {
		for _, env := range p.EnvVars {
			if key := strings.TrimSpace(m.getenv(env)); key != "" {
				return m.Login(ctx, id, APIKeyCredential{Key: key})
			}
		}
	}
	return fmt.Errorf("Provider %s 缺少 API key 环境变量: %s", id, strings.Join(p.EnvVars, ", "))
}
