package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

const acceptedText = "🤖 已收到，正在处理……"

func assertNoDelivery(t *testing.T, a *fakeAdapter) {
	t.Helper()
	select {
	case out := <-a.sent:
		t.Fatalf("unexpected delivery: %+v", out)
	default:
	}
}

func TestAcceptedOnlyForNewAdmittedTasks(t *testing.T) {
	runs := make(chan string, 2)
	g, a, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(_ context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
			runs <- s.ID
			return nil
		}), nil
	})
	msg := inbound("a", "normal", "hello")
	admit(t, g, msg)
	if out := receive(t, a.sent); out.route != msg.Route || out.msg != (channel.OutboundMessage{Kind: channel.OutboundText, Text: acceptedText}) {
		t.Fatalf("wrong accepted notification: %+v", out)
	}
	receive(t, runs)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Receive(context.Background(), msg); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	group := inbound("group", "not-mentioned", "ordinary chatter")
	group.ChatType = channel.ChatGroup
	admit(t, g, group)
	for _, text := range []string{"approve task", "reject task", "approve", "reject too many fields", "   "} {
		control := inbound("control", "control-"+text, text)
		control.ChatType = channel.ChatGroup
		admit(t, g, control)
	}
	assertNoDelivery(t, a)
	if g.ConversationCount() != 1 {
		t.Fatal("ignored message or control command created a conversation")
	}
	group.ID = "mentioned"
	group.MentionedBot = true
	admit(t, g, group)
	if out := receive(t, a.sent); out.route != group.Route || out.msg.Text != acceptedText {
		t.Fatalf("mentioned task missing accepted: %+v", out)
	}
	receive(t, runs)
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	assertNoDelivery(t, a)
	select {
	case id := <-runs:
		t.Fatalf("duplicate/ignored task executed: %s", id)
	default:
	}
}

func TestSlowAcceptedDoesNotHoldAdmissionLock(t *testing.T) {
	runs := make(chan string, 2)
	g, a, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, route channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, r engine.Reporter) error {
			runs <- route.ChatID
			r.OnMessage(ctx, "final:"+route.ChatID)
			return nil
		}), nil
	})
	entered, release, ackFinished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	a.sendHook = func(ctx context.Context, route channel.Route, msg channel.OutboundMessage) error {
		if route.ChatID == "a" && msg.Text == "final:a" {
			select {
			case <-ackFinished:
			default:
				t.Error("final raced ahead of the Accepted send attempt")
			}
		}
		if route.ChatID == "a" && msg.Text == acceptedText {
			defer close(ackFinished)
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	msg := inbound("a", "a1", "slow accepted delivery")
	aReturned := make(chan error, 1)
	callback, cancelCallback := context.WithCancel(context.Background())
	defer cancelCallback()
	go func() { aReturned <- g.Receive(callback, msg) }()
	receive(t, entered)
	// Send can start only after successful enqueue, and the task can execute
	// while its best-effort acknowledgment is still blocked on the platform.
	if got := receive(t, runs); got != "a" {
		t.Fatal("notification ran before task was enqueued", got)
	}
	duplicateReturned := make(chan error, 1)
	go func() { duplicateReturned <- g.Receive(context.Background(), msg) }()
	if err := receive(t, duplicateReturned); err != nil {
		t.Fatal(err)
	}
	bReturned := make(chan error, 1)
	go func() { bReturned <- g.Receive(context.Background(), inbound("b", "b1", "fast admission")) }()
	if got := receive(t, runs); got != "b" {
		t.Fatal("chat B was not independently admitted", got)
	}
	if err := receive(t, bReturned); err != nil {
		t.Fatal(err)
	}
	out := receive(t, a.sent)
	if out.route.ChatID != "b" || out.msg.Text != acceptedText {
		t.Fatal("chat B notification blocked by chat A", out)
	}
	if out := receive(t, a.sent); out.route.ChatID != "b" || out.msg.Text != "final:b" {
		t.Fatal("chat B final blocked by chat A", out)
	}
	select {
	case err := <-aReturned:
		t.Fatal("slow notification unexpectedly completed", err)
	default:
	}
	// Cancel the callback, not the accepted work. Delivery failure is logged,
	// Receive still succeeds, and no platform retry is requested.
	cancelCallback()
	if err := receive(t, aReturned); err != nil {
		t.Fatal(err)
	}
	if out := receive(t, a.sent); out.route.ChatID != "a" || out.msg.Text != "final:a" {
		t.Fatal("failed Accepted suppressed the final reply", out)
	}
	assertNoDelivery(t, a)
}

func TestAcceptedSendFailureIsBestEffortAndStillDeduplicated(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	runs := make(chan struct{}, 1)
	g, a, store := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, r engine.Reporter) error {
			r.OnMessage(ctx, "final")
			runs <- struct{}{}
			return nil
		}), nil
	})
	var attempts atomic.Int32
	a.sendHook = func(_ context.Context, _ channel.Route, msg channel.OutboundMessage) error {
		if msg.Text == acceptedText {
			attempts.Add(1)
			return errors.New("accepted API unavailable")
		}
		return nil
	}
	msg := inbound("a", "1", "run once")
	admit(t, g, msg)
	receive(t, runs)
	admit(t, g, msg)
	if out := receive(t, a.sent); out.msg.Text != "final" {
		t.Fatal(out)
	}
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 1 || !strings.Contains(logs.String(), "发送任务接收通知失败") {
		t.Fatalf("accepted failure/retry handling: attempts=%d logs=%s", attempts.Load(), logs.String())
	}
	snap, err := store.Load(context.Background(), channel.BuildSessionKey(msg.Route))
	if err != nil || len(snap.History) != 1 || snap.History[0].Content != msg.Text {
		t.Fatalf("accepted task lost or duplicated: %+v, %v", snap, err)
	}
	assertNoDelivery(t, a)
}

type presentationProviderFunc func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f presentationProviderFunc) Generate(ctx context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, msgs, defs)
}

type presentationTool struct{}

func (presentationTool) Name() string { return "probe" }
func (presentationTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "probe"}
}
func (presentationTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	if strings.Contains(string(args), "fail") {
		return "private-stderr: command exit code 1", errors.New("exit code 1")
	}
	return "private-stdout", nil
}

func TestMessagingPresentationWithRealEngineRecoverySessionAndTrace(t *testing.T) {
	for _, mode := range []delivery.ReportMode{"", delivery.ReportModeQuiet, delivery.ReportModeCompact, delivery.ReportModeVerbose} {
		for _, fatal := range []bool{false, true} {
			name := string(mode) + "/success"
			if fatal {
				name = string(mode) + "/fatal"
			}
			t.Run(name, func(t *testing.T) {
				done := make(chan error, 1)
				sessions := make(chan *ctxpkg.Session, 1)
				g, a, store := testGateway(t, func(_ context.Context, s *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
					sessions <- s
					registry := tools.NewRegistry()
					registry.Register(presentationTool{})
					turn := 0
					p := presentationProviderFunc(func(_ context.Context, msgs []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
						if len(defs) == 0 {
							return &schema.Message{Role: schema.RoleAssistant, Content: "private-thinking"}, nil
						}
						turn++
						switch turn {
						case 1:
							return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "fail", Name: "probe", Arguments: []byte(`{"step":"fail","path":"private-path"}`)}}}, nil
						case 2:
							if !strings.Contains(msgs[len(msgs)-2].Content, "private-stderr") { // last is ephemeral Thinking
								return nil, errors.New("suppression removed failed tool observation")
							}
							return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "ok", Name: "probe", Arguments: []byte(`{"step":"ok","path":"private-path"}`)}}}, nil
						default:
							if !strings.Contains(msgs[len(msgs)-2].Content, "private-stdout") {
								return nil, errors.New("suppression removed successful tool observation")
							}
							if fatal {
								return nil, errors.New("task provider unavailable")
							}
							return &schema.Message{Role: schema.RoleAssistant, Content: "最终回答"}, nil
						}
					})
					eng := engine.NewAgentEngine(p, registry, true, false)
					return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
						err := eng.Run(ctx, s, r)
						done <- err
						return err
					}), nil
				}, delivery.ReportingPolicy{Mode: mode})
				msg := inbound("a", "1", "recover then finish")
				admit(t, g, msg)
				sess := receive(t, sessions)
				wantKinds := map[channel.OutboundKind]int{channel.OutboundText: 1}
				if fatal {
					wantKinds = map[channel.OutboundKind]int{channel.OutboundError: 1}
				}
				if mode == "" || mode == delivery.ReportModeCompact {
					wantKinds[channel.OutboundText]++
				}
				if mode == delivery.ReportModeVerbose {
					wantKinds[channel.OutboundThinking] = 3
					wantKinds[channel.OutboundToolStart] = 2
					wantKinds[channel.OutboundToolResult] = 2
				}
				count := 0
				for _, n := range wantKinds {
					count += n
				}
				gotKinds := map[channel.OutboundKind]int{}
				for i := range count {
					out := receive(t, a.sent)
					if i == 0 && (mode == "" || mode == delivery.ReportModeCompact) && out.msg.Text != acceptedText {
						t.Fatalf("task output preceded Accepted: %+v", out)
					}
					gotKinds[out.msg.Kind]++
					if out.route != msg.Route {
						t.Fatal("wrong destination", out)
					}
					if mode != delivery.ReportModeVerbose && strings.Contains(out.msg.Text, "private-") {
						t.Fatalf("internal telemetry leaked to Adapter.Send: %+v", out)
					}
					if out.msg.Kind == channel.OutboundError && (!fatal || !strings.Contains(out.msg.Text, "❌ Agent 运行失败: Action 阶段失败: task provider unavailable")) {
						t.Fatalf("tool error treated as fatal or task error lost: %+v", out)
					}
				}
				if !reflect.DeepEqual(gotKinds, wantKinds) {
					t.Fatalf("got kinds %v; want %v", gotKinds, wantKinds)
				}
				if err := receive(t, done); (err != nil) != fatal {
					t.Fatalf("runner result changed: %v", err)
				}
				if err := g.Shutdown(); err != nil {
					t.Fatal(err)
				}
				assertNoDelivery(t, a)
				snap, err := store.Load(context.Background(), sess.ID)
				wantHistory := 6
				if fatal {
					wantHistory--
				}
				if err != nil || len(snap.History) != wantHistory || !strings.Contains(snap.History[2].Content, "private-stderr") || snap.History[4].Content != "private-stdout" {
					t.Fatalf("suppressed events changed session history: %+v, %v", snap, err)
				}
				paths, err := filepath.Glob(filepath.Join(sess.WorkDir, ".claw", "traces", "*.json"))
				if err != nil || len(paths) != 1 {
					t.Fatal("missing trace", paths, err)
				}
				data, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				var trace observability.Span
				if err := json.Unmarshal(data, &trace); err != nil {
					t.Fatal(err)
				}
				toolSpans := 0
				var visit func(*observability.Span)
				visit = func(span *observability.Span) {
					if span.Name == "Tool.Execute" {
						toolSpans++
						if span.Attributes["arguments"] == nil || span.Attributes["output_preview"] == nil {
							t.Fatal("suppression removed tool trace attributes")
						}
					}
					for _, child := range span.Children {
						visit(child)
					}
				}
				visit(&trace)
				if toolSpans != 2 || !bytes.Contains(data, []byte("private-stderr")) || !bytes.Contains(data, []byte("private-stdout")) {
					t.Fatal("suppression removed tool traces")
				}
			})
		}
	}
}
