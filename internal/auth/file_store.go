package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type storedCredential struct {
	Type              CredentialType `json:"type"`
	APIKey            string         `json:"api_key,omitempty"`
	AccessToken       string         `json:"access_token,omitempty"`
	RefreshToken      string         `json:"refresh_token,omitempty"`
	ExpiresAt         time.Time      `json:"expires_at,omitzero"`
	AccountID         string         `json:"account_id,omitempty"`
	Scope             string         `json:"scope,omitempty"`
	TokenType         string         `json:"token_type,omitempty"`
	ClientID          string         `json:"client_id,omitempty"`
	ExtAgentHostID    string         `json:"ext_agent_host_id,omitempty"`
	Issuer            string         `json:"issuer,omitempty"`
	Subject           string         `json:"subject,omitempty"`
	Email             string         `json:"email,omitempty"`
	IDToken           string         `json:"id_token,omitempty"`
	ExpiresIn         int64          `json:"expires_in,omitempty"`
	EarliestRefreshAt time.Time      `json:"earliest_refresh_at,omitzero"`
	Scopes            []string       `json:"scopes,omitempty"`
}

type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) read() (map[string]storedCredential, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]storedCredential), nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("auth store must be a regular file, not a symlink")
	}
	// Repair old permissions rather than exposing secrets with a broad mode.
	if info.Mode().Perm() != 0600 {
		if err := os.Chmod(s.path, 0600); err != nil {
			return nil, errors.New("cannot secure auth store permissions (0600 required)")
		}
	}
	var records map[string]storedCredential
	if err := fileutil.ReadJSON(s.path, &records); err != nil {
		return nil, err
	}
	if records == nil {
		return nil, errors.New("invalid auth store: expected an object")
	}
	for id, record := range records {
		if !idPattern.MatchString(id) {
			return nil, errors.New("invalid provider ID in auth store")
		}
		if _, err := record.credential(); err != nil {
			return nil, fmt.Errorf("invalid credential record for provider %s", id)
		}
	}
	return records, nil
}
func (r storedCredential) credential() (Credential, error) {
	var c Credential
	switch r.Type {
	case CredentialAPIKey:
		if r.AccessToken != "" || r.RefreshToken != "" || r.IDToken != "" {
			return nil, errors.New("mixed credential types")
		}
		c = APIKeyCredential{Key: r.APIKey}
	case CredentialOAuth:
		if r.APIKey != "" {
			return nil, errors.New("mixed credential types")
		}
		c = OAuthCredential{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, IDToken: r.IDToken, ExpiresAt: r.ExpiresAt, ExpiresIn: r.ExpiresIn, EarliestRefreshAt: r.EarliestRefreshAt, AccountID: r.AccountID, Scope: r.Scope, Scopes: r.Scopes, TokenType: r.TokenType, ClientID: r.ClientID, ExtAgentHostID: r.ExtAgentHostID, Issuer: r.Issuer, Subject: r.Subject, Email: r.Email}
	default:
		return nil, errors.New("invalid credential type")
	}
	return c, Validate(c)
}
func (s *FileStore) Load(ctx context.Context, id string) (Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := s.read()
	if err != nil {
		return nil, err
	}
	r, ok := records[id]
	if !ok {
		return nil, ErrNotFound
	}
	return r.credential()
}
func (s *FileStore) Save(ctx context.Context, id string, c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !idPattern.MatchString(id) {
		return errors.New("invalid provider ID")
	}
	if err := Validate(c); err != nil {
		return err
	}
	var r storedCredential
	switch c := c.(type) {
	case APIKeyCredential:
		r = storedCredential{Type: c.Type(), APIKey: c.Key}
	case OAuthCredential:
		r = storedCredential{Type: c.Type(), AccessToken: c.AccessToken, RefreshToken: c.RefreshToken, IDToken: c.IDToken, ExpiresAt: c.ExpiresAt, ExpiresIn: c.ExpiresIn, EarliestRefreshAt: c.EarliestRefreshAt, AccountID: c.AccountID, Scopes: c.GrantedScopes(), TokenType: c.TokenType, ClientID: c.ClientID, ExtAgentHostID: c.ExtAgentHostID, Issuer: c.Issuer, Subject: c.Subject, Email: c.Email}
	default:
		return errors.New("only API key and OAuth credentials can be stored")
	}
	records, err := s.read()
	if err != nil {
		return err
	}
	records[id] = r
	return fileutil.WriteJSON(s.path, records)
}
func (s *FileStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	records, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := records[id]; !ok {
		return nil
	}
	delete(records, id)
	return fileutil.WriteJSON(s.path, records)
}
