package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testPlanScope = "chatgpt.tokens.use.direct"
const testScopes = "openid profile email offline_access resource.invoke " + testPlanScope
const testResource = "https://api.openai.com/v1"

type oauthFixture struct {
	oidc         *oidcFixture
	server       *httptest.Server
	query        url.Values
	clientID     string
	subject      string
	scope        *string
	body         string
	status       int
	newRefreshID bool
	requests     atomic.Int32
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	scope := testScopes
	f := &oauthFixture{oidc: newOIDCFixture(t), clientID: "oaiapp_test", subject: "subject-1", scope: &scope, status: 200}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.URL.Path != "/token" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Error("token endpoint/method/type")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("client_id") != f.clientID || r.Form.Has("client_secret") || r.Form.Get("resource") != testResource {
			t.Error("issued client/resource required; no client secret")
		}
		grant := r.Form.Get("grant_type")
		if grant == "authorization_code" {
			if r.Form.Get("code") != "test-code" || r.Form.Get("redirect_uri") != f.query.Get("redirect_uri") {
				t.Error("exchange must use exact transaction redirect")
			}
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if len(r.Form.Get("code_verifier")) < 43 || base64.RawURLEncoding.EncodeToString(sum[:]) != f.query.Get("code_challenge") {
				t.Error("PKCE verifier mismatch")
			}
		} else if grant == "refresh_token" {
			if r.Form.Get("refresh_token") != "refresh" || r.Form.Has("redirect_uri") || r.Form.Has("code_verifier") {
				t.Error("invalid refresh contract")
			}
		} else {
			t.Error("unknown grant")
		}
		w.WriteHeader(f.status)
		if f.body != "" {
			fmt.Fprint(w, f.body)
			return
		}
		claims := idClaims(f.clientID, f.query.Get("nonce"))
		claims["sub"] = f.subject
		reply := map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600, "token_type": "Bearer"}
		if grant == "authorization_code" || f.newRefreshID {
			reply["id_token"] = signID(t, f.oidc.key, "key1", claims)
		}
		if grant == "refresh_token" {
			reply["access_token"], reply["refresh_token"] = "new-access", "rotated"
			reply["earliest_refresh_at"] = time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
		}
		if f.scope != nil {
			reply["scope"] = *f.scope
		}
		json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *oauthFixture) client(t *testing.T, clientID string, previous *OAuthCredential) *OAuthClient {
	t.Helper()
	c, err := NewOAuthClient(OAuthConfig{ClientID: clientID, AuthorizationURL: f.server.URL + "/authorize", TokenURL: f.server.URL + "/token", RedirectURL: "http://127.0.0.1:0/auth/callback", Scope: testScopes, Resource: testResource, AgentName: "go-tiny-claw", HostID: testHostID, IDTokenVerifier: f.oidc.verifier, Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func (f *oauthFixture) browser(t *testing.T, callbackID string, inspect func(url.Values)) func(string) error {
	t.Helper()
	return func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		f.query = u.Query()
		q := f.query
		redirect, _ := url.Parse(q.Get("redirect_uri"))
		if redirect.Hostname() != "127.0.0.1" || redirect.Port() == "0" || redirect.Path != "/auth/callback" {
			t.Error("dynamic loopback redirect not resolved")
		}
		if inspect != nil {
			inspect(q)
		}
		bad, err := http.Get(q.Get("redirect_uri") + "?state=wrong&code=test-code&client_id=attacker")
		if err != nil {
			return err
		}
		bad.Body.Close()
		if bad.StatusCode != 400 {
			t.Error("bad state accepted")
		}
		callback := url.Values{"state": {q.Get("state")}, "code": {"test-code"}, "scope": {testScopes}}
		if callbackID != "" {
			callback.Set("client_id", callbackID)
		}
		good, err := http.Get(q.Get("redirect_uri") + "?" + callback.Encode())
		if err == nil {
			good.Body.Close()
		}
		return err
	}
}

func TestFirstDynamicRegistrationAndReturningLogin(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.client(t, DynamicClientID, nil)
	var first url.Values
	credential, err := client.Login(context.Background(), LoginOptions{PresentURL: func(string) { t.Error("automatic login printed authorization URL") }, OpenBrowser: f.browser(t, f.clientID, func(q url.Values) {
		first = q
		for key, want := range map[string]string{"client_id": DynamicClientID, "agent_name_hint": "go-tiny-claw", "ext_agent_host_id": testHostID, "scope": testScopes, "resource": testResource, "response_type": "code", "code_challenge_method": "S256"} {
			if q.Get(key) != want {
				t.Errorf("authorization field %s mismatch", key)
			}
		}
		if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
			t.Error("missing transaction randomness")
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	if credential.ClientID != "oaiapp_test" || credential.IDToken == "" || credential.ExtAgentHostID != testHostID || credential.Subject != "subject-1" || !credential.HasScope(testPlanScope) || credential.ExpiresIn != 3600 {
		t.Fatal("registration/identity/grant not preserved")
	}
	store := NewFileStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.Save(context.Background(), "openai", credential); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(context.Background(), "openai")
	if err != nil {
		t.Fatal(err)
	}
	previous := stored.(OAuthCredential)
	returning := f.client(t, previous.ClientID, &previous)
	second, err := returning.Login(context.Background(), LoginOptions{OpenBrowser: f.browser(t, f.clientID, func(q url.Values) {
		if q.Get("client_id") != previous.ClientID || q.Get("ext_agent_host_id") != first.Get("ext_agent_host_id") || q.Get("id_token_hint") != previous.IDToken || q.Get("login_hint") != previous.Email {
			t.Error("returning registration/hints not reused")
		}
		for _, key := range []string{"state", "nonce", "code_challenge"} {
			if q.Get(key) == first.Get(key) {
				t.Error("reused OAuth transaction", key)
			}
		}
	})})
	if err != nil || second.Subject != credential.Subject || second.ClientID != credential.ClientID {
		t.Fatal("returning login", err)
	}
}

func TestDynamicMissingIssuedIDAndReturningChangedID(t *testing.T) {
	for _, name := range []string{"missing", "entry-point", "changed"} {
		t.Run(name, func(t *testing.T) {
			f := newOAuthFixture(t)
			clientID, callbackID := DynamicClientID, ""
			if name == "entry-point" {
				callbackID = DynamicClientID
			}
			if name == "changed" {
				clientID, callbackID = f.clientID, "oaiapp_other"
			}
			client := f.client(t, clientID, nil)
			if _, err := client.Login(context.Background(), LoginOptions{OpenBrowser: f.browser(t, callbackID, nil)}); err == nil {
				t.Fatal("invalid registration accepted")
			}
			if f.requests.Load() != 0 {
				t.Fatal("token exchanged before issued ID validated")
			}
		})
	}
}
func TestReturningIdentityAndNonceRejectDoNotOverwrite(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.client(t, DynamicClientID, nil)
	ctx := context.Background()
	c, err := client.Login(ctx, LoginOptions{OpenBrowser: f.browser(t, f.clientID, nil)})
	if err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.Save(ctx, "openai", c); err != nil {
		t.Fatal(err)
	}
	returning := f.client(t, c.ClientID, &c)
	f.subject = "different-subject"
	if _, err := returning.Login(ctx, LoginOptions{OpenBrowser: f.browser(t, f.clientID, nil)}); err == nil {
		t.Fatal("changed subject accepted")
	}
	saved, _ := store.Load(ctx, "openai")
	if saved.(OAuthCredential).Subject != c.Subject {
		t.Fatal("credential overwritten")
	}
	f.subject = c.Subject
	s, _ := client.Begin()
	f.query, _ = url.ParseQuery(strings.SplitN(s.AuthorizationURL, "?", 2)[1])
	f.query.Set("nonce", "wrong-nonce")
	if _, err := client.Exchange(ctx, s, OAuthCallback{Code: "test-code", ClientID: f.clientID}); err == nil {
		t.Fatal("wrong nonce accepted")
	}
}
func TestCallbackStateAndURLValidation(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.client(t, DynamicClientID, nil)
	s, _ := client.Begin()
	good := s.RedirectURL + "?code=test-code&client_id=oaiapp_test&state=" + s.State
	if callback, err := client.callback(good, s); err != nil || callback.ClientID != f.clientID {
		t.Fatal(err)
	}
	for _, value := range []string{
		s.RedirectURL + "?state=wrong&error=access_denied", good + "&state=other", good + "&code=other", good + "&client_id=other", strings.Replace(good, "/auth/callback", "/different", 1), strings.Replace(good, "127.0.0.1", "localhost", 1), "https://attacker.example/auth/callback?state=" + s.State, "test-code",
	} {
		if _, err := client.callback(value, s); err == nil {
			t.Fatal("invalid callback accepted")
		}
	}
	if _, err := client.callback(s.RedirectURL+"?state=wrong&error=access_denied", s); !errors.Is(err, errState) {
		t.Fatal("state was not checked first")
	}
}
func TestOAuthManualRequiresFullCallbackAndNeverDisplaysHint(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.client(t, DynamicClientID, nil)
	client.config.RedirectURL = "http://127.0.0.1:1455/auth/callback"
	options := LoginOptions{Manual: true, PresentURL: func(value string) {
		u, _ := url.Parse(value)
		f.query = u.Query()
		if f.query.Has("id_token_hint") || f.query.Has("login_hint") {
			t.Error("manual presenter leaked hint")
		}
	}, ReadRedirect: func(context.Context) (string, error) {
		return f.query.Get("redirect_uri") + "?" + url.Values{"state": {f.query.Get("state")}, "code": {"test-code"}, "client_id": {f.clientID}}.Encode(), nil
	}}
	c, err := client.Login(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	returning := f.client(t, c.ClientID, &c)
	returning.config.RedirectURL = client.config.RedirectURL
	if _, err := returning.Login(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.ReadRedirect = func(context.Context) (string, error) { return "test-code", nil }
	if _, err := client.Login(context.Background(), options); err == nil {
		t.Fatal("bare manual code bypassed state")
	}
}
func TestOAuthCancellationClosesListener(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.client(t, DynamicClientID, nil)
	ctx, cancel := context.WithCancel(context.Background())
	var address string
	_, err := client.Login(ctx, LoginOptions{OpenBrowser: func(value string) error {
		u, _ := url.Parse(value)
		redirect, _ := url.Parse(u.Query().Get("redirect_uri"))
		address = redirect.Host
		cancel()
		return nil
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal("callback listener leaked", err)
	}
	ln.Close()
}
func TestOAuthRefreshRotationConcurrentAndOptionalMetadata(t *testing.T) {
	f := newOAuthFixture(t)
	client := f.client(t, DynamicClientID, nil)
	ctx := context.Background()
	c, err := client.Login(ctx, LoginOptions{OpenBrowser: f.browser(t, f.clientID, nil)})
	if err != nil {
		t.Fatal(err)
	}
	c.ExpiresAt = time.Now().Add(-time.Minute)
	m := NewManager(NewFileStore(filepath.Join(t.TempDir(), "auth.json")), nil)
	if err := m.Register("openai", Policy{Methods: []CredentialType{CredentialOAuth}, Refresh: client.Refresh}); err != nil {
		t.Fatal(err)
	}
	if err := m.Login(ctx, "openai", c); err != nil {
		t.Fatal(err)
	}
	f.requests.Store(0)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := m.GetCredential(ctx, "openai")
			if err != nil || got.(OAuthCredential).AccessToken != "new-access" {
				t.Error("refresh", err)
			}
		}()
	}
	wg.Wait()
	if f.requests.Load() != 1 {
		t.Fatal("duplicate account refresh")
	}
	stored, _ := m.StoredCredential(ctx, "openai")
	updated := stored.(OAuthCredential)
	if updated.RefreshToken != "rotated" || updated.ClientID != c.ClientID || updated.IDToken != c.IDToken || updated.Subject != c.Subject || updated.EarliestRefreshAt.IsZero() || !updated.HasScope(testPlanScope) {
		t.Fatal("refresh rotation/metadata not saved")
	}
	// Omitted fields preserve the grant, while an explicitly empty scope removes it.
	f.body = `{"access_token":"new-access","expires_in":3600}`
	optional, err := client.Refresh(ctx, c)
	if err != nil || optional.RefreshToken != c.RefreshToken || optional.IDToken != c.IDToken || !optional.HasScope(testPlanScope) {
		t.Fatal("optional metadata lost", err)
	}
	f.body = `{"access_token":"new-access","expires_in":3600,"scope":""}`
	cleared, err := client.Refresh(ctx, c)
	if err != nil || cleared.HasScope(testPlanScope) {
		t.Fatal("permission reduction ignored", err)
	}
	f.body = ""
	f.newRefreshID = true
	f.subject = "other-subject"
	if _, err := client.Refresh(ctx, c); err == nil {
		t.Fatal("changed refresh identity accepted")
	}
}
func TestMissingIDTokenAndGrantedScope(t *testing.T) {
	for _, name := range []string{"identity only", "omitted scope", "missing ID token"} {
		t.Run(name, func(t *testing.T) {
			f := newOAuthFixture(t)
			if name == "identity only" {
				scope := "openid profile email offline_access"
				f.scope = &scope
			}
			if name == "omitted scope" {
				f.scope = nil
			}
			if name == "missing ID token" {
				f.body = `{"access_token":"access","expires_in":3600,"scope":"` + testScopes + `"}`
			}
			c, err := f.client(t, DynamicClientID, nil).Login(context.Background(), LoginOptions{OpenBrowser: f.browser(t, f.clientID, nil)})
			if name == "missing ID token" {
				if err == nil {
					t.Fatal("OAuth succeeded without ID token validation")
				}
				return
			}
			if err != nil || c.Subject != "subject-1" || c.HasScope(testPlanScope) {
				t.Fatal("identity/granted scope separation failed", err)
			}
		})
	}
}

func TestTokenHTTPErrorAndRefreshTimeFormats(t *testing.T) {
	f := newOAuthFixture(t)
	f.status = 401
	f.body = `{"error":"private-refresh-token"}`
	client := f.client(t, DynamicClientID, nil)
	if _, err := client.Refresh(context.Background(), OAuthCredential{ClientID: f.clientID, RefreshToken: "refresh"}); err == nil || strings.Contains(err.Error(), "private-refresh-token") {
		t.Fatal("unsafe refresh error", err)
	}
	for _, raw := range []string{`1770000000`, `"2026-02-02T02:40:00Z"`} {
		if parsed, err := refreshTime(json.RawMessage(raw)); err != nil || parsed.Unix() != 1770000000 {
			t.Fatal("refresh time format", err)
		}
	}
}

func TestOAuthInvalidTokenResponsesAndRedaction(t *testing.T) {
	for _, body := range []string{`private-token`, `{"access_token":"private-token","expires_in":-1}`, `{"expires_in":3600}`, `{"access_token":"private-token","expires_in":3600,"token_type":"MAC"}`, `{"access_token":"private-token","expires_in":3600,"earliest_refresh_at":"bad"}`} {
		f := newOAuthFixture(t)
		f.body = body
		client := f.client(t, DynamicClientID, nil)
		_, err := client.Refresh(context.Background(), OAuthCredential{ClientID: f.clientID, RefreshToken: "refresh"})
		if err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatal("unsafe invalid response", err)
		}
	}
}
