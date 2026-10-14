package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

type IDTokenExpectation struct {
	ClientID, Nonce string
	ValidateNonce   bool // login requires it; refresh may omit a nonce
}

type Identity struct{ Issuer, Subject, Email string }

type IDTokenVerifier interface {
	Verify(context.Context, string, IDTokenExpectation) (Identity, error)
}

// JWKSVerifier delegates cryptographic verification, key caching and rotation to
// go-oidc/go-jose. No unverified access-token claims are used as identity.
type JWKSVerifier struct {
	issuer string
	keys   oidc.KeySet
}

func NewJWKSVerifier(issuer, jwksURL string, client *http.Client) *JWKSVerifier {
	c := oauthHTTPClient(client)
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	c.Transport = boundedJWKSTransport{transport}
	ctx := oidc.ClientContext(context.Background(), c)
	return &JWKSVerifier{issuer: issuer, keys: oidc.NewRemoteKeySet(ctx, jwksURL)}
}

func (v *JWKSVerifier) Verify(ctx context.Context, raw string, expected IDTokenExpectation) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if raw == "" || len(raw) > 64<<10 || expected.ClientID == "" || expected.ClientID == DynamicClientID {
		return Identity{}, errors.New("invalid/missing OAuth ID token or audience")
	}
	verifier := oidc.NewVerifier(v.issuer, v.keys, &oidc.Config{
		ClientID: expected.ClientID, SupportedSigningAlgs: []string{"RS256", "PS256", "ES256"},
	})
	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		if ctx.Err() != nil {
			return Identity{}, ctx.Err()
		}
		// Library errors can include JWT claims or JWKS response bodies.
		return Identity{}, errors.New("OAuth ID token verification failed (signature, issuer, audience or expiration)")
	}
	if token.Issuer != v.issuer || token.Subject == "" || !token.Expiry.After(time.Now()) {
		return Identity{}, errors.New("invalid OAuth ID token identity/expiration")
	}
	if expected.ValidateNonce && (expected.Nonce == "" || subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(expected.Nonce)) != 1) {
		return Identity{}, errors.New("OAuth ID token nonce mismatch")
	}
	var claims struct {
		Email string `json:"email"`
		AZP   string `json:"azp"`
	}
	if token.Claims(&claims) != nil || claims.AZP != "" && claims.AZP != expected.ClientID || len(token.Audience) > 1 && claims.AZP != expected.ClientID {
		return Identity{}, errors.New("invalid OAuth ID token claims/authorized party")
	}
	return Identity{Issuer: token.Issuer, Subject: token.Subject, Email: claims.Email}, nil
}

type boundedJWKSTransport struct{ http.RoundTripper }
type boundedJWKSBody struct {
	io.Reader
	io.Closer
}

func (t boundedJWKSTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.RoundTripper.RoundTrip(r)
	if err == nil {
		resp.Body = boundedJWKSBody{Reader: io.LimitReader(resp.Body, 1<<20), Closer: resp.Body}
	}
	return resp, err
}
