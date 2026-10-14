package wire

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransportDoesNotFollowCredentialRedirectOrEchoErrors(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed, credential could leak") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	if _, err := Post(context.Background(), Client(nil), source.URL, map[string]string{"Authorization": "Bearer private-secret"}, map[string]string{"model": "a"}); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("redirect/unsafe error", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer bad.Close()
	if _, err := Post(context.Background(), Client(nil), bad.URL, map[string]string{"Authorization": "Bearer private-secret"}, nil); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("unsafe HTTP error", err)
	}
}
