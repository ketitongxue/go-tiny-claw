package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func httpAdapter(t *testing.T, handler http.HandlerFunc) *Adapter {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := newClient("123:test-secret")
	c.baseURL = server.URL
	return &Adapter{client: c}
}

func TestClientClassifiesErrorsAndHidesSecrets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		permanent bool
	}{
		{"invalid-token", 401, `{"ok":false,"error_code":401,"description":"private-response-test-secret"}`, true},
		{"unknown-token", 404, `{"ok":false,"error_code":404}`, true},
		{"forbidden", 403, `{"ok":false,"error_code":403}`, true},
		{"bad-config", 400, `{"ok":false,"error_code":400}`, true},
		{"conflict", 409, `{"ok":false,"error_code":409}`, true},
		{"rate-limit", 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":37}}`, false},
		{"server", 500, `{"ok":false,"error_code":500}`, false},
		{"proxy", 502, `private-response-test-secret`, false},
		{"timeout", 408, `{"ok":false,"error_code":408}`, false},
		{"malformed", 200, `private-response-test-secret`, false},
		{"null-result", 200, `{"ok":true,"result":null}`, false},
		{"api-error-in-200", 200, `{"ok":false,"error_code":401}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := httpAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := a.client.getMe(context.Background())
			if err == nil || channel.IsPermanent(err) != tc.permanent || strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "private-response") || strings.Contains(err.Error(), "/bot123") {
				t.Fatalf("unsafe/misclassified API error: %v", err)
			}
			if tc.name == "rate-limit" {
				var limited interface{ RetryAfter() time.Duration }
				if !errors.As(err, &limited) || limited.RetryAfter() != 37*time.Second {
					t.Fatal("rate limit hint lost", err)
				}
			}
		})
	}
}

func TestClientNetworkErrorsNeverExposeTokenURL(t *testing.T) {
	c := newClient("123:test-secret")
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset: /bot123:test-secret/getMe")
	})
	if _, err := c.getMe(context.Background()); err == nil || channel.IsPermanent(err) || strings.Contains(err.Error(), "test-secret") {
		t.Fatal("transport error leaked or became permanent", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.getMe(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	a := httpAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/must-not-follow", http.StatusFound)
	})
	if _, err := a.client.getMe(context.Background()); err == nil || calls.Load() != 1 {
		t.Fatal("bot-token request followed a redirect", err, calls.Load())
	}
}

func TestTokenValidationBelongsToSupervisedStart(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	if _, err := NewAdapter(); err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatal(err)
	}
	for _, token := range []string{"not-a-token", "123:", "0:secret", "123:secret/escape", "123:secret\nvalue"} {
		t.Setenv("TELEGRAM_BOT_TOKEN", token)
		a, err := NewAdapter()
		if err != nil {
			t.Fatal("constructor must not prevent healthy channels from starting", err)
		}
		err = a.Start(context.Background(), func(context.Context, channel.InboundMessage) error { return nil })
		if !channel.IsPermanent(err) || strings.Contains(err.Error(), token) {
			t.Fatal("invalid token was retried or exposed", err)
		}
	}
}
