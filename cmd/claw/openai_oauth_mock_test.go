package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/app"
	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/openai"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// Signature/issuer/audience failures are covered with a REAL mock JWKS in auth.
// This integration substitutes only that verifier boundary, not the OAuth
// transaction, store, discovery, Responses transport, engine or Docker tools.
type mockOAuthVerifier struct{}

func (mockOAuthVerifier) Verify(_ context.Context, raw string, e auth.IDTokenExpectation) (auth.Identity, error) {
	if e.ClientID != "oaiapp_test" || !e.ValidateNonce || e.Nonce == "" || raw != "mock-id-"+e.Nonce {
		return auth.Identity{}, errors.New("invalid mock ID token transaction")
	}
	return auth.Identity{Issuer: openai.OAuthIssuer, Subject: "subject-1", Email: "test@example.com"}, nil
}

func TestOpenAIOAuthMockDockerAgentIntegration(t *testing.T) {
	ctx := context.Background()
	if err := executor.DockerAvailable(ctx); err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	image := os.Getenv("CLAW_SANDBOX_TEST_IMAGE")
	if image == "" {
		image = executor.DefaultImage
	}
	probe, err := executor.NewDockerExecutor(image, executor.CodingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.CheckImage(ctx); err != nil {
		t.Skipf("build sandbox image first: %v", err)
	}
	paths, _ := config.NewPaths(t.TempDir())
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := app.Load(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := r.Hosts.OpenAIHostID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var nonce atomic.Value
	nonce.Store("")
	var calls, catalogs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/token":
			req.ParseForm()
			if req.Form.Get("client_id") != "oaiapp_test" || req.Form.Has("client_secret") || req.Form.Get("resource") != openai.OAuthResource {
				t.Error("registration/token contract")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "mock-access", "refresh_token": "mock-refresh", "id_token": "mock-id-" + nonce.Load().(string), "expires_in": 3600, "token_type": "Bearer", "scope": openai.OAuthScopes})
		case "/v1/models":
			catalogs.Add(1)
			if req.Method != "GET" || req.Header.Get("Authorization") != "Bearer mock-access" {
				t.Error("OAuth discovery auth")
			}
			fmt.Fprint(w, `{"models":[{"slug":"model-a","display_name":"Model A","visibility":"list"},{"slug":"hidden","visibility":"hidden"}]}`)
		case "/v1/responses":
			if req.Header.Get("Authorization") != "Bearer mock-access" || req.Header.Get("Accept") != "text/event-stream" {
				t.Error("OAuth inference auth")
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			encoded, _ := json.Marshal(body)
			if body["store"] != false || body["stream"] != true || body["model"] != "model-a" || body["previous_response_id"] != nil {
				t.Error("plan request contract")
			}
			for _, secret := range []string{"mock-access", "mock-refresh", "mock-id-", hostID} {
				if strings.Contains(string(encoded), secret) {
					t.Error("credential/host ID leaked into model input")
				}
			}
			// Match the observed public plan route: SSE without Content-Type,
			// finalized items followed by a completion summary with output:[].
			w.Header()["Content-Type"] = nil
			var item map[string]any
			inputTokens, outputTokens := 10, 2
			if calls.Add(1) == 1 {
				command := "test -z \"$OPENAI_API_KEY\" && test ! -e /root/.claw/auth.json && printf 'sandbox-ok\\n' && ls -1"
				args, _ := json.Marshal(map[string]string{"command": command})
				item = map[string]any{"type": "function_call", "call_id": "call-bash", "name": "bash", "arguments": string(args)}
			} else {
				if !strings.Contains(string(encoded), "function_call_output") || !strings.Contains(string(encoded), "sandbox-ok") || !strings.Contains(string(encoded), "sample.go") {
					t.Error("full tool history/Docker output not preserved")
				}
				item = map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "目录包含 sample.go"}}}
				inputTokens, outputTokens = 20, 3
			}
			payload, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			payload, _ = json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{}, "usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens}}})
			fmt.Fprintf(w, "data: %s\n\n", payload)
		default:
			t.Error("unexpected endpoint")
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	r.DiscoverOpenAI = func(ctx context.Context, c auth.OAuthCredential) ([]provider.ModelInfo, error) {
		return openai.DiscoverModels(ctx, c, server.URL+"/v1/models", nil)
	}
	var output, logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	cio := commandIO{output: &output, oauthLogin: func(ctx context.Context, options auth.LoginOptions) (auth.OAuthCredential, error) {
		client, err := auth.NewOAuthClient(auth.OAuthConfig{ClientID: auth.DynamicClientID, AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token", RedirectURL: "http://127.0.0.1:0/auth/callback", HostID: hostID, AgentName: "go-tiny-claw", Resource: openai.OAuthResource, Scope: openai.OAuthScopes, IDTokenVerifier: mockOAuthVerifier{}})
		if err != nil {
			return auth.OAuthCredential{}, err
		}
		options.OpenBrowser = func(value string) error {
			u, _ := url.Parse(value)
			q := u.Query()
			nonce.Store(q.Get("nonce"))
			if q.Get("client_id") != auth.DynamicClientID {
				t.Error("static client used on first login")
			}
			response, err := http.Get(q.Get("redirect_uri") + "?" + url.Values{"code": {"mock-code"}, "state": {q.Get("state")}, "client_id": {"oaiapp_test"}}.Encode())
			if err == nil {
				response.Body.Close()
			}
			return err
		}
		return client.Login(ctx, options)
	}}
	if err := runCommand(ctx, []string{"auth", "login", "openai"}, r, cio); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(ctx, []string{"models"}, r, cio); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(ctx, []string{"model", "set", "openai/model-a"}, r, cio); err != nil {
		t.Fatal(err)
	}
	if catalogs.Load() != 2 || strings.Contains(output.String(), "hidden") || strings.Contains(output.String(), "gpt-5.2") {
		t.Fatal("wrong available models")
	}
	r, err = app.Load(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Replace only the endpoint boundary after restart; production always uses
	// https://api.openai.com/v1/responses, with the same OAuth request builder.
	definitions := provider.NewRegistry()
	for _, d := range r.Providers.List() {
		if d.ID == "openai" {
			d.Factory = func(_ context.Context, m provider.ModelInfo, c auth.Credential, _ provider.ProviderConfig) (provider.LLMProvider, error) {
				return openai.New(openai.Config{SubscriptionEndpoint: server.URL + "/v1/responses"}, m, c)
			}
		}
		if err := definitions.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	r.Manager, err = provider.NewManager(definitions, r.Models, r.Auth)
	if err != nil {
		t.Fatal(err)
	}
	ref, _, err := config.ResolveSelectedModel("", r.Config)
	if err != nil {
		t.Fatal(err)
	}
	p, model, err := r.Manager.Create(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	opts := options{mode: "demo", workDir: workspace, sandbox: executor.ModeDocker, sandboxImage: image, modelInfo: model, authType: string(auth.CredentialOAuth)}
	executors, err := configureExecutors(ctx, opts, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	sess := ctxpkg.NewSession("oauth-docker", workspace)
	reporter := engine.NewTerminalReporter()
	eng := newSessionEngine(p, sess, reporter, opts, executors)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: "读取当前工作区文件，并总结目录结构"})
	if err := eng.Run(ctx, sess, reporter); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || sess.UsageSnapshot().BillingMode != provider.BillingSubscription {
		t.Fatal("subscription/tool round-trip not completed")
	}
	traces, _ := filepath.Glob(filepath.Join(workspace, ".claw", "traces", "*.json"))
	if len(traces) != 1 {
		t.Fatal("missing trace")
	}
	trace, _ := os.ReadFile(traces[0])
	for _, secret := range []string{"mock-access", "mock-refresh", "mock-id-", hostID} {
		if strings.Contains(output.String()+logs.String()+string(trace), secret) {
			t.Fatal("OAuth credential/host metadata entered logs/trace")
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, ".claw", "auth.json")); !os.IsNotExist(err) {
		t.Fatal("workspace credential created")
	}
}
