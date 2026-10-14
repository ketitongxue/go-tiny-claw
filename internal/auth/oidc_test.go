package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testIssuer = "https://auth.openai.com"
const testHostID = "urn:uuid:123e4567-e89b-42d3-a456-426614174000"

type oidcFixture struct {
	key      *rsa.PrivateKey
	verifier *JWKSVerifier
	mu       sync.Mutex
	jwks     []map[string]any
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oidcFixture{key: key}
	f.jwks = []map[string]any{testJWK(&key.PublicKey, "key1")}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"keys": f.jwks})
	}))
	t.Cleanup(s.Close)
	f.verifier = NewJWKSVerifier(testIssuer, s.URL, nil)
	return f
}
func testJWK(key *rsa.PublicKey, kid string) map[string]any {
	return map[string]any{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
}
func idClaims(clientID, nonce string) map[string]any {
	return map[string]any{"iss": testIssuer, "sub": "subject-1", "aud": clientID, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce, "email": "test@example.com"}
}
func signID(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestJWKSIDTokenVerification(t *testing.T) {
	f := newOIDCFixture(t)
	expected := IDTokenExpectation{ClientID: "oaiapp_test", Nonce: "nonce-1", ValidateNonce: true}
	for _, name := range []string{"valid", "wrong issuer", "wrong audience", "entry audience", "expired", "wrong nonce", "missing nonce", "unknown key", "bad signature", "missing subject", "missing expiration", "multi audience", "wrong azp"} {
		t.Run(name, func(t *testing.T) {
			claims := idClaims(expected.ClientID, expected.Nonce)
			kid := "key1"
			switch name {
			case "wrong issuer":
				claims["iss"] = "https://attacker.example"
			case "wrong audience":
				claims["aud"] = "oaiapp_other"
			case "entry audience":
				claims["aud"] = DynamicClientID
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "wrong nonce":
				claims["nonce"] = "replayed"
			case "missing nonce":
				delete(claims, "nonce")
			case "unknown key":
				kid = "unknown"
			case "missing subject":
				delete(claims, "sub")
			case "missing expiration":
				delete(claims, "exp")
			case "multi audience":
				claims["aud"] = []string{expected.ClientID, "other"}
			case "wrong azp":
				claims["azp"] = "other"
			}
			raw := signID(t, f.key, kid, claims)
			if name == "bad signature" {
				parts := strings.Split(raw, ".")
				sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
				sig[0] ^= 1
				raw = parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig)
			}
			identity, err := f.verifier.Verify(context.Background(), raw, expected)
			if name == "valid" {
				if err != nil || identity.Subject != "subject-1" || identity.Issuer != testIssuer || identity.Email != "test@example.com" {
					t.Fatal("valid token rejected", err)
				}
			} else if err == nil {
				t.Fatal("invalid ID token accepted", name)
			}
			if err != nil && (strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "attacker")) {
				t.Fatal("unsafe verification error")
			}
		})
	}
	// Multi-audience tokens require the issued client to be the authorized party.
	claims := idClaims(expected.ClientID, expected.Nonce)
	claims["aud"], claims["azp"] = []string{expected.ClientID, "other"}, expected.ClientID
	if _, err := f.verifier.Verify(context.Background(), signID(t, f.key, "key1", claims), expected); err != nil {
		t.Fatal(err)
	}
}

func TestJWKSRotationAndCancellation(t *testing.T) {
	f := newOIDCFixture(t)
	expected := IDTokenExpectation{ClientID: "oaiapp_test", Nonce: "nonce", ValidateNonce: true}
	claims := idClaims(expected.ClientID, expected.Nonce)
	if _, err := f.verifier.Verify(context.Background(), signID(t, f.key, "key1", claims), expected); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.jwks = []map[string]any{testJWK(&key.PublicKey, "key2")}
	f.mu.Unlock()
	if _, err := f.verifier.Verify(context.Background(), signID(t, key, "key2", claims), expected); err != nil {
		t.Fatal("rotated JWKS rejected", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.verifier.Verify(ctx, "private-token", expected); err != context.Canceled {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", OAuthCredential{IDToken: "private-token"}, OAuthSession{AuthorizationURL: "private-token"}), "private-token") {
		t.Fatal("formatting leaks token/hint")
	}
}
