package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type OAuthConfig struct {
	ClientID, AuthorizationURL, TokenURL, RedirectURL, Scope string
	Resource, AgentName, HostID                              string
	HTTPClient                                               *http.Client
	IDTokenVerifier                                          IDTokenVerifier
	Previous                                                 *OAuthCredential
}
type OAuthClient struct {
	config OAuthConfig
	client *http.Client
}

func (OAuthClient) String() string     { return "oauth_client:[REDACTED]" }
func (c OAuthClient) GoString() string { return c.String() }
func (OAuthConfig) String() string     { return "oauth_config:[REDACTED]" }
func (c OAuthConfig) GoString() string { return c.String() }

type OAuthSession struct {
	Verifier, Challenge, State, Nonce, AuthorizationURL string
	RedirectURL, ClientID                               string
}

func (OAuthSession) String() string     { return "oauth_attempt:[REDACTED]" }
func (s OAuthSession) GoString() string { return s.String() }

type OAuthCallback struct{ Code, ClientID string }

func (OAuthCallback) String() string     { return "oauth_callback:[REDACTED]" }
func (c OAuthCallback) GoString() string { return c.String() }

type LoginOptions struct {
	Manual       bool
	OpenBrowser  func(string) error
	PresentURL   func(string) // manual mode only; token/login hints are stripped
	ReadRedirect func(context.Context) (string, error)
}

func NewOAuthClient(config OAuthConfig) (*OAuthClient, error) {
	if !validClientID(config.ClientID) {
		return nil, errors.New("invalid OAuth client ID")
	}
	for _, endpoint := range []string{config.AuthorizationURL, config.TokenURL} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname())) {
			return nil, errors.New("invalid OAuth endpoint")
		}
	}
	u, err := url.Parse(config.RedirectURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("OAuth redirect must be an HTTP 127.0.0.1 loopback URL with port/path")
	}
	if port, err := strconv.Atoi(u.Port()); err != nil || port < 0 || port > 65535 {
		return nil, errors.New("invalid OAuth callback port")
	}
	if config.IDTokenVerifier == nil {
		return nil, errors.New("OAuth requires an ID token verifier")
	}
	if config.ClientID == DynamicClientID && (config.AgentName == "" || !ValidHostID(config.HostID)) {
		return nil, errors.New("dynamic OAuth registration requires an agent name and persistent host ID")
	}
	if config.Previous != nil {
		previous := *config.Previous
		previous.Scopes = previous.GrantedScopes()
		if previous.ClientID != config.ClientID || previous.ClientID == DynamicClientID || previous.Subject == "" || previous.Issuer == "" || previous.IDToken == "" || previous.ExtAgentHostID != config.HostID {
			return nil, errors.New("inconsistent/incomplete returning OAuth registration; restore Agent Home or logout before registering again")
		}
		config.Previous = &previous
	}
	return &OAuthClient{config: config, client: oauthHTTPClient(config.HTTPClient)}, nil
}
func oauthHTTPClient(source *http.Client) *http.Client {
	client := http.Client{Timeout: 30 * time.Second}
	if source != nil {
		client = *source
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func validClientID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-.:", r))
	})
}
func randomValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (o *OAuthClient) Begin() (OAuthSession, error) { return o.begin(o.config.RedirectURL) }
func (o *OAuthClient) begin(redirect string) (OAuthSession, error) {
	verifier, err := randomValue()
	if err != nil {
		return OAuthSession{}, err
	}
	state, err := randomValue()
	if err != nil {
		return OAuthSession{}, err
	}
	nonce, err := randomValue()
	if err != nil {
		return OAuthSession{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	u, _ := url.Parse(o.config.AuthorizationURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", o.config.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", o.config.Scope)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if o.config.Resource != "" {
		q.Set("resource", o.config.Resource)
	}
	if o.config.HostID != "" {
		q.Set("ext_agent_host_id", o.config.HostID)
	}
	if o.config.AgentName != "" {
		q.Set("agent_name_hint", o.config.AgentName)
	}
	if previous := o.config.Previous; previous != nil {
		q.Set("id_token_hint", previous.IDToken)
		if previous.Email != "" {
			q.Set("login_hint", previous.Email)
		}
	}
	u.RawQuery = q.Encode()
	return OAuthSession{Verifier: verifier, Challenge: challenge, State: state, Nonce: nonce, AuthorizationURL: u.String(), RedirectURL: redirect, ClientID: o.config.ClientID}, nil
}

var errState = errors.New("invalid OAuth state")
var errDenied = errors.New("OAuth authorization denied")

func (o *OAuthClient) callback(value string, s OAuthSession) (OAuthCallback, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	expected, _ := url.Parse(s.RedirectURL)
	if err != nil || u.Scheme != expected.Scheme || u.Host != expected.Host || u.EscapedPath() != expected.EscapedPath() || u.Fragment != "" || u.User != nil {
		return OAuthCallback{}, errors.New("invalid OAuth redirect URL; paste the full callback URL, not a bare code")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["state"]) != 1 || s.State == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(s.State)) != 1 {
		return OAuthCallback{}, errState
	}
	// Never interpret code/client_id/error before validating state.
	if q.Get("error") != "" {
		return OAuthCallback{}, errDenied
	}
	if len(q["code"]) != 1 || q.Get("code") == "" || strings.ContainsAny(q.Get("code"), "\r\n") {
		return OAuthCallback{}, errors.New("OAuth callback is missing/invalid authorization code")
	}
	issued := q.Get("client_id")
	if len(q["client_id"]) > 1 || issued != "" && (!validClientID(issued) || issued == DynamicClientID) {
		return OAuthCallback{}, errors.New("invalid OAuth callback client ID")
	}
	if s.ClientID == DynamicClientID {
		if issued == "" {
			return OAuthCallback{}, errors.New("OpenAI dynamic registration incomplete: callback is missing issued client_id")
		}
	} else {
		if issued != "" && issued != s.ClientID {
			return OAuthCallback{}, errors.New("OAuth callback changed the registered client ID; credentials were not replaced")
		}
		issued = s.ClientID
	}
	return OAuthCallback{Code: q.Get("code"), ClientID: issued}, nil
}

func (o *OAuthClient) Login(ctx context.Context, options LoginOptions) (OAuthCredential, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return OAuthCredential{}, err
	}
	redirect, _ := url.Parse(o.config.RedirectURL)
	var listener net.Listener
	if !options.Manual {
		var err error
		listener, err = net.Listen("tcp4", redirect.Host)
		if err != nil {
			return OAuthCredential{}, errors.New("cannot listen for OAuth loopback callback; retry with --manual and a fixed forwarded callback port")
		}
		defer listener.Close()
		redirect.Host = listener.Addr().String()
	} else if redirect.Port() == "0" {
		return OAuthCredential{}, errors.New("manual OAuth requires OPENAI_OAUTH_REDIRECT_URI with a fixed 127.0.0.1 callback port")
	}
	s, err := o.begin(redirect.String())
	if err != nil {
		return OAuthCredential{}, err
	}
	if options.Manual {
		if options.ReadRedirect == nil || options.PresentURL == nil {
			return OAuthCredential{}, errors.New("manual OAuth needs a private URL presenter and full redirect reader")
		}
		// id_token_hint must never be printed. A returning manual login can still
		// use its registered client/host, without sending the optional hint.
		u, _ := url.Parse(s.AuthorizationURL)
		q := u.Query()
		q.Del("id_token_hint")
		q.Del("login_hint")
		u.RawQuery = q.Encode()
		options.PresentURL(u.String())
		value, err := options.ReadRedirect(ctx)
		if err != nil {
			return OAuthCredential{}, err
		}
		callback, err := o.callback(value, s)
		if err != nil {
			return OAuthCredential{}, err
		}
		return o.Exchange(ctx, s, callback)
	}
	type result struct {
		callback OAuthCallback
		err      error
	}
	results := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(redirect.Path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" || r.URL.Path != redirect.Path {
			http.Error(w, "invalid callback", 400)
			return
		}
		callback, err := o.callback(s.RedirectURL+"?"+r.URL.RawQuery, s)
		if errors.Is(err, errState) {
			http.Error(w, "invalid OAuth state", 400)
			return
		}
		if err != nil {
			http.Error(w, "OAuth callback rejected. Return to the terminal.", 400)
		} else {
			fmt.Fprint(w, "Authorization callback received. Return to the terminal.")
		}
		select {
		case results <- result{callback, err}:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	// Automatic mode never displays an authorization URL (it contains hints).
	if options.OpenBrowser == nil || options.OpenBrowser(s.AuthorizationURL) != nil {
		if ctx.Err() != nil {
			return OAuthCredential{}, ctx.Err()
		}
		return OAuthCredential{}, errors.New("cannot open browser; retry --manual with a fixed loopback callback port (authorization URL was not logged)")
	}
	select {
	case <-ctx.Done():
		return OAuthCredential{}, ctx.Err()
	case <-serveErr:
		return OAuthCredential{}, errors.New("OAuth callback server stopped")
	case result := <-results:
		if result.err != nil {
			return OAuthCredential{}, result.err
		}
		return o.Exchange(ctx, s, result.callback)
	}
}
func (o *OAuthClient) Exchange(ctx context.Context, s OAuthSession, callback OAuthCallback) (OAuthCredential, error) {
	if callback.Code == "" || !validClientID(callback.ClientID) || callback.ClientID == DynamicClientID || s.ClientID != o.config.ClientID || s.Nonce == "" || s.Verifier == "" || s.RedirectURL == "" || s.ClientID != DynamicClientID && callback.ClientID != s.ClientID {
		return OAuthCredential{}, errors.New("incomplete/inconsistent OAuth transaction")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {callback.ClientID}, "code": {callback.Code}, "redirect_uri": {s.RedirectURL}, "code_verifier": {s.Verifier}}
	if o.config.Resource != "" {
		form.Set("resource", o.config.Resource)
	}
	c, err := o.token(ctx, form, OAuthCredential{})
	if err != nil {
		return OAuthCredential{}, err
	}
	if c.IDToken == "" {
		return OAuthCredential{}, errors.New("OAuth token response is missing id_token; credentials were not saved")
	}
	identity, err := o.config.IDTokenVerifier.Verify(ctx, c.IDToken, IDTokenExpectation{ClientID: callback.ClientID, Nonce: s.Nonce, ValidateNonce: true})
	if err != nil {
		return OAuthCredential{}, safeVerificationError(ctx)
	}
	if identity.Subject == "" || identity.Issuer == "" {
		return OAuthCredential{}, errors.New("OAuth ID token lacks a verified identity")
	}
	if p := o.config.Previous; p != nil && (identity.Subject != p.Subject || identity.Issuer != p.Issuer) {
		return OAuthCredential{}, errors.New("returning OAuth identity changed; existing credentials were not replaced (logout explicitly to switch accounts)")
	}
	c.ExtAgentHostID = o.config.HostID
	c.Issuer, c.Subject, c.Email = identity.Issuer, identity.Subject, identity.Email
	return c, Validate(c)
}
func safeVerificationError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Even an injected verifier must not leak tokens or unverified claim values.
	return errors.New("OAuth ID token validation failed; credentials were not saved")
}
func (o *OAuthClient) Refresh(ctx context.Context, c OAuthCredential) (OAuthCredential, error) {
	if c.RefreshToken == "" || !validClientID(c.ClientID) || c.ClientID == DynamicClientID {
		return OAuthCredential{}, errors.New("missing OAuth refresh token/issued client ID; sign in again")
	}
	if !c.RefreshAllowed(time.Now()) {
		return OAuthCredential{}, errors.New("OAuth refresh deferred until earliest_refresh_at")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "refresh_token": {c.RefreshToken}}
	if o.config.Resource != "" {
		form.Set("resource", o.config.Resource)
	}
	updated, err := o.token(ctx, form, c)
	if err != nil {
		return OAuthCredential{}, err
	}
	return updated, Validate(updated)
}
func (o *OAuthClient) token(ctx context.Context, form url.Values, previous OAuthCredential) (OAuthCredential, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", o.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthCredential{}, errors.New("invalid OAuth token endpoint")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return OAuthCredential{}, ctx.Err()
		}
		return OAuthCredential{}, errors.New("OAuth token transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return OAuthCredential{}, fmt.Errorf("OAuth token exchange failed (HTTP %d); sign in again", resp.StatusCode)
	}
	var reply struct {
		AccessToken       string          `json:"access_token"`
		RefreshToken      string          `json:"refresh_token"`
		IDToken           string          `json:"id_token"`
		ExpiresIn         int64           `json:"expires_in"`
		TokenType         string          `json:"token_type"`
		Scope             *string         `json:"scope"`
		EarliestRefreshAt json.RawMessage `json:"earliest_refresh_at"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &reply) != nil || reply.ExpiresIn <= 0 || reply.ExpiresIn > 365*24*3600 {
		if ctx.Err() != nil {
			return OAuthCredential{}, ctx.Err()
		}
		return OAuthCredential{}, errors.New("invalid OAuth token response")
	}
	c := previous
	c.AccessToken, c.ExpiresIn, c.ExpiresAt = reply.AccessToken, reply.ExpiresIn, time.Now().Add(time.Duration(reply.ExpiresIn)*time.Second)
	c.ClientID = form.Get("client_id")
	c.RefreshToken = reply.RefreshToken
	if c.RefreshToken == "" {
		c.RefreshToken = previous.RefreshToken
	}
	c.IDToken = reply.IDToken
	if c.IDToken == "" {
		c.IDToken = previous.IDToken
	}
	if reply.Scope != nil {
		c.Scope = "" // do not let an old legacy scope survive a permission reduction
		c.Scopes = strings.Fields(*reply.Scope)
	}
	c.TokenType = reply.TokenType
	if c.TokenType == "" {
		c.TokenType = "Bearer"
	}
	c.EarliestRefreshAt, err = refreshTime(reply.EarliestRefreshAt)
	if err != nil {
		return OAuthCredential{}, err
	}
	if form.Get("grant_type") == "refresh_token" && reply.IDToken != "" {
		identity, err := o.config.IDTokenVerifier.Verify(ctx, reply.IDToken, IDTokenExpectation{ClientID: c.ClientID})
		if err != nil {
			return OAuthCredential{}, safeVerificationError(ctx)
		}
		if identity.Subject != previous.Subject || identity.Issuer != previous.Issuer {
			return OAuthCredential{}, errors.New("OAuth refresh identity changed; sign in again")
		}
		c.Email = identity.Email
	}
	return c, Validate(c)
}
func refreshTime(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, nil
	}
	var unix int64
	if json.Unmarshal(raw, &unix) == nil && unix > 0 {
		return time.Unix(unix, 0).UTC(), nil
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("invalid OAuth earliest_refresh_at")
}
