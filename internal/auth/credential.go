package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type CredentialType string

const (
	CredentialNone   CredentialType = "none"
	CredentialAPIKey CredentialType = "api_key"
	CredentialOAuth  CredentialType = "oauth"
)

type Credential interface{ Type() CredentialType }

type NoCredential struct{}

func (NoCredential) Type() CredentialType { return CredentialNone }

type APIKeyCredential struct{ Key string }

func (APIKeyCredential) Type() CredentialType { return CredentialAPIKey }
func (APIKeyCredential) String() string       { return "api_key:[REDACTED]" }
func (c APIKeyCredential) GoString() string   { return c.String() }

// DynamicClientID is a registration entry point, never a stored client ID.
const DynamicClientID = "dynamic_agent_client"

type OAuthCredential struct {
	AccessToken, RefreshToken, IDToken string
	ExpiresAt, EarliestRefreshAt       time.Time
	ExpiresIn                          int64
	ClientID, ExtAgentHostID           string
	Issuer, Subject, Email             string
	Scopes                             []string
	TokenType                          string
	AccountID, Scope                   string // legacy records; not used as authenticated identity
}

func (c OAuthCredential) HasScope(scope string) bool {
	for _, granted := range c.GrantedScopes() {
		if granted == scope {
			return true
		}
	}
	return false
}
func (c OAuthCredential) GrantedScopes() []string {
	if c.Scopes != nil {
		return append([]string(nil), c.Scopes...)
	}
	return strings.Fields(c.Scope) // backwards-compatible reads
}
func (c OAuthCredential) RefreshAllowed(now time.Time) bool {
	return !c.EarliestRefreshAt.After(now)
}
func (c OAuthCredential) Expired(now time.Time) bool { return !c.ExpiresAt.After(now) }

func (OAuthCredential) Type() CredentialType { return CredentialOAuth }
func (OAuthCredential) String() string       { return "oauth:[REDACTED]" }
func (c OAuthCredential) GoString() string   { return c.String() }
func (c OAuthCredential) Expiring(now time.Time) bool {
	return !c.ExpiresAt.After(now.Add(time.Minute))
}

var ErrNotFound = errors.New("credential not found")

func Validate(c Credential) error {
	switch c := c.(type) {
	case NoCredential:
		return nil
	case APIKeyCredential:
		if strings.TrimSpace(c.Key) == "" || strings.ContainsAny(c.Key, "\r\n") {
			return errors.New("invalid API key")
		}
	case OAuthCredential:
		if c.AccessToken == "" || strings.ContainsAny(c.AccessToken+c.AccountID+c.RefreshToken+c.IDToken+c.ClientID+c.ExtAgentHostID+c.Issuer+c.Subject+c.Email, "\r\n") || c.ExpiresAt.IsZero() || c.ClientID == DynamicClientID {
			return errors.New("invalid OAuth credential")
		}
		if c.TokenType != "" && !strings.EqualFold(c.TokenType, "Bearer") {
			return errors.New("unsupported OAuth token type")
		}
	default:
		return fmt.Errorf("unsupported credential type")
	}
	return nil
}
