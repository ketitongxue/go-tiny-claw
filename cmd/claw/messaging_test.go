package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

func TestInvalidMessagingModeFailsStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("FEISHU_APP_ID", "offline-app")
	t.Setenv("FEISHU_APP_SECRET", "offline-secret")
	t.Setenv("PATH", t.TempDir()) // A config error must precede any Docker probe.
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"messaging":{"report_mode":"comapct"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"feishu", "demo"} {
		dir := filepath.Join(t.TempDir(), "not-created")
		err := run(context.Background(), []string{"-mode=" + mode, "-workdir=" + dir}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "messaging.report_mode") || !strings.Contains(err.Error(), "comapct") {
			t.Fatalf("invalid messaging configuration did not stop %s startup: %v", mode, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("invalid config created a workspace: %v", err)
		}
	}
}

// Run the real demo entry point in a subprocess to capture TerminalReporter's
// stdout without replacing process-global streams in the test runner.
func TestMessagingDemoProcessHelper(t *testing.T) {
	if os.Getenv("CLAW_MESSAGING_DEMO_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx, []string{"-mode=demo", "-sandbox=local", "-thinking", "-workdir=" + os.Getenv("CLAW_MESSAGING_DEMO_WORKDIR"), "-prompt=查看当前目录并告诉我结果"}, os.Stdout); err != nil {
		t.Fatal(err)
	}
}

func TestMessagingModesDoNotFilterDemoTerminalReporter(t *testing.T) {
	for _, mode := range []string{"quiet", "compact", "verbose"} {
		t.Run(mode, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "known.txt"), []byte("evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			var actions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Tools []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if len(body.Tools) == 0 {
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"thinking"}}]}`)
					return
				}
				switch actions.Add(1) {
				case 1:
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"missing","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"missing.txt\"}"}}]}}]}`)
				case 2:
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"known","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"known.txt\"}"}}]}}]}`)
				default:
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"terminal-final"}}]}`)
				}
			}))
			defer server.Close()
			paths, err := config.NewPaths(home)
			if err != nil {
				t.Fatal(err)
			}
			models := config.Models{Providers: map[string]config.CustomProvider{"fake": {
				API: "openai-compatible", BaseURL: server.URL, Auth: config.CustomAuth{Type: "none"},
				Models: []provider.ModelInfo{{ID: "demo", ToolCalling: true}},
			}}}
			if err := fileutil.WriteJSON(paths.Models, models); err != nil {
				t.Fatal(err)
			}
			if err := config.Save(paths.Config, config.Config{DefaultModel: "fake/demo", Messaging: config.MessagingConfig{ReportMode: mode}}); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestMessagingDemoProcessHelper$")
			cmd.Env = append(os.Environ(), "CLAW_HOME="+home, "CLAW_MESSAGING_DEMO_HELPER=1", "CLAW_MESSAGING_DEMO_WORKDIR="+dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("demo failed: %v\n%s", err, out)
			}
			for _, text := range []string{"[🤔 思考中]", "[🛠️ 调用工具] read_file", "[❌ 执行失败] read_file", "[✅ 执行成功] read_file", "🤖 Agent 回复:", "terminal-final"} {
				if !strings.Contains(string(out), text) {
					t.Fatalf("messaging mode %s filtered terminal output %q:\n%s", mode, text, out)
				}
			}
			if actions.Load() != 3 || strings.Contains(string(out), "🤖 已收到，正在处理……") {
				t.Fatalf("messaging presentation changed demo execution: actions=%d\n%s", actions.Load(), out)
			}
		})
	}
}
