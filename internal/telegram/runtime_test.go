package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/access"
	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

type sentText struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type botAPI struct {
	adapter *Adapter
	updates chan []update
	polls   chan int64
	sent    chan sentText
}

// Only Telegram's HTTP API is replaced; normalization, polling, Gateway,
// ChannelReporter, approvals, AgentEngine and durable sessions are real.
func newBotAPI(t *testing.T) *botAPI {
	t.Helper()
	api := &botAPI{updates: make(chan []update, 8), polls: make(chan int64, 32), sent: make(chan sentText, 64)}
	api.adapter = httpAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			apiResult(w, testBot)
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			apiResult(w, webhookInfo{})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			var req struct {
				Offset int64 `json:"offset"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			api.polls <- req.Offset
			select {
			case batch := <-api.updates:
				apiResult(w, batch)
			case <-r.Context().Done():
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var req map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req) != 2 {
				t.Error("send must contain only chat_id and text", err)
			}
			var sent sentText
			_ = json.Unmarshal(req["chat_id"], &sent.ChatID)
			_ = json.Unmarshal(req["text"], &sent.Text)
			api.sent <- sent
			apiResult(w, message{MessageID: 1})
		default:
			t.Error("unexpected Telegram API method")
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	return api
}

func (api *botAPI) publish(t *testing.T, batch ...update) {
	t.Helper()
	api.updates <- batch
	// The next poll confirms every event in the previous batch was admitted or
	// deliberately ignored. It does not wait for ordinary agent work to finish.
	await(t, api.polls)
}

type runFunc func(context.Context, *ctxpkg.Session, engine.Reporter) error

func (f runFunc) Run(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
	return f(ctx, s, r)
}

type providerFunc func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f providerFunc) Generate(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, msgs, defs)
}

type runningGateway struct {
	*gateway.Gateway
	done <-chan error
}

func (g *runningGateway) stop(t *testing.T) {
	t.Helper()
	g.Cancel()
	if err := await(t, g.done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func serveGateway(t *testing.T, api *botAPI, store sessionstore.Store, dir string, mode delivery.ReportMode, factory gateway.RuntimeFactory, others ...channel.Adapter) *runningGateway {
	t.Helper()
	return serveGatewayWithAccess(t, api, store, dir, mode, factory, nil, others...)
}

func serveGatewayWithAccess(t *testing.T, api *botAPI, store sessionstore.Store, dir string, mode delivery.ReportMode, factory gateway.RuntimeFactory, authorizer *access.Authorizer, others ...channel.Adapter) *runningGateway {
	t.Helper()
	registry := channel.NewRegistry()
	for _, a := range append([]channel.Adapter{api.adapter}, others...) {
		if err := registry.Register(a); err != nil {
			t.Fatal(err)
		}
	}
	svc := ctxpkg.NewSessionService(store)
	sessions := gateway.DurableSessions{Service: svc, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, dir, provider.ModelInfo{ProviderID: "fake", ID: "pinned"}, "none")
	}}
	g, err := gateway.New(context.Background(), gateway.Config{Channels: registry, ResolveSession: sessions.Resolve,
		Factory: factory, Approvals: approval.NewApprovalManager(), Authorizer: authorizer, ReportingPolicy: delivery.ReportingPolicy{Mode: mode}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := g.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	done := make(chan error, 1)
	go func() { done <- g.Run() }()
	if offset := await(t, api.polls); offset != 0 {
		t.Fatal("new adapter should start with an unconfirmed cursor", offset)
	}
	return &runningGateway{Gateway: g, done: done}
}

func TestTelegramDedupAcrossChatsAndGroupAdmission(t *testing.T) {
	api := newBotAPI(t)
	var runs atomic.Int32
	g := serveGateway(t, api, sessionstore.NewMemoryStore(), t.TempDir(), delivery.ReportModeQuiet,
		func(context.Context, *ctxpkg.Session, channel.Route, engine.Reporter, *approval.Service) (gateway.AgentRunner, error) {
			return runFunc(func(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
				runs.Add(1)
				h := s.GetWorkingMemory(0)
				r.OnMessage(ctx, h[len(h)-1].Content)
				return nil
			}), nil
		})
	api.publish(t, textUpdate(100, 1, -42, "group", "ordinary group chatter"))
	if g.ConversationCount() != 0 {
		t.Fatal("group chatter created a conversation")
	}
	mentioned := textUpdate(101, 1, -42, "group", "@claw_bot group task")
	mentioned.Message.Entities = []messageEntity{{Type: "mention", Length: 9}}
	// Two chats with the same message_id must both run; duplicate update_id must not.
	api.publish(t, mentioned, mentioned, textUpdate(102, 1, 43, "private", "private task"))
	seen := map[string]string{}
	for range 2 {
		out := await(t, api.sent)
		seen[out.ChatID] = out.Text
	}
	if seen["-42"] != "group task" || seen["43"] != "private task" {
		t.Fatal("chat-local IDs collided or mention was not stripped", seen)
	}
	reply := textUpdate(103, 2, -42, "supergroup", "continued")
	reply.Message.ReplyToMessage = &message{From: &testBot}
	api.publish(t, reply)
	if out := await(t, api.sent); out.ChatID != "-42" || out.Text != "continued" {
		t.Fatal("reply-to-bot was not admitted", out)
	}
	if g.ConversationCount() != 2 {
		t.Fatal("group admission/session isolation changed", g.ConversationCount())
	}
	g.stop(t)
	if runs.Load() != 3 || g.ConversationCount() != 0 {
		t.Fatal("duplicate ran or shutdown retained closed runtime", runs.Load(), g.ConversationCount())
	}
}

// This replaces only Feishu transport. Both adapters still run concurrently in
// one Gateway; no Telegram branches are added to the conversation/engine.
type feishuTransport struct {
	started chan struct{}
	sent    chan sentText
}

func (*feishuTransport) ID() string { return "feishu" }
func (a *feishuTransport) Start(ctx context.Context, _ channel.InboundHandler) error {
	close(a.started)
	<-ctx.Done()
	return ctx.Err()
}
func (a *feishuTransport) Send(ctx context.Context, route channel.Route, msg channel.OutboundMessage) error {
	select {
	case a.sent <- sentText{route.ChatID, msg.Text}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestTelegramDurableResumeAndCrossChannelSessionIsolation(t *testing.T) {
	ctx := context.Background()
	dir, sessionDir := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
	for generation := range 2 {
		store, err := sessionstore.NewFileStore(sessionDir)
		if err != nil {
			t.Fatal(err)
		}
		api := newBotAPI(t)
		feishu := &feishuTransport{started: make(chan struct{}), sent: make(chan sentText, 8)}
		g := serveGateway(t, api, store, dir, delivery.ReportModeQuiet,
			func(_ context.Context, s *ctxpkg.Session, route channel.Route, _ engine.Reporter, _ *approval.Service) (gateway.AgentRunner, error) {
				if s.ID != channel.BuildSessionKey(route) || s.ModelRef().String() != "fake/pinned" {
					return nil, fmt.Errorf("incorrect durable session %s", s.ID)
				}
				p := providerFunc(func(_ context.Context, history []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
					code := ""
					for _, m := range history {
						if m.Role == schema.RoleUser && strings.HasPrefix(m.Content, "remember ") {
							code = strings.TrimPrefix(m.Content, "remember ")
						}
					}
					if code == "" {
						return nil, errors.New("remembered value missing after restart")
					}
					return &schema.Message{Role: schema.RoleAssistant, Content: code}, nil
				})
				return engine.NewAgentEngine(p, tools.NewRegistry(), false, false), nil
			}, feishu)
		await(t, feishu.started)
		tgPrompt, feishuPrompt := "remember TG-123", "remember FEISHU-111"
		if generation == 1 {
			tgPrompt, feishuPrompt = "what was the code?", "what was the code?"
		}
		// Deliberately use identical chat IDs across platforms.
		api.publish(t, textUpdate(int64(200+generation), 1, 42, "private", tgPrompt))
		fr := channel.Route{Channel: "feishu", AccountID: "default", ChatID: "42"}
		if err := g.Receive(ctx, channel.InboundMessage{ID: "1", Route: fr, ChatType: channel.ChatDirect, Text: feishuPrompt}); err != nil {
			t.Fatal(err)
		}
		if out := await(t, api.sent); out.Text != "TG-123" || out.ChatID != "42" {
			t.Fatal("Telegram lost/mixed memory", out)
		}
		if out := await(t, feishu.sent); out.Text != "FEISHU-111" || out.ChatID != "42" {
			t.Fatal("Feishu lost/mixed memory", out)
		}
		if g.ConversationCount() != 2 {
			t.Fatal("cross-channel conversations merged")
		}
		g.stop(t)
		if g.ConversationCount() != 0 {
			t.Fatal("shutdown retained closed runtime")
		}
		tr := channel.Route{Channel: "telegram", AccountID: "default", ChatID: "42"}
		for _, route := range []channel.Route{tr, fr} {
			snap, err := store.Load(ctx, channel.BuildSessionKey(route))
			if err != nil || len(snap.History) != (generation+1)*2 {
				t.Fatal("durable history was lost or not committed", err, len(snap.History))
			}
			lock, err := store.AcquireWriter(ctx, snap.Metadata.ID)
			if err != nil {
				t.Fatal("shutdown leaked writer", err)
			}
			_ = lock.Release()
		}
	}
}

type dangerousTestTool struct{ executed chan<- struct{} }

func (dangerousTestTool) Name() string { return "bash" }
func (dangerousTestTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "bash"}
}
func (tool dangerousTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	tool.executed <- struct{}{}
	return "executed", nil
}

func TestTelegramApprovalAndPresentationThroughRealEngine(t *testing.T) {
	for _, mode := range []delivery.ReportMode{delivery.ReportModeQuiet, delivery.ReportModeCompact, delivery.ReportModeVerbose} {
		for _, command := range []string{"approve", "reject"} {
			t.Run(string(mode)+"/"+command, func(t *testing.T) {
				api := newBotAPI(t)
				feishu := &feishuTransport{started: make(chan struct{}), sent: make(chan sentText, 8)}
				executed, decisions, done := make(chan struct{}, 1), make(chan bool, 1), make(chan error, 1)
				g := serveGateway(t, api, sessionstore.NewMemoryStore(), t.TempDir(), mode,
					func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (gateway.AgentRunner, error) {
						registry := tools.NewRegistry()
						registry.Register(dangerousTestTool{executed})
						registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
							allowed, reason := approvals.Check(ctx, call)
							decisions <- allowed
							return allowed, reason
						})
						turn := 0
						p := providerFunc(func(_ context.Context, history []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
							turn++
							if turn == 1 {
								return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "danger", Name: "bash", Arguments: []byte(`{"command":"rm -r disposable"}`)}}}, nil
							}
							last := history[len(history)-1].Content
							if command == "approve" && last != "executed" || command == "reject" && !strings.Contains(last, "人类管理员已拒绝操作") {
								return nil, fmt.Errorf("incorrect approval observation: %s", last)
							}
							return &schema.Message{Role: schema.RoleAssistant, Content: "final"}, nil
						})
						eng := engine.NewAgentEngine(p, registry, false, false)
						return runFunc(func(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
							err := eng.Run(ctx, s, r)
							done <- err
							return err
						}), nil
					}, feishu)
				await(t, feishu.started)
				task := textUpdate(300, 1, -42, "group", "@claw_bot run dangerous command")
				task.Message.Entities = []messageEntity{{Type: "mention", Length: 9}}
				api.publish(t, task)
				var output []string
				for {
					out := await(t, api.sent)
					if out.ChatID != "-42" {
						t.Fatal("approval/telemetry sent to wrong chat", out)
					}
					output = append(output, out.Text)
					if strings.Contains(out.Text, "高危操作审批请求") {
						if !strings.Contains(out.Text, "approve danger") || !strings.Contains(out.Text, "reject danger") {
							t.Fatal("approval controls missing", out.Text)
						}
						break
					}
				}
				api.publish(t, textUpdate(301, 2, -43, "group", "approve danger"))
				wrongChannel := channel.InboundMessage{ID: "1", Route: channel.Route{Channel: "feishu", AccountID: "default", ChatID: "-42"}, ChatType: channel.ChatGroup, Text: "approve danger"}
				if err := g.Receive(context.Background(), wrongChannel); err != nil {
					t.Fatal(err)
				}
				select {
				case <-decisions:
					t.Fatal("wrong chat/channel resolved approval")
				default:
				}
				// Plain group control text bypasses the busy worker without @bot.
				api.publish(t, textUpdate(302, 3, -42, "group", command+" danger"))
				if allowed := await(t, decisions); allowed != (command == "approve") {
					t.Fatal("incorrect approval decision", allowed)
				}
				if err := await(t, done); err != nil {
					t.Fatal(err)
				}
				g.stop(t)
				if len(feishu.sent) != 0 {
					t.Fatal("Telegram approval leaked to Feishu")
				}
				for len(api.sent) > 0 {
					out := <-api.sent
					if out.ChatID != "-42" {
						t.Fatal("reply route changed", out)
					}
					output = append(output, out.Text)
				}
				accepted, telemetry, finals := 0, 0, 0
				for _, text := range output {
					switch {
					case text == "🤖 已收到，正在处理……":
						accepted++
					case text == "final":
						finals++
					case strings.Contains(text, "高危操作审批请求"):
					default:
						telemetry++
					}
				}
				wantAccepted, wantTelemetry := 0, 0
				if mode == delivery.ReportModeCompact {
					wantAccepted = 1
					if output[0] != "🤖 已收到，正在处理……" {
						t.Fatal("approval preceded Accepted")
					}
				}
				if mode == delivery.ReportModeVerbose {
					wantTelemetry = 2 // Tool call + success/rejection result.
				}
				if accepted != wantAccepted || telemetry != wantTelemetry || finals != 1 {
					t.Fatal("presentation policy changed", output)
				}
				if command == "approve" {
					await(t, executed)
				}
				select {
				case <-executed:
					t.Fatal("rejected tool ran or approved tool ran twice")
				default:
				}
			})
		}
	}
}
