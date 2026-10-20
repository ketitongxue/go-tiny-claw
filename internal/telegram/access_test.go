package telegram

import (
	"context"
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
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestTelegramNumericUserIDAuthorizesTasksAndApproval(t *testing.T) {
	api := newBotAPI(t)
	a, err := access.NewAuthorizer(map[access.Scope]access.Policy{{Channel: "telegram", AccountID: "default"}: {
		Mode: access.ModeRestricted, Operators: []string{"100", "200"}, Approvers: []string{"100"}, GroupChats: []string{"-1001"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := sessionstore.NewMemoryStore()
	results := make(chan bool, 1)
	var factories, runs atomic.Int32
	g := serveGatewayWithAccess(t, api, store, t.TempDir(), delivery.ReportModeCompact,
		func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (gateway.AgentRunner, error) {
			factories.Add(1)
			return runFunc(func(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
				runs.Add(1)
				h := s.GetWorkingMemory(0)
				if h[len(h)-1].Content == "danger" {
					allowed, _ := approvals.Check(ctx, schema.ToolCall{ID: "task", Name: "bash", Arguments: []byte(`{"command":"sudo ls"}`)})
					results <- allowed
				} else {
					r.OnMessage(ctx, "safe done")
				}
				return nil
			}), nil
		}, a)
	from := func(u update, id int64) update {
		u.Message.From.ID, u.Message.From.Username = id, "100" // A matching username grants no rights.
		return u
	}
	mentioned := func(u update) update {
		u.Message.Entities = []messageEntity{{Type: "mention", Length: 9}}
		return u
	}
	api.publish(t,
		from(textUpdate(1, 1, 43, "private", "untrusted"), 300),
		from(mentioned(textUpdate(2, 1, -1001, "group", "@claw_bot untrusted")), 300),
		from(mentioned(textUpdate(3, 1, -1002, "group", "@claw_bot unlisted group")), 100))
	if list, err := store.List(context.Background()); err != nil || len(list) != 0 || g.ConversationCount() != 0 || factories.Load() != 0 || runs.Load() != 0 {
		t.Fatal("denied Telegram update created runtime", err)
	}
	select {
	case out := <-api.sent:
		t.Fatal("denied update sent reply", out)
	default:
	}
	api.publish(t, from(mentioned(textUpdate(4, 2, -1001, "group", "@claw_bot safe")), 100))
	for _, want := range []string{"🤖 已收到，正在处理……", "safe done"} {
		if out := await(t, api.sent); out.Text != want {
			t.Fatal(out)
		}
	}
	api.publish(t, from(mentioned(textUpdate(5, 3, -1001, "group", "@claw_bot danger")), 100))
	if out := await(t, api.sent); out.Text != "🤖 已收到，正在处理……" {
		t.Fatal("missing Accepted", out)
	}
	if out := await(t, api.sent); !strings.Contains(out.Text, "approve task") {
		t.Fatal("missing approval", out)
	}
	api.publish(t, from(textUpdate(6, 4, -1001, "group", "reject task"), 200))
	if runs.Load() != 2 || g.ConversationCount() != 1 {
		t.Fatal("non-approver control entered FIFO")
	}
	api.publish(t, from(textUpdate(7, 5, -1001, "group", "approve task"), 100))
	if !await(t, results) || factories.Load() != 1 {
		t.Fatal("Telegram numeric sender authorization failed")
	}
	g.stop(t)
	route := channel.Route{Channel: "telegram", AccountID: "default", ChatID: "-1001"}
	snap, err := store.Load(context.Background(), channel.BuildSessionKey(route))
	if err != nil || len(snap.History) != 2 || snap.History[0].Content != "safe" || snap.History[1].Content != "danger" {
		t.Fatal("Telegram control entered prompt/history", err)
	}
	select {
	case out := <-api.sent:
		t.Fatal("control sent Accepted/reply", out)
	default:
	}
}
