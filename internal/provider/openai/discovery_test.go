package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

func TestOAuthModelDiscoveryAndBoundCache(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("OAuth catalog contract")
		}
		fmt.Fprint(w, `{"models":[{"slug":"model-a","display_name":"Model A","visibility":"list"},{"slug":"hidden","visibility":"hidden"},{"slug":"no-tools","visibility":"list","tool_calling":false}]}`)
	}))
	defer s.Close()
	c := testOAuthCredential()
	models, err := DiscoverModels(context.Background(), c, s.URL+"/v1/models", nil)
	if err != nil || len(models) != 2 || models[0].ID != "model-a" || models[0].Name != "Model A" || !models[0].ToolCalling || models[0].BillingMode != provider.BillingSubscription || models[1].ToolCalling {
		t.Fatal("wrong OAuth catalog", err)
	}
	path := filepath.Join(t.TempDir(), "openai-models.json")
	if err := SaveCatalog(path, c, models); err != nil {
		t.Fatal(err)
	}
	cached, err := LoadCatalog(path, c)
	if err != nil || len(cached) != 2 || cached[0].Ref().String() != "openai/model-a" {
		t.Fatal("cache reload", err)
	}
	data, _ := os.ReadFile(path)
	for _, secret := range []string{c.AccessToken, c.RefreshToken, c.IDToken} {
		if strings.Contains(string(data), secret) {
			t.Fatal("credential in model cache")
		}
	}
	for _, change := range []string{"client", "subject", "issuer", "host", "scope"} {
		other := c
		switch change {
		case "client":
			other.ClientID = "oaiapp_other"
		case "subject":
			other.Subject = "other-subject"
		case "issuer":
			other.Issuer = "https://other.example"
		case "host":
			other.ExtAgentHostID = "urn:uuid:00000000-0000-4000-8000-000000000000"
		case "scope":
			other.Scopes = []string{"openid"}
		}
		got, err := LoadCatalog(path, other)
		if err != nil || len(got) != 0 {
			t.Fatal("catalog reused across authorization", change, err)
		}
	}
	c.Scopes = []string{"openid"}
	if _, err := DiscoverModels(context.Background(), c, s.URL+"/v1/models", nil); err != ErrPlanUsageDisabled || calls.Load() != 1 {
		t.Fatal("ungranted plan made a request", err)
	}
	if _, err := DiscoverModels(context.Background(), auth.OAuthCredential{}, s.URL, nil); err == nil {
		t.Fatal("invalid identity discovered models")
	}
}
func TestOAuthModelDiscoveryInvalidCatalog(t *testing.T) {
	for _, body := range []string{`test-secret`, `{"data":[{"id":"gpt-5.2"}]}`, `{"models":null}`, `{"models":[{"slug":"","visibility":"list"}]}`, `{"models":[{"slug":"a","visibility":"list"},{"slug":"a","visibility":"list"}]}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := DiscoverModels(context.Background(), testOAuthCredential(), s.URL, nil)
		s.Close()
		if err == nil || strings.Contains(err.Error(), "test-secret") {
			t.Fatal("invalid API-key/plan catalog accepted", err)
		}
	}
}
