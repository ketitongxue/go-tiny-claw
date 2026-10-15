package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func recordingClient(t *testing.T, sent chan<- string) *lark.Client {
	t.Helper()
	client := &http.Client{Transport: botRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
		} else {
			var body struct {
				ReceiveID string `json:"receive_id"`
				Content   string `json:"content"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.ReceiveID != "chat" {
				t.Errorf("wrong approval/progress destination: %q", body.ReceiveID)
			}
			var content struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Error(err)
			}
			sent <- content.Text
			fmt.Fprint(w, `{"code":0,"data":{"message_id":"out"}}`)
		}
		return w.Result(), nil
	})}
	return lark.NewClient("regression-"+t.Name(), "secret", lark.WithHttpClient(client), lark.WithEnableTokenCache(false))
}

type approvedTestTool struct{ executed chan<- struct{} }

func (approvedTestTool) Name() string { return "bash" }
func (approvedTestTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "bash"}
}
func (tool approvedTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	tool.executed <- struct{}{}
	return "executed", nil
}

func TestFeishuApprovalAndRejectThroughGatewayEngineAndDelivery(t *testing.T) {
	for _, command := range []string{"approve", "reject"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("FEISHU_APP_ID", "cli_test")
			t.Setenv("FEISHU_APP_SECRET", "test")
			dir := t.TempDir()
			svc := ctxpkg.NewSessionService(sessionstore.NewMemoryStore())
			sessions := gateway.DurableSessions{Service: svc, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
				return svc.Create(ctx, id, dir, provider.ModelInfo{}, "")
			}}
			sent := make(chan string, 10)
			executed := make(chan struct{}, 1)
			done := make(chan error, 1)
			b := newTestRuntime(t, dir, sessions.Resolve, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (gateway.AgentRunner, error) {
				registry := tools.NewRegistry()
				registry.Register(approvedTestTool{executed})
				registry.Use(approvals.Check)
				turn := 0
				p := resumeProvider(func(_ context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
					turn++
					if turn == 1 {
						return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "task123", Name: "bash", Arguments: []byte(`{"command":"rm -r target"}`)}}}, nil
					}
					last := msgs[len(msgs)-1]
					if command == "approve" && last.Content != "executed" {
						return nil, fmt.Errorf("approved tool not executed: %+v", last)
					}
					if command == "reject" && !strings.Contains(last.Content, "人类管理员已拒绝操作") {
						return nil, fmt.Errorf("missing rejection: %+v", last)
					}
					return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
				})
				eng := engine.NewAgentEngine(p, registry, false, false)
				return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
					err := eng.Run(ctx, s, r)
					done <- err
					return err
				}), nil
			}, approval.NewApprovalManager())
			b.client = recordingClient(t, sent)
			if err := b.handleMessage(context.Background(), textEvent("chat", "1", "danger")); err != nil {
				t.Fatal(err)
			}
			for {
				text := receive(t, sent)
				if strings.Contains(text, "高危操作审批请求") {
					if !strings.Contains(text, "approve task123") || !strings.Contains(text, "reject task123") {
						t.Fatal("notice rendering changed", text)
					}
					break
				}
			}
			// Plain group replies must bypass the busy worker and need no bot identity.
			if err := b.handleMessage(context.Background(), groupEvent("chat", "2", command+" task123")); err != nil {
				t.Fatal(err)
			}
			if err := receive(t, done); err != nil {
				t.Fatal(err)
			}
			if command == "approve" {
				receive(t, executed)
			} else {
				select {
				case <-executed:
					t.Fatal("rejected tool executed")
				default:
				}
			}
		})
	}
}

func TestFeishuChannelReporterProgressRegression(t *testing.T) {
	sent := make(chan string, 8)
	done := make(chan struct{}, 1)
	b := testBot(t, func(*ctxpkg.Session, engine.Reporter) agentRunner {
		return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, r engine.Reporter) error {
			r.OnThinking(ctx)
			r.OnToolCall(ctx, "bash", "echo hi")
			r.OnToolResult(ctx, "bash", "hidden success output", false)
			r.OnToolResult(ctx, "bash", "failure", true)
			r.OnMessage(ctx, "final")
			done <- struct{}{}
			return nil
		})
	})
	b.client = recordingClient(t, sent)
	if err := b.handleMessage(context.Background(), textEvent("chat", "1", "run")); err != nil {
		t.Fatal(err)
	}
	receive(t, done)
	for _, want := range []string{"🤔 模型正在慢思考 (Thinking)...", "🛠️ **正在执行工具**：`bash`\n参数：`echo hi`", "✅ **执行成功** (bash)", "⚠️ **执行报错** (bash)：\nfailure", "final"} {
		if got := receive(t, sent); got != want {
			t.Fatalf("progress rendering changed: %q, want %q", got, want)
		}
	}
}
