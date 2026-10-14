package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Exercises the real dispatcher/config/auth/manager/adapter/engine/trace path,
// without a paid model request or external OAuth, and with explicit local mode.
func TestCustomProviderDefaultSurvivesRestartAndHidesCredentials(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("GATEWAY_TEST_KEY", "private-gateway-credential")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-gateway-credential" {
			t.Error("endpoint/auth")
		}
		var body struct {
			Model    string
			Messages []map[string]any
			Tools    []map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model != "coder" || len(body.Tools) != 5 {
			t.Error("saved model/tool definitions not used")
		}
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), "private-gateway-credential") {
			t.Error("credential in prompt/tools")
		}
		call := calls.Add(1)
		if call == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"write1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"result.txt\",\"content\":\"safe\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
		} else {
			last := body.Messages[len(body.Messages)-1]
			if last["role"] != "tool" || last["tool_call_id"] != "write1" {
				t.Error("tool result history not preserved")
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":20,"completion_tokens":3}}`)
		}
	}))
	defer server.Close()
	models := fmt.Sprintf(`{"providers":{"gateway":{"name":"Gateway","api":"openai-compatible","base_url":%q,"auth":{"type":"api_key","env":"GATEWAY_TEST_KEY"},"models":[{"id":"coder","tool_calling":true}]}}}`, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "models.json"), []byte(models), 0600); err != nil {
		t.Fatal(err)
	}
	var output, logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	ctx := context.Background()
	if err := run(ctx, []string{"auth", "login", "gateway", "--from-env"}, &output); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"model", "set", "gateway/coder"}, &output); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_TEST_KEY", "") // restarted runtime must use its global store.
	if err := run(ctx, []string{"-mode=demo", "-sandbox=local", "-workdir=" + workspace, "-prompt=write result"}, &output); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
	result, err := os.ReadFile(filepath.Join(workspace, "result.txt"))
	if err != nil || string(result) != "safe" {
		t.Fatal("tool execution", err)
	}
	if strings.Contains(output.String()+logs.String(), "private-gateway-credential") {
		t.Fatal("credential in logs")
	}
	if !strings.Contains(logs.String(), "Cost: unknown") || strings.Contains(logs.String(), "¥0.000000") {
		t.Fatal("unknown billing displayed as free")
	}
	traces, _ := filepath.Glob(filepath.Join(workspace, ".claw", "traces", "*.json"))
	if len(traces) != 1 {
		t.Fatal("missing trace")
	}
	data, _ := os.ReadFile(traces[0])
	if strings.Contains(string(data), "private-gateway-credential") || !strings.Contains(string(data), `"provider_id": "gateway"`) {
		t.Fatal("unsafe/missing trace metadata")
	}
	if _, err := os.Stat(filepath.Join(workspace, ".claw", "auth.json")); !os.IsNotExist(err) {
		t.Fatal("workspace credential file created")
	}
}
func TestFallbackNeverSilentlySwitchesProvider(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	t.Setenv("ZAI_CODING_CN_API_KEY", "")
	t.Setenv("ZHIPU_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "another-provider-key")
	var out bytes.Buffer
	err := run(context.Background(), []string{"-mode=demo", "-check-config", "-workdir=" + t.TempDir()}, &out)
	if err == nil || !strings.Contains(err.Error(), "zai-coding-cn/glm-5.3") || strings.Contains(err.Error(), "another-provider-key") {
		t.Fatal("silently routed or leaked key", err)
	}
}
func TestOpenAISelectionDoesNotRequireZAIOrDockerForCheck(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	t.Setenv("ZAI_CODING_CN_API_KEY", "")
	t.Setenv("ZHIPU_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-mode=demo", "-model=openai/gpt-5.2", "-check-config", "-workdir=" + t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Provider: openai") || !strings.Contains(out.String(), "env:OPENAI_API_KEY") || strings.Contains(out.String(), "openai-secret") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CLAW_HOME"), "host.json")); !os.IsNotExist(err) {
		t.Fatal("API-key check unexpectedly required/wrote OAuth host metadata")
	}
}
