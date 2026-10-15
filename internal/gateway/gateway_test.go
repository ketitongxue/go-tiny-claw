package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

type runnerFunc func(context.Context, *ctxpkg.Session, engine.Reporter) error

func (f runnerFunc) Run(ctx context.Context, sess *ctxpkg.Session, reporter engine.Reporter) error {
	return f(ctx, sess, reporter)
}

type inboundDelivery struct {
	msg channel.InboundMessage
	ack chan error
}
type sentMessage struct {
	route channel.Route
	msg   channel.OutboundMessage
}
type fakeAdapter struct {
	id       string
	inbound  chan inboundDelivery
	sent     chan sentMessage
	started  chan struct{}
	stopped  chan struct{}
	sendHook func(context.Context, channel.Route, channel.OutboundMessage) error
}

func newFakeAdapter(id string) *fakeAdapter {
	return &fakeAdapter{id: id, inbound: make(chan inboundDelivery, 100), sent: make(chan sentMessage, 100), started: make(chan struct{}), stopped: make(chan struct{})}
}
func (a *fakeAdapter) ID() string { return a.id }
func (a *fakeAdapter) Start(ctx context.Context, handler channel.InboundHandler) error {
	close(a.started)
	defer close(a.stopped)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case in := <-a.inbound:
			in.ack <- handler(ctx, in.msg)
		}
	}
}
func (a *fakeAdapter) Send(ctx context.Context, route channel.Route, msg channel.OutboundMessage) error {
	if a.sendHook != nil {
		if err := a.sendHook(ctx, route, msg); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a.sent <- sentMessage{route, msg}:
		return nil
	}
}
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for gateway")
		var zero T
		return zero
	}
}
func inbound(chat, id, text string) channel.InboundMessage {
	return channel.InboundMessage{ID: id, Route: channel.Route{Channel: "test", AccountID: "default", ChatID: chat}, ChatType: channel.ChatDirect, Text: text, ReceivedAt: time.Now()}
}
func receiveTaskReport(t *testing.T, a *fakeAdapter) sentMessage {
	t.Helper()
	for {
		out := receive(t, a.sent)
		if out.msg.Text != "🤖 已收到，正在处理……" {
			return out
		}
	}
}

func testGateway(t *testing.T, factory RuntimeFactory, policies ...delivery.ReportingPolicy) (*Gateway, *fakeAdapter, *sessionstore.MemoryStore) {
	t.Helper()
	a := newFakeAdapter("test")
	registry := channel.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	store := sessionstore.NewMemoryStore()
	svc := ctxpkg.NewSessionService(store)
	dir := t.TempDir()
	sessions := DurableSessions{Service: svc, Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
		return svc.Create(ctx, id, dir, provider.ModelInfo{ProviderID: "fake", ID: "pinned"}, "none")
	}}
	var policy delivery.ReportingPolicy
	if len(policies) != 0 {
		policy = policies[0]
	}
	g, err := New(context.Background(), Config{Channels: registry, ResolveSession: sessions.Resolve, Factory: factory,
		Approvals: approval.NewApprovalManager(), ReportingPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := g.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return g, a, store
}
func admit(t *testing.T, g *Gateway, msg channel.InboundMessage) {
	t.Helper()
	if err := g.Receive(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
}

func TestFakeAdapterEndToEnd(t *testing.T) {
	g, a, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
			r.OnMessage(ctx, "reply: "+s.GetWorkingMemory(0)[0].Content)
			return nil
		}), nil
	})
	done := make(chan error, 1)
	go func() { done <- g.Run() }()
	receive(t, a.started)
	msg := inbound("a", "1", "hello")
	ack := make(chan error, 1)
	a.inbound <- inboundDelivery{msg, ack}
	if err := receive(t, ack); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{acceptedText, "reply: hello"} {
		out := receive(t, a.sent)
		if out.route != msg.Route || out.msg.Kind != channel.OutboundText || out.msg.Text != want {
			t.Fatalf("wrong delivery: %+v; want %q", out, want)
		}
	}
	g.Cancel()
	if err := receive(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	receive(t, a.stopped)
}

func TestConversationsAreOrderedIsolatedAndConcurrent(t *testing.T) {
	type run struct {
		sess    *ctxpkg.Session
		route   channel.Route
		text    string
		history []schema.Message
	}
	runs := make(chan run, 8)
	release := make(chan struct{})
	var mu sync.Mutex
	factories := map[string]int{}
	g, _, _ := testGateway(t, func(_ context.Context, s *ctxpkg.Session, route channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		mu.Lock()
		factories[s.ID]++
		mu.Unlock()
		return runnerFunc(func(ctx context.Context, current *ctxpkg.Session, _ engine.Reporter) error {
			h := current.GetWorkingMemory(0)
			text := h[len(h)-1].Content
			runs <- run{current, route, text, h}
			if text == "A1" {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return current.Append(ctx, schema.Message{Role: schema.RoleAssistant, Content: "response"})
		}), nil
	})
	admit(t, g, inbound("a", "a1", "A1"))
	first := receive(t, runs)
	admit(t, g, inbound("a", "a2", "A2"))
	admit(t, g, inbound("a", "a3", "A3"))
	admit(t, g, inbound("b", "b1", "B1"))
	other := receive(t, runs)
	if other.text != "B1" || first.sess == other.sess || first.sess.ID == other.sess.ID || len(other.history) != 1 {
		t.Fatal("sessions mixed or cross-conversation blocked")
	}
	if first.sess.ID != channel.BuildSessionKey(first.route) || other.sess.ID != channel.BuildSessionKey(other.route) {
		t.Fatal("noncanonical new session")
	}
	close(release)
	second, third := receive(t, runs), receive(t, runs)
	if second.text != "A2" || third.text != "A3" || second.sess != first.sess || len(second.history) != 3 || len(third.history) != 5 {
		t.Fatalf("order/continuity changed: %+v %+v", second, third)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(factories, map[string]int{first.sess.ID: 1, other.sess.ID: 1}) {
		t.Fatal(factories)
	}
}

func TestDedupIsAtomicAndChannelAccountScoped(t *testing.T) {
	runs := make(chan string, 20)
	g, _, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(_ context.Context, s *ctxpkg.Session, _ engine.Reporter) error { runs <- s.ID; return nil }), nil
	})
	if err := g.channels.Register(newFakeAdapter("other")); err != nil {
		t.Fatal(err)
	}
	msg := inbound("a", "same-id", "once")
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
	receive(t, runs)
	// Same ID in a different chat of the same account is still a retry.
	sameAccount := msg
	sameAccount.Route.ChatID = "b"
	admit(t, g, sameAccount)
	account := msg
	account.Route.AccountID = "another"
	admit(t, g, account)
	receive(t, runs)
	otherChannel := msg
	otherChannel.Route.Channel = "other"
	admit(t, g, otherChannel)
	receive(t, runs)
	// A sentinel on the first queue makes duplicate execution observable without sleeping.
	admit(t, g, inbound("a", "sentinel", "sentinel"))
	receive(t, runs)
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-runs:
		t.Fatal("duplicate ran", id)
	default:
	}
}

func TestQueueFullIsRetryableNotRemembered(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	processed := make(chan string, ConversationQueueSize+2)
	g, a, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, _ *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
			h := s.GetWorkingMemory(0)
			text := h[len(h)-1].Content
			if text == "block" {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			processed <- text
			return nil
		}), nil
	})
	admit(t, g, inbound("a", "first", "block"))
	receive(t, started)
	if out := receive(t, a.sent); out.msg.Text != acceptedText {
		t.Fatal("missing accepted notification", out)
	}
	for i := range ConversationQueueSize {
		id := fmt.Sprint(i)
		admit(t, g, inbound("a", id, id))
		if out := receive(t, a.sent); out.msg.Text != acceptedText {
			t.Fatal("missing queued task accepted notification", out)
		}
	}
	overflow := inbound("a", "overflow", "retry")
	if err := g.Receive(context.Background(), overflow); !errors.Is(err, ErrQueueFull) {
		t.Fatal("missing backpressure", err)
	}
	assertNoDelivery(t, a)
	close(release)
	for range ConversationQueueSize + 1 {
		receive(t, processed)
	}
	admit(t, g, overflow)
	if out := receive(t, a.sent); out.msg.Text != acceptedText {
		t.Fatal("retry missing accepted notification", out)
	}
	if receive(t, processed) != "retry" {
		t.Fatal("retry lost")
	}
}

func TestApprovalBypassesQueueAndIsScoped(t *testing.T) {
	for _, command := range []string{"approve", "reject"} {
		t.Run(command, func(t *testing.T) {
			results := make(chan bool, 2)
			next := make(chan struct{}, 1)
			g, a, _ := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (AgentRunner, error) {
				return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
					h := s.GetWorkingMemory(0)
					if h[len(h)-1].Content == "danger" {
						allowed, _ := approvals.Check(ctx, schema.ToolCall{ID: "task123", Name: "bash", Arguments: []byte(`{"command":"rm -r target"}`)})
						results <- allowed
					} else {
						next <- struct{}{}
					}
					return nil
				}), nil
			})
			admit(t, g, inbound("a", "1", "danger"))
			notice := receiveTaskReport(t, a)
			if !strings.Contains(notice.msg.Text, "approve task123") {
				t.Fatal(notice)
			}
			admit(t, g, inbound("a", "2", "queued"))
			if out := receive(t, a.sent); out.msg.Text != acceptedText {
				t.Fatal("queued task missing accepted notification", out)
			}
			wrong := inbound("b", "3", "approve task123")
			wrong.ChatType = channel.ChatGroup
			admit(t, g, wrong)
			select {
			case <-results:
				t.Fatal("wrong chat approved")
			default:
			}
			// No @ mention, and a full ordinary queue must not affect control replies.
			right := inbound("a", "4", command+" task123")
			right.ChatType = channel.ChatGroup
			admit(t, g, right)
			admit(t, g, right)
			if receive(t, results) != (command == "approve") {
				t.Fatal("unexpected resolution")
			}
			receive(t, next)
			assertNoDelivery(t, a) // approve/reject (including retries) never ACK.
		})
	}
}

func TestShutdownStopsActiveApprovalAndQueuedMessageReleasesWriters(t *testing.T) {
	active := make(chan struct{})
	stopped := make(chan struct{})
	approvalResult := make(chan bool, 1)
	g, a, store := testGateway(t, func(_ context.Context, _ *ctxpkg.Session, route channel.Route, _ engine.Reporter, approvals *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
			if len(s.GetWorkingMemory(0)) != 1 {
				t.Error("queued message ran after shutdown")
			}
			if route.ChatID == "approval" {
				allowed, _ := approvals.Check(ctx, schema.ToolCall{ID: "t", Name: "bash", Arguments: []byte(`{"command":"sudo ls"}`)})
				approvalResult <- allowed
				return ctx.Err()
			}
			close(active)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		}), nil
	})
	admit(t, g, inbound("active", "1", "running"))
	receive(t, active)
	admit(t, g, inbound("active", "2", "queued"))
	admit(t, g, inbound("approval", "3", "danger"))
	receiveTaskReport(t, a)
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	receive(t, stopped)
	if receive(t, approvalResult) {
		t.Fatal("cancellation allowed approval")
	}
	for _, chat := range []string{"active", "approval"} {
		key := channel.BuildSessionKey(inbound(chat, "", "").Route)
		snap, err := store.Load(context.Background(), key)
		if err != nil || len(snap.History) != 1 {
			t.Fatal("shutdown history changed", err)
		}
		lock, err := store.AcquireWriter(context.Background(), key)
		if err != nil {
			t.Fatal("writer leaked", err)
		}
		_ = lock.Release()
	}
	if err := g.Receive(context.Background(), inbound("after", "4", "late")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDedupBoundTTLAndSafeEncoding(t *testing.T) {
	d := deduplicator{}
	now := time.Now()
	for i := range maxSeenMessages + 2 {
		d.remember(fmt.Sprint(i), now.Add(time.Duration(i)*time.Second))
	}
	if len(d.seen) != maxSeenMessages || d.contains("0", now) || d.contains("1", now) {
		t.Fatal("dedup not bounded")
	}
	d.remember("expired", now.Add(-seenTTL))
	if d.contains("expired", now) {
		t.Fatal("TTL ignored")
	}
	a := inbound("", "x:message:y", "")
	b := a
	b.Route.AccountID = "default:message:x"
	b.ID = "y"
	if DedupKey(a) == DedupKey(b) {
		t.Fatal("ambiguous dedup key")
	}
}
