package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

const completedTestStream = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]},{\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}]}}\n\n"

func TestOAuthSSEMissingOrMislabeledContentType(t *testing.T) {
	for _, tc := range []struct{ name, media, preamble string }{
		{name: "missing"},
		{name: "empty", media: ""},
		{name: "octet stream", media: "application/octet-stream"},
		{name: "plain text", media: "text/plain; charset=utf-8"},
		{name: "JSON label on real SSE", media: "application/json"},
		{name: "case insensitive", media: "Text/Event-Stream; charset=utf-8"},
		{name: "SSE header with BOM", media: "text/event-stream", preamble: "\xef\xbb\xbf"},
		{name: "heartbeat", preamble: ": heartbeat\n\n"},
		{name: "blank lines and BOM", preamble: "\n\r\n\xef\xbb\xbf"},
		{name: "event field", preamble: "event: response.completed\n"},
		{name: "id and retry fields", preamble: "id: 1\nretry: 1000\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Prevent net/http from synthesizing Content-Type from the body.
				w.Header()["Content-Type"] = nil
				if tc.name != "missing" {
					w.Header().Set("Content-Type", tc.media)
				}
				fmt.Fprint(w, tc.preamble+completedTestStream)
			}))
			defer server.Close()
			p, err := New(Config{SubscriptionEndpoint: server.URL}, provider.ModelInfo{ID: "model-a"}, testOAuthCredential())
			if err != nil {
				t.Fatal(err)
			}
			msg, err := p.Generate(context.Background(), nil, nil)
			if err != nil || msg == nil || msg.Content != "answer" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call-1" {
				t.Fatal("real SSE rejected due to response header", err)
			}
		})
	}
}

func TestOAuthDirectPlanEmptyCompletionSummary(t *testing.T) {
	// Observed public plan-route shape: no Content-Type, finalized output events,
	// and a completed summary containing output:[]. No payload values are logged.
	stream := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","content":[]}}

data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"answer"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"answer"}]}}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call-1","name":"bash","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"pwd\"}"}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call-1","name":"bash","arguments":"{\"command\":\"pwd\"}"}}

data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":15,"output_tokens":4,"input_tokens_details":{"cached_tokens":2}}}}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		fmt.Fprint(w, stream)
	}))
	defer server.Close()
	p, err := New(Config{SubscriptionEndpoint: server.URL}, provider.ModelInfo{ID: "model-a"}, testOAuthCredential())
	if err != nil {
		t.Fatal(err)
	}
	msg, err := p.Generate(context.Background(), nil, nil)
	if err != nil || msg == nil || msg.Content != "answer" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call-1" || string(msg.ToolCalls[0].Arguments) != `{"command":"pwd"}` || msg.Usage.PromptTokens != 15 {
		t.Fatal("finalized output discarded by empty completion summary", err)
	}
	// Even finalized output is not success without a completed terminal event.
	withoutCompletion := strings.Split(stream, `data: {"type":"response.completed"`)[0]
	if _, err := readEvents(strings.NewReader(withoutCompletion)); err == nil {
		t.Fatal("item.done accepted without response.completed")
	}
}

func TestOAuthMissingContentTypeStillRequiresCompleted(t *testing.T) {
	for _, body := range []string{
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		"data: [DONE]\n\n",
		"data: {\"type\":\"response.failed\"}\n\n",
		"data: {\"type\":\"response.incomplete\"}\n\n",
		"data: test-secret\n\n",
		`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"not SSE"}]}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Content-Type"] = nil
			fmt.Fprint(w, body)
		}))
		p, err := New(Config{SubscriptionEndpoint: server.URL}, provider.ModelInfo{ID: "model-a"}, testOAuthCredential())
		if err != nil {
			t.Fatal(err)
		}
		msg, err := p.Generate(context.Background(), nil, nil)
		server.Close()
		if err == nil || msg != nil || strings.Contains(err.Error(), "test-secret") {
			t.Fatal("non-stream/partial result accepted", err)
		}
	}
}

func TestOAuthNonStreamingDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	for _, tc := range []struct{ name, media, body, shape string }{
		{name: "JSON", media: "application/json", body: `{"detail":"test-secret"}`, shape: "json"},
		{name: "HTML", media: "text/html; charset=utf-8", body: "<html>test-secret</html>", shape: "html_or_xml"},
		{name: "empty", shape: "empty"},
		{name: "unknown header", media: "application/test-secret", body: "test-secret", shape: "other"},
		{name: "whitespace limit", body: strings.Repeat(" ", 4097) + completedTestStream, shape: "preamble_too_long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = nil
				if tc.media != "" {
					w.Header().Set("Content-Type", tc.media)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p, err := New(Config{SubscriptionEndpoint: server.URL}, provider.ModelInfo{ID: "model-a"}, testOAuthCredential())
			if err != nil {
				t.Fatal(err)
			}
			msg, err := p.Generate(context.Background(), nil, nil)
			if err == nil || msg != nil || strings.Contains(err.Error(), "test-secret") || !strings.Contains(err.Error(), "HTTP 200") || !strings.Contains(err.Error(), "response_shape="+tc.shape) {
				t.Fatal("unsafe/incomplete non-SSE diagnostic", err)
			}
		})
	}
}

func TestOAuthJSONUsageErrorWithSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		fmt.Fprint(w, `{"error":{"code":"subscription_sharing_usage_unavailable","message":"test-secret"}}`)
	}))
	defer server.Close()
	p, err := New(Config{SubscriptionEndpoint: server.URL}, provider.ModelInfo{ID: "model-a"}, testOAuthCredential())
	if err != nil {
		t.Fatal(err)
	}
	msg, err := p.Generate(context.Background(), nil, nil)
	if err == nil || msg != nil || !strings.Contains(err.Error(), "subscription_sharing_usage_unavailable") || strings.Contains(err.Error(), "test-secret") {
		t.Fatal("subscription admission error lost/leaked", err)
	}
}
