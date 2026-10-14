package compatible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func TestMapping(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("wrong endpoint/auth")
		}
		var body struct {
			Model    string
			Messages []message
			Tools    []struct {
				Type     string
				Function struct {
					Name       string
					Parameters map[string]any
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "test-model" || len(body.Messages) != 5 || body.Messages[3].ToolCalls[0].Function.Arguments != `{"command":"pwd"}` || body.Messages[4].Role != "tool" || body.Messages[4].ToolCallID != "call1" || body.Tools[0].Function.Name != "bash" || body.Tools[0].Function.Parameters["type"] != "object" {
			t.Error("request mapping lost history/tools")
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done","tool_calls":[{"id":"call2","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":25}}}`))
	}))
	defer s.Close()
	p, err := New(Config{BaseURL: s.URL + "/v1"}, provider.ModelInfo{ID: "test-model"}, auth.APIKeyCredential{Key: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []schema.Message{{Role: schema.RoleSystem, Content: "system"}, {Role: schema.RoleUser, Content: "hello"}, {Role: schema.RoleAssistant, Content: "thinking"}, {Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "call1", Name: "bash", Arguments: json.RawMessage(`{"command":"pwd"}`)}}}, {Role: schema.RoleUser, ToolCallID: "call1", Content: "/workspace"}}
	tools := []schema.ToolDefinition{{Name: "bash", InputSchema: map[string]any{"type": "object"}}}
	msg, err := p.Generate(context.Background(), msgs, tools)
	if err != nil || msg.Content != "done" || msg.ToolCalls[0].Name != "bash" || *msg.Usage != (schema.Usage{PromptTokens: 100, CompletionTokens: 5, CachedPromptTokens: 25}) {
		t.Fatal("response mapping", err)
	}
}
func TestErrorsCancellationAndNoAuth(t *testing.T) {
	for _, tc := range []struct {
		status  int
		body    string
		wantErr bool
	}{{200, `{"choices":[]}`, true}, {200, `bad-json`, true}, {401, `Authorization: Bearer test-secret`, true}, {200, `{"choices":[{"message":{"content":"ok"}}]}`, false}, {200, `{"choices":[{"message":{"tool_calls":[{"id":"1","type":"function","function":{"name":"bash","arguments":"no-json"}}]}}]}`, true}} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				t.Error("none auth sent key")
			}
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		p, err := New(Config{BaseURL: s.URL}, provider.ModelInfo{ID: "a"}, auth.NoCredential{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Generate(context.Background(), nil, nil)
		if (err != nil) != tc.wantErr || err != nil && strings.Contains(err.Error(), "test-secret") {
			t.Fatal("unsafe error", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.Generate(ctx, nil, nil); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		s.Close()
	}
}
func TestCancellationDuringResponseRead(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	p, _ := New(Config{BaseURL: server.URL}, provider.ModelInfo{ID: "a"}, auth.NoCredential{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.Generate(ctx, nil, nil); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFactorySafeForConcurrentSessions(t *testing.T) {
	definition := Definition(Config{ID: "custom", BaseURL: "http://localhost/v1"}, []provider.AuthMethod{auth.CredentialNone}, nil, nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := definition.Factory(context.Background(), provider.ModelInfo{ID: "test"}, auth.NoCredential{}, definition.Config); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestCachedUsage(t *testing.T) {
	for _, usage := range []string{`{"prompt_tokens":100,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":100}}`, `{"prompt_tokens":123,"completion_tokens":45}`, `null`, `{"prompt_tokens":0,"completion_tokens":0}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"ok"}}],"usage":%s}`, usage)
		}))
		p, _ := New(Config{BaseURL: s.URL}, provider.ModelInfo{ID: "a"}, auth.NoCredential{})
		msg, err := p.Generate(context.Background(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(usage, "cached_tokens") && (msg.Usage == nil || msg.Usage.CachedPromptTokens != 100) {
			t.Fatal("cached tokens lost")
		}
		if (usage == `null` || strings.Contains(usage, `"prompt_tokens":0`)) && msg.Usage != nil {
			t.Fatal("zero usage")
		}
		s.Close()
	}
}
