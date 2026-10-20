package gateway

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/access"
	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

type accessStore struct {
	sessionstore.Store
	writers atomic.Int32
}

func (s *accessStore) AcquireWriter(ctx context.Context, id string) (sessionstore.SessionLock, error) {
	s.writers.Add(1)
	return s.Store.AcquireWriter(ctx, id)
}

type accessFixture struct {
	g         *Gateway
	adapters  map[string]*fakeAdapter
	store     *accessStore
	resolves  atomic.Int32
	factories atomic.Int32
	runs      atomic.Int32
	started   chan *ctxpkg.Session
}

func newAccessFixture(t *testing.T, policies map[access.Scope]access.Policy, timeout time.Duration, factory RuntimeFactory) *accessFixture {
	t.Helper()
	a, err := access.NewAuthorizer(policies)
	if err != nil {
		t.Fatal(err)
	}
	f := &accessFixture{adapters: make(map[string]*fakeAdapter), store: &accessStore{Store: sessionstore.NewMemoryStore()}, started: make(chan *ctxpkg.Session, 100)}
	registry := channel.NewRegistry()
	for _, name := range []string{"telegram", "feishu"} {
		adapter := newFakeAdapter(name)
		f.adapters[name] = adapter
		if err := registry.Register(adapter); err != nil {
			t.Fatal(err)
		}
	}
	svc := ctxpkg.NewSessionService(f.store)
	dir := t.TempDir()
	sessions := DurableSessions{Service: svc, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, dir, provider.ModelInfo{ProviderID: "fake", ID: "pinned"}, "none")
	}}
	f.g, err = New(context.Background(), Config{Channels: registry, Authorizer: a,
		Approvals: approval.NewApprovalManager(), ConversationIdleTimeout: timeout,
		ResolveSession: func(ctx context.Context, route channel.Route) (*ctxpkg.Session, error) {
			f.resolves.Add(1)
			return sessions.Resolve(ctx, route)
		},
		Factory: func(ctx context.Context, s *ctxpkg.Session, route channel.Route, reporter engine.Reporter, approvals *approval.Service) (AgentRunner, error) {
			f.factories.Add(1)
			var runner AgentRunner = runnerFunc(func(context.Context, *ctxpkg.Session, engine.Reporter) error { return nil })
			if factory != nil {
				var err error
				runner, err = factory(ctx, s, route, reporter, approvals)
				if err != nil {
					return nil, err
				}
			}
			return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
				f.runs.Add(1)
				f.started <- s
				return runner.Run(ctx, s, r)
			}), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.g.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func restrictedPolicies() map[access.Scope]access.Policy {
	return map[access.Scope]access.Policy{
		{Channel: "telegram", AccountID: "default"}: {Mode: access.ModeRestricted, Operators: []string{"100", "200"}, Approvers: []string{"100"}, GroupChats: []string{"-1001", "-1002"}},
		{Channel: "feishu", AccountID: "default"}:   {Mode: access.ModeRestricted, Operators: []string{"100"}, Approvers: []string{"100"}, GroupChats: []string{"-1001"}},
	}
}

func accessMessage(user, chat, id, text string, kind channel.ChatType) channel.InboundMessage {
	return channel.InboundMessage{ID: id, Route: channel.Route{Channel: "telegram", AccountID: "default", ChatID: chat},
		UserID: user, UserName: "100", Text: text, ChatType: kind, MentionedBot: true}
}

func (f *accessFixture) assertNoRuntime(t *testing.T) {
	t.Helper()
	if f.g.ConversationCount() != 0 || f.resolves.Load() != 0 || f.store.writers.Load() != 0 || f.factories.Load() != 0 || f.runs.Load() != 0 {
		t.Fatal("unauthorized/consumed message created runtime or acquired a writer")
	}
	if sessions, err := f.store.List(context.Background()); err != nil || len(sessions) != 0 {
		t.Fatal("unauthorized/consumed message created durable session", sessions, err)
	}
	for _, a := range f.adapters {
		assertNoDelivery(t, a)
	}
}

func TestRestrictedDirectGroupAndMissingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, user, chat string
		kind             channel.ChatType
		allow            bool
	}{
		{"direct-allowed", "100", "private", channel.ChatDirect, true},
		{"direct-denied", "200", "private", channel.ChatDirect, false},
		{"missing-user-no-username-or-chat-fallback", "", "100", channel.ChatDirect, false},
		{"whitespace-user", " \t", "100", channel.ChatDirect, false},
		{"group-allowed", "100", "-1001", channel.ChatGroup, true},
		{"group-wrong-user", "200", "-1001", channel.ChatGroup, false},
		{"group-wrong-chat", "100", "-1002", channel.ChatGroup, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccessFixture(t, map[access.Scope]access.Policy{{Channel: "telegram", AccountID: "default"}: {
				Mode: access.ModeRestricted, Operators: []string{"100"}, GroupChats: []string{"-1001"},
			}}, 0, nil)
			admit(t, f.g, accessMessage(tc.user, tc.chat, "1", "task", tc.kind))
			if !tc.allow {
				f.assertNoRuntime(t)
				if len(f.g.dedup.seen) != 0 {
					t.Fatal("denied message entered dedup")
				}
				return
			}
			receive(t, f.started)
			if f.g.ConversationCount() != 1 || f.resolves.Load() != 1 || f.factories.Load() != 1 || f.runs.Load() != 1 {
				t.Fatal("authorized task did not run exactly once")
			}
			if out := receive(t, f.adapters["telegram"].sent); out.msg.Text != acceptedText {
				t.Fatal("missing Accepted", out)
			}
			assertNoDelivery(t, f.adapters["telegram"])
		})
	}
}

func TestAccessIsChannelAndAccountScopedBeforeDedup(t *testing.T) {
	f := newAccessFixture(t, map[access.Scope]access.Policy{
		{Channel: "telegram", AccountID: "default"}: {Mode: access.ModeRestricted, Operators: []string{"42"}},
		{Channel: "feishu", AccountID: "default"}:   {Mode: access.ModeRestricted, Operators: []string{"other"}},
	}, 0, nil)
	msg := accessMessage("42", "private", "same-id", "task", channel.ChatDirect)
	denied := msg
	denied.Route.Channel = "feishu"
	admit(t, f.g, denied)
	denied.Route.Channel, denied.Route.AccountID = "telegram", "other"
	admit(t, f.g, denied)
	denied = msg
	denied.UserID = "not-42"
	admit(t, f.g, denied)
	f.assertNoRuntime(t)
	admit(t, f.g, msg) // A denied message cannot poison legitimate dedup.
	receive(t, f.started)
	if f.runs.Load() != 1 || f.g.ConversationCount() != 1 {
		t.Fatal("cross-scope admission or poisoned dedup")
	}
}

func TestApprovalAuthorizationBypassesFullQueueAndPreservesScopes(t *testing.T) {
	for _, command := range []string{"approve", "reject"} {
		t.Run(command, func(t *testing.T) {
			results := make(chan bool, 1)
			policies := restrictedPolicies()
			policies[access.Scope{Channel: "telegram", AccountID: "second"}] = policies[access.Scope{Channel: "telegram", AccountID: "default"}]
			f := newAccessFixture(t, policies, testIdleTimeout, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (AgentRunner, error) {
				return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
					h := s.GetWorkingMemory(0)
					if h[len(h)-1].Content == "danger" {
						allowed, _ := approvals.Check(ctx, schema.ToolCall{ID: "task", Name: "bash", Arguments: []byte(`{"command":"rm -r disposable"}`)})
						results <- allowed
					}
					return nil
				}), nil
			})
			a := f.adapters["telegram"]
			msg := accessMessage("100", "-1001", "start", "danger", channel.ChatGroup)
			admit(t, f.g, msg)
			receive(t, f.started)
			if notice := receiveTaskReport(t, a); !strings.Contains(notice.msg.Text, "approve task") {
				t.Fatal("missing approval request", notice)
			}
			c := conversationFor(t, f.g, msg.Route)
			// Fill the ordinary queue while the worker is waiting for approval.
			for i := range ConversationQueueSize {
				admit(t, f.g, accessMessage("100", "-1001", fmt.Sprint(i), "queued", channel.ChatGroup))
				if out := receive(t, a.sent); out.msg.Text != acceptedText {
					t.Fatal("queued task missing Accepted", out)
				}
			}
			// Opposite outcomes make an unauthorized resolution observable even
			// if the waiter is scheduled only after the legitimate reply arrives.
			wrongCommand := "reject"
			if command == "reject" {
				wrongCommand = "approve"
			}
			for i, variant := range []string{"non-approver", "stranger", "missing-user", "wrong-chat", "wrong-channel", "wrong-account", "wrong-thread", "wrong-task", "malformed"} {
				control := accessMessage("100", "-1001", "control-"+fmt.Sprint(i), wrongCommand+" task", channel.ChatGroup)
				control.MentionedBot = false // Control replies bypass group mentions.
				switch variant {
				case "non-approver":
					control.UserID = "200"
				case "stranger":
					control.UserID = "300"
				case "missing-user":
					control.UserID = ""
				case "wrong-chat":
					control.Route.ChatID = "-1002" // Allowed group, wrong session.
				case "wrong-channel":
					control.Route.Channel = "feishu" // Authorized there too.
				case "wrong-account":
					control.Route.AccountID = "second" // Authorized account, wrong route.
				case "wrong-thread":
					control.Route.ThreadID = "other"
				case "wrong-task":
					control.Text = wrongCommand + " other-task"
				case "malformed":
					control.Text = wrongCommand + " task extra"
				}
				admit(t, f.g, control)
			}
			time.Sleep(3 * testIdleTimeout)
			if f.g.ConversationCount() != 1 || f.factories.Load() != 1 || f.resolves.Load() != 1 || f.runs.Load() != 1 {
				t.Fatal("denied/wrong-route approval ran, created runtime, or ended approval wait")
			}
			select {
			case <-results:
				t.Fatal("unauthorized approval resolved pending task")
			case <-c.closed:
				t.Fatal("approval wait evicted")
			default:
			}
			for _, a := range f.adapters {
				assertNoDelivery(t, a)
			}
			right := accessMessage("100", "-1001", "right", command+" task", channel.ChatGroup)
			right.MentionedBot = false
			admit(t, f.g, right)
			if allowed := receive(t, results); allowed != (command == "approve") {
				t.Fatal("wrong approval result", allowed)
			}
			for range ConversationQueueSize {
				receive(t, f.started)
			}
			receive(t, c.closed)
			if f.g.ConversationCount() != 0 || f.runs.Load() != ConversationQueueSize+1 || f.factories.Load() != 1 {
				t.Fatal("approval/FIFO/lifecycle regression")
			}
			admit(t, f.g, right) // Late duplicate must not recreate a runtime.
			if f.g.ConversationCount() != 0 {
				t.Fatal("late approval created runtime")
			}
			snap, err := f.store.Load(context.Background(), channel.BuildSessionKey(msg.Route))
			if err != nil || len(snap.History) != ConversationQueueSize+1 {
				t.Fatal("control command entered prompt/history", err)
			}
			for _, m := range snap.History {
				if m.Content != "danger" && m.Content != "queued" {
					t.Fatal("control command prompted agent")
				}
			}
		})
	}
}

func TestUnauthorizedAndMalformedControlsCreateNoRuntime(t *testing.T) {
	for _, user := range []string{"100", "200", "300", ""} {
		f := newAccessFixture(t, restrictedPolicies(), 0, nil)
		for _, command := range []string{"approve task", "reject task", "approve", "reject", "approve a b", "reject a b"} {
			admit(t, f.g, accessMessage(user, "private", command, command, channel.ChatDirect))
		}
		f.assertNoRuntime(t)
	}
}

func TestEmptyApproversCannotResolve(t *testing.T) {
	policies := restrictedPolicies()
	p := policies[access.Scope{Channel: "telegram", AccountID: "default"}]
	p.Approvers = nil
	policies[access.Scope{Channel: "telegram", AccountID: "default"}] = p
	results := make(chan bool, 1)
	f := newAccessFixture(t, policies, 0, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, _ engine.Reporter) error {
			allowed, _ := approvals.Check(ctx, schema.ToolCall{ID: "task", Name: "bash", Arguments: []byte(`{"command":"sudo ls"}`)})
			results <- allowed
			return nil
		}), nil
	})
	admit(t, f.g, accessMessage("100", "private", "1", "danger", channel.ChatDirect))
	receiveTaskReport(t, f.adapters["telegram"])
	for _, user := range []string{"100", "200"} {
		admit(t, f.g, accessMessage(user, "private", user, "approve task", channel.ChatDirect))
	}
	if err := f.g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if receive(t, results) {
		t.Fatal("empty approvers allowed approval instead of cancellation denial")
	}
}

func TestRestrictedLifecycleEvictsAndResumesOnlyAuthorizedTasks(t *testing.T) {
	runs := make(chan lifecycleRun, 2)
	f := newAccessFixture(t, restrictedPolicies(), testIdleTimeout, gatedLifecycleFactory(runs))
	msg := accessMessage("300", "private", "denied", "untrusted", channel.ChatDirect)
	admit(t, f.g, msg)
	f.assertNoRuntime(t)
	msg.UserID, msg.ID, msg.Text = "100", "first", "remember CODE"
	admit(t, f.g, msg)
	first := receive(t, runs)
	c1 := conversationFor(t, f.g, msg.Route)
	close(first.release)
	receive(t, c1.closed)
	if f.g.ConversationCount() != 0 {
		t.Fatal("authorized conversation did not evict")
	}
	writers := f.store.writers.Load()
	msg.UserID, msg.ID = "300", "denied-after-eviction"
	admit(t, f.g, msg)
	if f.g.ConversationCount() != 0 || f.resolves.Load() != 1 || f.store.writers.Load() != writers {
		t.Fatal("unauthorized message resumed an evicted session")
	}
	msg.UserID, msg.ID, msg.Text = "100", "second", "recall CODE"
	admit(t, f.g, msg)
	second := receive(t, runs)
	c2 := conversationFor(t, f.g, msg.Route)
	if c1 == c2 || first.session == second.session || first.session.ID != second.session.ID || len(second.history) != 2 || second.history[0].Content != "remember CODE" || second.history[1].Content != "recall CODE" {
		t.Fatal("restricted lifecycle lost durable history", second.history)
	}
	close(second.release)
	receive(t, c2.closed)
	if f.factories.Load() != 2 || f.resolves.Load() != 2 {
		t.Fatal("authorized runtime was not recreated")
	}
}

func TestOpenTasksAllowMissingIdentityAndUnlistedGroups(t *testing.T) {
	for _, mode := range []access.Mode{"", access.ModeOpen} {
		for _, kind := range []channel.ChatType{channel.ChatDirect, channel.ChatGroup} {
			f := newAccessFixture(t, map[access.Scope]access.Policy{{Channel: "telegram", AccountID: "default"}: {Mode: mode}}, 0, nil)
			admit(t, f.g, accessMessage("", "unlisted", "1", "task", kind))
			receive(t, f.started)
			if f.g.ConversationCount() != 1 || f.runs.Load() != 1 || f.factories.Load() != 1 {
				t.Fatal("open backward compatibility changed")
			}
			if out := receive(t, f.adapters["telegram"].sent); out.msg.Text != acceptedText {
				t.Fatal("open task missing Accepted", out)
			}
		}
	}
}

func TestCommandRouterCannotResolveWithoutAuthorizer(t *testing.T) {
	// The command boundary itself must fail closed, even outside Receive. A
	// missing authorizer must never reach session lookup or Approval.Resolve.
	r := commandRouter{approvals: approval.NewApprovalManager()}
	for _, command := range []string{"approve task", "reject task", "approve", "approve a b"} {
		if !r.handle(accessMessage("100", "private", command, command, channel.ChatDirect)) {
			t.Fatal("unauthorized control was not consumed")
		}
	}
}

func TestOpenStartupWarningsAreOnceAndDenialLogsArePrivate(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	registry := channel.NewRegistry()
	if err := registry.Register(newFakeAdapter("telegram")); err != nil {
		t.Fatal(err)
	}
	// The disabled/unregistered Feishu scope must not produce a warning.
	a, err := access.NewAuthorizer(map[access.Scope]access.Policy{{Channel: "telegram", AccountID: "default"}: {}, {Channel: "feishu", AccountID: "default"}: {}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(context.Background(), Config{Channels: registry, Authorizer: a,
		ResolveSession: func(context.Context, channel.Route) (*ctxpkg.Session, error) {
			t.Error("control resolved session")
			return nil, nil
		},
		Factory: func(context.Context, *ctxpkg.Session, channel.Route, engine.Reporter, *approval.Service) (AgentRunner, error) {
			t.Error("control created factory")
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"approve", "approve a b", "reject task"} {
		admit(t, g, accessMessage("", "private", text, text, channel.ChatDirect))
	}
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), "channel telegram access mode is open") != 1 || strings.Contains(logs.String(), "feishu") {
		t.Fatal("missing/repeated/disabled-channel warning", logs.String())
	}
	logs.Reset()
	f := newAccessFixture(t, restrictedPolicies(), 0, nil)
	msg := accessMessage("private-user-id", "private-chat-id", "private-message-id", "private-message-body token-secret", channel.ChatGroup)
	admit(t, f.g, msg)
	msg.UserID, msg.Text = "200", "approve private-task-id"
	admit(t, f.g, msg) // Ordinary access denies unlisted group first.
	msg.Route.ChatID, msg.ID = "-1001", "another-id"
	admit(t, f.g, msg) // Operator denied specifically as a non-approver.
	f.assertNoRuntime(t)
	if err := f.g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-user-id", "private-chat-id", "private-message-id", "private-message-body", "token-secret", "private-task-id", "-1001"} {
		if strings.Contains(logs.String(), private) {
			t.Fatal("denial log leaked identity/content", private)
		}
	}
	for _, reason := range []string{"user_not_allowed", "group_not_allowed", "not_approver"} {
		if !strings.Contains(logs.String(), "reason="+reason) {
			t.Fatal("missing typed denial reason", logs.String())
		}
	}
}
