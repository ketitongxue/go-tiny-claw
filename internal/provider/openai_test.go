package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestOpenAIProviderExtractsCachedPromptUsage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		usageJSON string
		want      *schema.Usage
	}{
		{
			name:      "partial cache hit",
			usageJSON: `{"prompt_tokens":1000000,"completion_tokens":100000,"total_tokens":1100000,"prompt_tokens_details":{"cached_tokens":250000}}`,
			want:      &schema.Usage{PromptTokens: 1_000_000, CompletionTokens: 100_000, CachedPromptTokens: 250_000},
		},
		{
			name:      "full cache hit without output",
			usageJSON: `{"prompt_tokens":100,"completion_tokens":0,"total_tokens":100,"prompt_tokens_details":{"cached_tokens":100}}`,
			want:      &schema.Usage{PromptTokens: 100, CachedPromptTokens: 100},
		},
		{
			name:      "no cache details",
			usageJSON: `{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168}`,
			want:      &schema.Usage{PromptTokens: 123, CompletionTokens: 45},
		},
		{
			name:      "empty cache details",
			usageJSON: `{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168,"prompt_tokens_details":{}}`,
			want:      &schema.Usage{PromptTokens: 123, CompletionTokens: 45},
		},
		{name: "no usage", usageJSON: `null`},
		{name: "zero usage", usageJSON: `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"id":"test","object":"chat.completion","model":"glm-5.3","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"answer"}}],"usage":%s}`, tc.usageJSON)
			}))
			defer server.Close()

			p := &OpenAIProvider{
				client: openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL+"/"), option.WithMaxRetries(0)),
				model:  "glm-5.3",
			}
			msg, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hello"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if msg.Role != schema.RoleAssistant || msg.Content != "answer" {
				t.Fatalf("unexpected response: %+v", msg)
			}
			if tc.want == nil {
				if msg.Usage != nil {
					t.Fatalf("unexpected usage: %+v", msg.Usage)
				}
			} else if msg.Usage == nil || *msg.Usage != *tc.want {
				t.Fatalf("usage = %+v, want %+v", msg.Usage, tc.want)
			}
		})
	}
}
