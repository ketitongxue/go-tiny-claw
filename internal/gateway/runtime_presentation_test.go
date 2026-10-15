package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func TestRuntimeFailuresRemainVisibleInEveryMode(t *testing.T) {
	for _, mode := range []delivery.ReportMode{delivery.ReportModeQuiet, delivery.ReportModeCompact, delivery.ReportModeVerbose} {
		for _, failure := range []string{"restore", "factory", "persistence"} {
			t.Run(string(mode)+"/"+failure, func(t *testing.T) {
				g, a, _ := testGateway(t, func(_ context.Context, s *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
					if failure == "factory" {
						return nil, errors.New("model unavailable")
					}
					if failure == "persistence" {
						if err := s.Close(); err != nil {
							return nil, err
						}
					}
					return runnerFunc(func(context.Context, *ctxpkg.Session, engine.Reporter) error {
						t.Error("runner invoked despite runtime setup failure")
						return nil
					}), nil
				}, delivery.ReportingPolicy{Mode: mode})
				want := "❌ Session 保存失败"
				if failure == "factory" {
					want = "❌ 模型不可用"
				}
				if failure == "restore" {
					want = "❌ Session 恢复失败"
					g.conversations.resolve = func(context.Context, channel.Route) (*ctxpkg.Session, error) {
						return nil, errors.New("session unavailable")
					}
				}
				admit(t, g, inbound("a", "1", "fail setup"))
				count := 1
				if mode == delivery.ReportModeCompact {
					count++
				}
				accepted, taskErrors := 0, 0
				for i := range count {
					out := receive(t, a.sent)
					if i == 0 && mode == delivery.ReportModeCompact && out.msg.Text != acceptedText {
						t.Fatalf("runtime error preceded Accepted: %+v", out)
					}
					switch {
					case out.msg.Kind == channel.OutboundError && out.msg.IsError && strings.HasPrefix(out.msg.Text, want):
						taskErrors++
					case mode == delivery.ReportModeCompact && out.msg.Kind == channel.OutboundText && out.msg.Text == acceptedText:
						accepted++
					default:
						t.Fatalf("unexpected runtime output: %+v", out)
					}
				}
				if taskErrors != 1 || mode == delivery.ReportModeCompact && accepted != 1 {
					t.Fatalf("missing runtime output: accepted=%d errors=%d", accepted, taskErrors)
				}
				if err := g.Shutdown(); err != nil {
					t.Fatal(err)
				}
				assertNoDelivery(t, a)
			})
		}
	}
}

func TestApprovalAndControlRepliesInEveryMode(t *testing.T) {
	for _, mode := range []delivery.ReportMode{delivery.ReportModeQuiet, delivery.ReportModeCompact, delivery.ReportModeVerbose} {
		for _, command := range []string{"approve", "reject"} {
			t.Run(string(mode)+"/"+command, func(t *testing.T) {
				results := make(chan bool, 1)
				g, a, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (AgentRunner, error) {
					return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, _ engine.Reporter) error {
						allowed, _ := approvals.Check(ctx, schema.ToolCall{ID: "danger", Name: "bash", Arguments: []byte(`{"command":"rm -r disposable"}`)})
						results <- allowed
						return nil
					}), nil
				}, delivery.ReportingPolicy{Mode: mode})
				admit(t, g, inbound("a", "1", "danger"))
				count := 1
				if mode == delivery.ReportModeCompact {
					count++
				}
				approvals := 0
				for i := range count {
					out := receive(t, a.sent)
					if i == 0 && mode == delivery.ReportModeCompact && out.msg.Text != acceptedText {
						t.Fatalf("approval preceded Accepted: %+v", out)
					}
					if out.msg.Kind != channel.OutboundText {
						t.Fatal(out)
					}
					if strings.Contains(out.msg.Text, "高危操作审批请求") && strings.Contains(out.msg.Text, "approve danger") && strings.Contains(out.msg.Text, "reject danger") {
						approvals++
					} else if mode != delivery.ReportModeCompact || out.msg.Text != acceptedText {
						t.Fatal("unexpected approval output", out)
					}
				}
				if approvals != 1 {
					t.Fatal("approval suppressed/duplicated", approvals)
				}
				reply := inbound("a", "2", command+" danger")
				reply.ChatType = channel.ChatGroup // Control replies need no mention.
				admit(t, g, reply)
				if got := receive(t, results); got != (command == "approve") {
					t.Fatal("approval decision changed", got)
				}
				if err := g.Shutdown(); err != nil {
					t.Fatal(err)
				}
				assertNoDelivery(t, a) // No accepted notification for the control reply.
			})
		}
	}
}
