package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func TestSSEAggregatesTextToolsAndCompletion(t *testing.T) {
	stream := `: heartbeat

event: response.output_item.added
data: {"output_index":0,"item":{"type":"message","content":[]}}

data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello "}

data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"world"}

data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"hello world"}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc1","call_id":"call1","name":"bash","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":"}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"pwd\"}"}

data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":12,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}}}

`
	r, err := readEvents(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mapResponse(r)
	if err != nil || msg.Content != "hello world" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call1" || string(msg.ToolCalls[0].Arguments) != `{"command":"pwd"}` || msg.Usage.PromptTokens != 12 {
		t.Fatal("SSE aggregation failed", err)
	}
	// A complete terminal snapshot is authoritative, not appended to deltas.
	full := strings.Replace(stream, `"status":"completed","usage"`, `"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"final"}]}],"usage"`, 1)
	r, err = readEvents(strings.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}
	msg, err = mapResponse(r)
	if err != nil || msg.Content != "final" || len(msg.ToolCalls) != 0 {
		t.Fatal("terminal snapshot duplicated deltas", err)
	}
	empty := strings.Replace(stream, `"status":"completed","usage"`, `"status":"completed","output":[],"usage"`, 1)
	r, err = readEvents(strings.NewReader(empty))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mapResponse(r); err == nil {
		t.Fatal("partial tool calls resurrected after an authoritative empty snapshot")
	}
}

func TestOAuthGenerateStreamFailuresAndSubscriptionLimits(t *testing.T) {
	for _, tc := range []struct {
		name, data, want string
		status           int
		json             bool
	}{
		{name: "failed", data: "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"test-secret\"}}}\n\n"},
		{name: "incomplete", data: "data: {\"type\":\"response.incomplete\"}\n\n"},
		{name: "eof", data: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"},
		{name: "done without completion", data: "data: [DONE]\n\n"},
		{name: "malformed", data: "data: test-secret\n\n"},
		{name: "false completed", data: "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\"}}\n\n"},
		{name: "JSON not SSE", data: `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"not streaming"}]}]}`, json: true},
		{name: "HTTP quota", data: `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"test-secret"}}`, status: 429, want: "subscription_sharing_usage_limit_exceeded"},
		{name: "HTTP unavailable", data: `{"error":{"code":"subscription_sharing_usage_unavailable","message":"test-secret"}}`, status: 403, want: "subscription_sharing_usage_unavailable"},
		{name: "stream quota", data: "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\"}}}\n\n", want: "subscription_sharing_usage_limit_exceeded"},
		{name: "stream unavailable", data: "data: {\"type\":\"error\",\"code\":\"subscription_sharing_usage_unavailable\"}\n\n", want: "subscription_sharing_usage_unavailable"},
		{name: "untrusted HTTP error", data: `{"error":{"code":"test-secret","message":"test-secret"}}`, status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.json {
					w.Header().Set("Content-Type", "application/json")
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				fmt.Fprint(w, tc.data)
			}))
			defer server.Close()
			p, err := New(Config{SubscriptionEndpoint: server.URL}, provider.ModelInfo{ID: "test"}, testOAuthCredential())
			if err != nil {
				t.Fatal(err)
			}
			msg, err := p.Generate(context.Background(), nil, nil)
			if err == nil || msg != nil || strings.Contains(err.Error(), "test-secret") || tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatal("unsafe/partial success", err)
			}
		})
	}
}

type interruptedReader struct{ io.Reader }

func (r interruptedReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if errors.Is(err, io.EOF) {
		return n, errors.New("private network detail")
	}
	return n, err
}
func TestStreamNetworkInterruptionCannotComplete(t *testing.T) {
	if _, err := readEvents(interruptedReader{strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("network interruption accepted/leaked")
	}
}
func TestSeparateRequestContracts(t *testing.T) {
	msgs := []schema.Message{{Role: schema.RoleSystem, Content: "instructions"}, {Role: schema.RoleUser, Content: "history one"}, {Role: schema.RoleAssistant, Content: "history two"}}
	for _, oauth := range []bool{false, true} {
		builder := apiKeyRequest
		if oauth {
			builder = oauthRequest
		}
		body, err := builder("model-a", msgs, nil)
		if err != nil || body["store"] != false || body["stream"] != oauth || body["instructions"] != "instructions" || len(body["input"].([]any)) != 2 {
			t.Fatal("invalid request contract", err)
		}
		for _, key := range []string{"previous_response_id", "temperature", "max_output_tokens"} {
			if _, present := body[key]; present {
				t.Fatal("API-only option in plan request")
			}
		}
	}
}
