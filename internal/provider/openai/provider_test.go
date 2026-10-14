package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func TestResponsesMappingAndAuth(t *testing.T) {
	payload := `{"status":"completed","output":[{"type":"reasoning"},{"type":"message","content":[{"type":"output_text","text":"answer"}]},{"type":"function_call","id":"fc-not-call-id","call_id":"call2","name":"bash","arguments":"{\"command\":\"ls\"}"}],"usage":{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":20}}}`
	for _, oauth := range []bool{false, true} {
		t.Run(fmt.Sprint(oauth), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("bearer missing")
				}
				var b struct {
					Model, Instructions string
					Input               []map[string]any
					Tools               []map[string]any
					Store, Stream       bool
				}
				if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
					t.Fatal(err)
				}
				if b.Model != "test" || b.Instructions != "system" || b.Store || b.Stream != oauth || len(b.Input) != 4 || b.Input[2]["type"] != "function_call" || b.Input[3]["call_id"] != "call1" || b.Tools[0]["name"] != "bash" || b.Tools[0]["strict"] != false {
					t.Error("Responses mapping")
				}
				if oauth {
					if r.Header.Get("ChatGPT-Account-Id") != "" || r.Header.Get("Accept") != "text/event-stream" || r.URL.Path != "/v1/responses" {
						t.Error("OAuth must use public Responses contract")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", payload)
				} else {
					if r.URL.Path != "/v1/responses" {
						t.Error("API key used subscription endpoint")
					}
					fmt.Fprint(w, payload)
				}
			}))
			defer s.Close()
			var c auth.Credential = auth.APIKeyCredential{Key: "test-secret"}
			if oauth {
				c = testOAuthCredential()
			}
			p, err := New(Config{APIEndpoint: s.URL + "/v1/responses"}, provider.ModelInfo{ID: "test"}, c)
			if err != nil {
				t.Fatal(err)
			}
			msgs := []schema.Message{{Role: schema.RoleSystem, Content: "system"}, {Role: schema.RoleUser, Content: "hello"}, {Role: schema.RoleAssistant, Content: "thinking", ToolCalls: []schema.ToolCall{{ID: "call1", Name: "bash", Arguments: json.RawMessage(`{"command":"pwd"}`)}}}, {Role: schema.RoleUser, ToolCallID: "call1", Content: "/workspace"}}
			msg, err := p.Generate(context.Background(), msgs, []schema.ToolDefinition{{Name: "bash", InputSchema: map[string]any{"type": "object"}}})
			if err != nil || msg.Content != "answer" || msg.ToolCalls[0].ID != "call2" || *msg.Usage != (schema.Usage{PromptTokens: 100, CompletionTokens: 10, CachedPromptTokens: 20}) {
				t.Fatal("output mapping", err)
			}
		})
	}
}
func TestResponsesCancellationDuringStream(t *testing.T) {
	for _, media := range []string{"text/event-stream", "missing"} {
		t.Run(media, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = nil
				if media != "missing" {
					w.Header().Set("Content-Type", media)
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			p, _ := New(Config{SubscriptionEndpoint: server.URL}, provider.ModelInfo{ID: "a"}, testOAuthCredential())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := p.Generate(ctx, nil, nil); done <- err }()
			<-started
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestResponsesErrors(t *testing.T) {
	for _, data := range []string{`bad`, `{}`, `{"status":"incomplete"}`, `{"output":[{"type":"function_call","call_id":"c","name":"bash","arguments":"bad"}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, data) }))
		p, _ := New(Config{APIEndpoint: server.URL}, provider.ModelInfo{ID: "a"}, auth.APIKeyCredential{Key: "secret"})
		if _, err := p.Generate(context.Background(), nil, nil); err == nil {
			t.Error("malformed accepted", data)
		}
		server.Close()
	}
	for _, data := range []string{"data: {\"type\":\"response.failed\"}\n\n", "data: nope\n\n", "data: [DONE]\n\n"} {
		if _, err := readEvents(strings.NewReader(data)); err == nil {
			t.Fatal("bad SSE accepted")
		}
	}
}
