package feishu

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/access"
	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestFeishuStableOpenIDAuthorizesTasksAndApproval(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "cli_test")
	t.Setenv("FEISHU_APP_SECRET", "test")
	a, err := access.NewAuthorizer(map[access.Scope]access.Policy{{Channel: "feishu", AccountID: "default"}: {
		Mode: access.ModeRestricted, Operators: []string{"ou_Case", "ou_operator"}, Approvers: []string{"ou_Case"}, GroupChats: []string{"chat"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := sessionstore.NewMemoryStore()
	svc := ctxpkg.NewSessionService(store)
	dir := t.TempDir()
	sessions := gateway.DurableSessions{Service: svc, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, dir, provider.ModelInfo{}, "")
	}}
	sent := make(chan string, 10)
	results := make(chan bool, 1)
	var factories, runs atomic.Int32
	b := newTestRuntime(t, dir, sessions.Resolve, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (gateway.AgentRunner, error) {
		factories.Add(1)
		return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
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
	}, approval.NewApprovalManager(), a)
	b.client, b.botOpenID = recordingClient(t, sent), "bot-self"
	setUser := func(e *larkim.P2MessageReceiveV1, id string) *larkim.P2MessageReceiveV1 {
		e.Event.Sender.SenderId = &larkim.UserId{OpenId: &id}
		return e
	}
	for _, event := range []*larkim.P2MessageReceiveV1{
		textEvent("chat", "missing", "missing sender ID"),
		setUser(textEvent("chat", "stranger", "untrusted"), "stranger"),
		setUser(textEvent("chat", "wrong-case", "untrusted"), "ou_case"),
		setUser(groupEvent("unlisted", "wrong-group", "@_user_1 task", botMention("@_user_1", "bot-self", "bot")), "ou_Case"),
	} {
		if err := b.handleMessage(context.Background(), event); err != nil {
			t.Fatal("denied event must be acknowledged", err)
		}
	}
	if list, err := store.List(context.Background()); err != nil || len(list) != 0 || b.gateway.ConversationCount() != 0 || factories.Load() != 0 || runs.Load() != 0 {
		t.Fatal("denied Feishu event created runtime", err)
	}
	select {
	case text := <-sent:
		t.Fatal("denied event sent reply", text)
	default:
	}
	allowed := setUser(groupEvent("chat", "safe", "@_user_1 safe", botMention("@_user_1", "bot-self", "bot")), "ou_Case")
	if err := b.handleMessage(context.Background(), allowed); err != nil {
		t.Fatal(err)
	}
	if text := receive(t, sent); text != "safe done" {
		t.Fatal(text)
	}
	if err := b.handleMessage(context.Background(), setUser(textEvent("chat", "danger", "danger"), "ou_Case")); err != nil {
		t.Fatal(err)
	}
	if notice := receive(t, sent); !strings.Contains(notice, "approve task") {
		t.Fatal("missing approval", notice)
	}
	// Non-approver and missing sender cannot resolve; no @ mention is needed.
	for _, event := range []*larkim.P2MessageReceiveV1{
		setUser(groupEvent("chat", "denied-control", "reject task"), "ou_operator"),
		groupEvent("chat", "missing-control", "reject task"),
		setUser(groupEvent("chat", "right-control", "approve task"), "ou_Case"),
	} {
		if err := b.handleMessage(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if !receive(t, results) || factories.Load() != 1 || runs.Load() != 2 {
		t.Fatal("Feishu approval sender authorization failed")
	}
	b.Close()
	b.workers.Wait()
	snap, err := store.Load(context.Background(), channel.BuildSessionKey(feishuRoute("chat")))
	if err != nil || len(snap.History) != 2 || snap.History[0].Content != "safe" || snap.History[1].Content != "danger" {
		t.Fatal("Feishu control entered prompt/history", err)
	}
}
