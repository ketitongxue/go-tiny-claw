package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestDemoCompatibleRestartResumePinning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	paths, _ := config.NewPaths(home)
	dir := t.TempDir()
	var mu sync.Mutex
	requested := []string{}
	histories := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body struct {
			Model    string          `json:"model"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requested = append(requested, body.Model)
		histories = append(histories, string(body.Messages))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"remembered"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	}))
	defer server.Close()
	models := config.Models{Providers: map[string]config.CustomProvider{"fake": {API: "openai-compatible", BaseURL: server.URL, Auth: config.CustomAuth{Type: "none"}, Models: []provider.ModelInfo{{ID: "a", ToolCalling: true}, {ID: "b", ToolCalling: true}}}}}
	if err := fileutil.WriteJSON(paths.Models, models); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(paths.Config, config.Config{DefaultModel: "fake/a"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ctx := context.Background()
	if err := run(ctx, []string{"-mode", "demo", "-sandbox", "local", "-workdir", dir, "-prompt", "original task"}, &out); err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.NewFileStore(paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(err)
	}
	id := list[0].ID
	if err := config.Save(paths.Config, config.Config{DefaultModel: "fake/b"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"-mode", "demo", "-sandbox", "local", "-resume", id, "-prompt", "continue"}, &out); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if requested[0] != "a" || requested[1] != "a" || !strings.Contains(histories[1], "original task") || !strings.Contains(histories[1], "remembered") {
		t.Fatalf("pinning/history failed %v %v", requested, histories)
	}
	mu.Unlock()
	if err := run(ctx, []string{"-mode", "demo", "-sandbox", "local", "-workdir", dir, "-prompt", "new task"}, &out); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := requested[2]
	mu.Unlock()
	if last != "b" {
		t.Fatal("new default ignored")
	}
	list, err = store.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID == list[1].ID {
		t.Fatal("demo reused ID")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"-mode", "demo", "-sandbox", "local", "-resume", id, "-prompt", "continue"}, &out); err == nil {
		t.Fatal("missing workdir accepted")
	}
}
