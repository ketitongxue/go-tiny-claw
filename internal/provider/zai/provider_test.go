package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func TestZAICompatibility(t *testing.T) {
	d := Definition()
	if d.ID != ID || d.Config.BaseURL != BaseURL || d.EnvVars[1] != "ZHIPU_API_KEY" {
		t.Fatal("ZAI defaults changed")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/coding/paas/v4/chat/completions" || r.Header.Get("Authorization") != "Bearer fake-zai" {
			t.Errorf("wrong request %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "glm-5.3" {
			t.Error("model mapping", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"pwd\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	}))
	defer s.Close()
	d.Config.BaseURL = s.URL + "/api/coding/paas/v4"
	p, err := d.Factory(context.Background(), d.Models[0], auth.APIKeyCredential{Key: "fake-zai"}, d.Config)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "hello"}}, nil)
	if err != nil || len(msg.ToolCalls) != 1 || msg.Usage.PromptTokens != 10 {
		t.Fatal("mapping", err)
	}
}
