package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

const testIdleTimeout = 20 * time.Millisecond

func lifecycleGateway(t *testing.T, timeout time.Duration, store sessionstore.Store, factory RuntimeFactory, policies ...delivery.ReportingPolicy) (*Gateway, *fakeAdapter) {
	t.Helper()
	a := newFakeAdapter("test")
	registry := channel.NewRegistry()
	if err := registry.Register(a); err != nil {
		t.Fatal(err)
	}
	resolver := durableResolver(ctxpkg.NewSessionService(store), t.TempDir(), "pinned")
	var policy delivery.ReportingPolicy
	if len(policies) != 0 {
		policy = policies[0]
	}
	g, err := New(context.Background(), Config{Channels: registry, ResolveSession: resolver.Resolve, Factory: factory,
		Approvals: approval.NewApprovalManager(), ReportingPolicy: policy, ConversationIdleTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	// Close-error tests assert the aggregated error themselves. Always drain on
	// an early test failure, too; runners below all observe Gateway cancellation.
	t.Cleanup(func() { _ = g.Shutdown() })
	return g, a
}

func conversationFor(t *testing.T, g *Gateway, route channel.Route) *Conversation {
	t.Helper()
	g.conversations.mu.Lock()
	defer g.conversations.mu.Unlock()
	c := g.conversations.conversations[channel.BuildSessionKey(route)]
	if c == nil {
		t.Fatal("expected active conversation")
	}
	return c
}

func assertWriterHeld(t *testing.T, store sessionstore.Store, id string) {
	t.Helper()
	lock, err := store.AcquireWriter(context.Background(), id)
	if lock != nil {
		_ = lock.Release()
	}
	if !errors.Is(err, sessionstore.ErrInUse) {
		t.Fatal("active session lost writer ownership", err)
	}
}

type lifecycleRun struct {
	session *ctxpkg.Session
	history []schema.Message
	release chan struct{}
}

func holdLifecycleRun(ctx context.Context, s *ctxpkg.Session, runs chan<- lifecycleRun) error {
	run := lifecycleRun{s, s.GetWorkingMemory(0), make(chan struct{})}
	select {
	case runs <- run:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-run.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func gatedLifecycleFactory(runs chan<- lifecycleRun) RuntimeFactory {
	return func(context.Context, *ctxpkg.Session, channel.Route, engine.Reporter, *approval.Service) (AgentRunner, error) {
		return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
			return holdLifecycleRun(ctx, s, runs)
		}), nil
	}
}

// The injection lives solely at the existing Store/SessionLock boundary. There
// is no new production Session interface or lifecycle testing hook.
type lifecycleStore struct {
	sessionstore.Store
	release func(string, sessionstore.SessionLock) error
}

type lifecycleLock func() error

func (f lifecycleLock) Release() error { return f() }

func (s lifecycleStore) AcquireWriter(ctx context.Context, id string) (sessionstore.SessionLock, error) {
	lock, err := s.Store.AcquireWriter(ctx, id)
	if err != nil {
		return nil, err
	}
	return lifecycleLock(func() error {
		// Resolve probes missing canonical/legacy IDs before Create. Those
		// temporary leases are not Session.Close and must not trigger the hook.
		if _, err := s.Store.Load(context.Background(), id); errors.Is(err, sessionstore.ErrNotFound) {
			return lock.Release()
		}
		return s.release(id, lock)
	}), nil
}

func TestIdleEvictionReleasesFileWriterAndResumesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions")
	store, err := sessionstore.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	other, err := sessionstore.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	runs := make(chan lifecycleRun, 2)
	var factories atomic.Int32
	g, _ := lifecycleGateway(t, testIdleTimeout, store, func(context.Context, *ctxpkg.Session, channel.Route, engine.Reporter, *approval.Service) (AgentRunner, error) {
		factories.Add(1)
		return runnerFunc(func(ctx context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
			if err := holdLifecycleRun(ctx, s, runs); err != nil {
				return err
			}
			return s.Append(ctx, schema.Message{Role: schema.RoleAssistant, Content: "noted"})
		}), nil
	})
	msg := inbound("memory", "1", "remember CODE-123")
	admit(t, g, msg)
	first := receive(t, runs)
	c1 := conversationFor(t, g, msg.Route)
	if g.ConversationCount() != 1 {
		t.Fatal("missing active runtime")
	}
	outsider := ctxpkg.NewSessionService(other)
	if s, err := outsider.Resume(context.Background(), first.session.ID); !errors.Is(err, sessionstore.ErrInUse) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatal("second service acquired an active writer", err)
	}
	close(first.release)
	receive(t, c1.closed)
	if g.ConversationCount() != 0 {
		t.Fatal("idle conversation retained")
	}
	if err := first.session.Append(context.Background(), schema.Message{}); !errors.Is(err, ctxpkg.ErrSessionClosed) {
		t.Fatal("Session.Close not called", err)
	}
	resumed, err := outsider.Resume(context.Background(), first.session.ID)
	if err != nil {
		t.Fatal("eviction did not release real file writer", err)
	}
	if h := resumed.GetWorkingMemory(0); len(h) != 2 || h[0].Content != msg.Text || h[1].Role != schema.RoleAssistant {
		t.Fatal("committed history lost", h)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	admit(t, g, inbound("memory", "2", "what was the code?"))
	second := receive(t, runs)
	c2 := conversationFor(t, g, msg.Route)
	if c1 == c2 || first.session == second.session || factories.Load() != 2 || g.ConversationCount() != 1 {
		t.Fatal("runtime/engine was reused instead of recreated")
	}
	if second.session.ID != first.session.ID || second.session.ModelRef() != first.session.ModelRef() ||
		len(second.history) != 3 || !reflect.DeepEqual(second.history[:2], resumed.GetWorkingMemory(0)) || second.history[2].Content != "what was the code?" {
		t.Fatal("durable resume lost ID/model/history", second.history)
	}
	close(second.release)
	receive(t, c2.closed)
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestIdleEvictionNeverInterruptsRunOrBacklog(t *testing.T) {
	runs := make(chan lifecycleRun, 3)
	store := sessionstore.NewMemoryStore()
	g, _ := lifecycleGateway(t, testIdleTimeout, store, gatedLifecycleFactory(runs))
	msg := inbound("backlog", "1", "msg1")
	admit(t, g, msg)
	first := receive(t, runs)
	c := conversationFor(t, g, msg.Route)
	admit(t, g, inbound("backlog", "2", "msg2"))
	admit(t, g, inbound("backlog", "3", "msg3"))
	current := first
	for i := range 3 {
		// Every Run lasts much longer than the timeout. The first two also
		// have backlog, and the last must get a fresh countdown afterwards.
		time.Sleep(3 * testIdleTimeout)
		if g.ConversationCount() != 1 || current.session != first.session || current.history[len(current.history)-1].Content != fmt.Sprintf("msg%d", i+1) {
			t.Fatal("run/backlog evicted or order changed", current.history)
		}
		assertWriterHeld(t, store, first.session.ID)
		select {
		case <-c.closed:
			t.Fatal("active worker closed")
		default:
		}
		finished := time.Now()
		close(current.release)
		if i < 2 {
			current = receive(t, runs)
		} else {
			receive(t, c.closed)
			if time.Since(finished) < testIdleTimeout {
				t.Fatal("stale tick evicted immediately after Run")
			}
		}
	}
	if g.ConversationCount() != 0 {
		t.Fatal("drained conversation not evicted")
	}
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

type lifecycleDangerousTool struct{ calls *atomic.Int32 }

func (lifecycleDangerousTool) Name() string { return "bash" }
func (lifecycleDangerousTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "bash"}
}
func (t lifecycleDangerousTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.calls.Add(1)
	return "fake tool: no host command executed", nil
}

func TestIdleEvictionWaitsForRealEngineApproval(t *testing.T) {
	store := sessionstore.NewMemoryStore()
	sessions := make(chan *ctxpkg.Session, 1)
	var toolCalls atomic.Int32
	g, a := lifecycleGateway(t, testIdleTimeout, store, func(_ context.Context, s *ctxpkg.Session, _ channel.Route, _ engine.Reporter, approvals *approval.Service) (AgentRunner, error) {
		sessions <- s
		registry := tools.NewRegistry()
		registry.Register(lifecycleDangerousTool{&toolCalls})
		registry.Use(approvals.Check)
		turn := 0
		p := presentationProviderFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
			turn++
			if turn == 1 {
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "lifecycle-task", Name: "bash", Arguments: []byte(`{"command":"rm -r disposable"}`)}}}, nil
			}
			return &schema.Message{Role: schema.RoleAssistant, Content: "approved and finished"}, nil
		})
		return engine.NewAgentEngine(p, registry, false, false), nil
	}, delivery.ReportingPolicy{Mode: delivery.ReportModeQuiet})
	msg := inbound("approval", "1", "danger")
	admit(t, g, msg)
	s := receive(t, sessions)
	c := conversationFor(t, g, msg.Route)
	if out := receive(t, a.sent); !strings.Contains(out.msg.Text, "approve lifecycle-task") {
		t.Fatal("approval request missing", out)
	}
	time.Sleep(4 * testIdleTimeout)
	if g.ConversationCount() != 1 || toolCalls.Load() != 0 {
		t.Fatal("approval wait was treated as idle")
	}
	assertWriterHeld(t, store, s.ID)
	approve := inbound("approval", "2", "approve lifecycle-task")
	approve.ChatType = channel.ChatGroup // Bypasses queue/mention, as before.
	admit(t, g, approve)
	if out := receive(t, a.sent); out.msg.Text != "approved and finished" {
		t.Fatal("approval did not resume Run", out)
	}
	receive(t, c.closed)
	if toolCalls.Load() != 1 || g.ConversationCount() != 0 {
		t.Fatal("approval completion did not permit eviction")
	}
	admit(t, g, inbound("approval", "late", "approve lifecycle-task"))
	if g.ConversationCount() != 0 {
		t.Fatal("late approval created runtime")
	}
	assertNoDelivery(t, a)
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestClosingAdmissionIsRetryableIsolatedAndDeduplicated(t *testing.T) {
	base := sessionstore.NewMemoryStore()
	closing, releaseClose := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(releaseClose) })
	var closes atomic.Int32
	route := inbound("a", "", "").Route
	key := channel.BuildSessionKey(route)
	store := lifecycleStore{Store: base, release: func(id string, lock sessionstore.SessionLock) error {
		if id == key && closes.Add(1) == 1 {
			close(closing)
			<-releaseClose // Deliberately retain writer while admission continues.
		}
		return lock.Release()
	}}
	runs := make(chan lifecycleRun, 40)
	g, a := lifecycleGateway(t, testIdleTimeout, store, gatedLifecycleFactory(runs))
	t.Cleanup(unblock) // Runs before the helper's Shutdown even on failure.
	admit(t, g, inbound("a", "first", "first"))
	first := receive(t, runs)
	c1 := conversationFor(t, g, route)
	if out := receive(t, a.sent); out.msg.Text != acceptedText {
		t.Fatal(out)
	}
	close(first.release)
	receive(t, closing)
	assertWriterHeld(t, base, first.session.ID)
	if g.ConversationCount() != 1 {
		t.Fatal("entry removed before writer release")
	}
	g.conversations.mu.Lock()
	seq := c1.activitySeq
	g.conversations.mu.Unlock()
	retry := inbound("a", "retry", "retry after closing")
	var callers sync.WaitGroup
	for range 32 {
		callers.Go(func() {
			if err := g.Receive(context.Background(), retry); !errors.Is(err, ErrConversationClosing) {
				t.Error("closing admission did not ask for retry", err)
			}
		})
	}
	callers.Wait()
	g.conversations.mu.Lock()
	unchanged := c1.closing && c1.activitySeq == seq && len(c1.queue) == 0 && g.conversations.conversations[key] == c1
	g.conversations.mu.Unlock()
	if !unchanged {
		t.Fatal("failed admission mutated lifecycle/queue")
	}
	assertNoDelivery(t, a)
	// Neither manager.mu nor g.mu may be held by A's Session.Close.
	bReturned := make(chan error, 1)
	go func() { bReturned <- g.Receive(context.Background(), inbound("b", "b1", "independent")) }()
	select {
	case err := <-bReturned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing A blocked B admission")
	}
	other := receive(t, runs)
	if out := receive(t, a.sent); out.route.ChatID != "b" || out.msg.Text != acceptedText {
		t.Fatal("B did not get Accepted", out)
	}
	close(other.release)
	unblock()
	receive(t, c1.closed)
	for range 32 {
		callers.Go(func() {
			if err := g.Receive(context.Background(), retry); err != nil {
				t.Error("retry was deduplicated/rejected", err)
			}
		})
	}
	callers.Wait()
	second := receive(t, runs)
	c2 := conversationFor(t, g, route)
	if c1 == c2 || first.session == second.session || len(second.history) != 2 || second.history[1].Content != retry.Text {
		t.Fatal("retry did not resume a new runtime exactly once", second.history)
	}
	if out := receive(t, a.sent); out.route != route || out.msg.Text != acceptedText {
		t.Fatal("retry missing Accepted", out)
	}
	close(second.release)
	receive(t, c2.closed)
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 2 {
		t.Fatal("Session.Close not exactly once per runtime", closes.Load())
	}
	assertNoDelivery(t, a)
	select {
	case duplicate := <-runs:
		t.Fatal("retry ran more than once", duplicate.history)
	default:
	}
}

func TestShutdownDuringEvictionAggregatesCloseErrorOnce(t *testing.T) {
	closing, releaseClose := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(releaseClose) })
	closeErr := errors.New("injected writer release failure")
	var closes atomic.Int32
	store := lifecycleStore{Store: sessionstore.NewMemoryStore(), release: func(_ string, lock sessionstore.SessionLock) error {
		closes.Add(1)
		close(closing)
		<-releaseClose
		return errors.Join(lock.Release(), closeErr)
	}}
	runs := make(chan lifecycleRun, 1)
	g, _ := lifecycleGateway(t, testIdleTimeout, store, gatedLifecycleFactory(runs))
	t.Cleanup(unblock)
	msg := inbound("failure", "1", "close with error")
	admit(t, g, msg)
	run := receive(t, runs)
	c := conversationFor(t, g, msg.Route)
	close(run.release)
	receive(t, closing)
	results := make(chan error, 8)
	var started sync.WaitGroup
	for range cap(results) {
		started.Add(1)
		go func() { started.Done(); results <- g.Shutdown() }()
	}
	started.Wait()
	select {
	case err := <-results:
		t.Fatal("Shutdown returned before Session.Close", err)
	case <-c.closed:
		t.Fatal("closed signaled before Session.Close")
	default:
	}
	unblock()
	for range cap(results) {
		if err := receive(t, results); !errors.Is(err, closeErr) {
			t.Fatal("close error not aggregated", err)
		}
	}
	receive(t, c.closed)
	if closes.Load() != 1 || g.ConversationCount() != 0 {
		t.Fatal("double close or zombie conversation")
	}
	if err := g.Shutdown(); !errors.Is(err, closeErr) {
		t.Fatal("repeated Shutdown lost close error", err)
	}
}

func TestIdleEvictionCloseErrorIsRetainedUntilShutdown(t *testing.T) {
	closeErr := errors.New("injected eviction close error")
	var closes atomic.Int32
	store := lifecycleStore{Store: sessionstore.NewMemoryStore(), release: func(_ string, lock sessionstore.SessionLock) error {
		closes.Add(1)
		return errors.Join(lock.Release(), closeErr)
	}}
	runs := make(chan lifecycleRun, 1)
	g, _ := lifecycleGateway(t, testIdleTimeout, store, gatedLifecycleFactory(runs))
	msg := inbound("failure", "1", "idle close failure")
	admit(t, g, msg)
	run := receive(t, runs)
	c := conversationFor(t, g, msg.Route)
	close(run.release)
	receive(t, c.closed)
	if g.ConversationCount() != 0 || closes.Load() != 1 || g.ctx.Err() != nil {
		t.Fatal("failed close left zombie runtime or canceled unrelated gateway")
	}
	if err := g.Shutdown(); !errors.Is(err, closeErr) {
		t.Fatal("idle eviction error lost before Shutdown", err)
	}
}

func TestShutdownDuringIdleAndEvictionDisabled(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Hour} {
		t.Run(timeout.String(), func(t *testing.T) {
			store := sessionstore.NewMemoryStore()
			runs := make(chan lifecycleRun, 1)
			g, _ := lifecycleGateway(t, timeout, store, gatedLifecycleFactory(runs))
			msg := inbound("idle", "1", "done")
			admit(t, g, msg)
			run := receive(t, runs)
			c := conversationFor(t, g, msg.Route)
			close(run.release)
			time.Sleep(3 * testIdleTimeout)
			if g.ConversationCount() != 1 {
				t.Fatal("disabled/long timeout evicted early")
			}
			finished := make(chan error, 1)
			go func() { finished <- g.Shutdown() }()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Shutdown waited for idle timeout")
			}
			receive(t, c.closed)
			lock, err := store.AcquireWriter(context.Background(), run.session.ID)
			if err != nil {
				t.Fatal("Shutdown leaked writer", err)
			}
			_ = lock.Release()
			if g.ConversationCount() != 0 {
				t.Fatal("Shutdown retained runtime")
			}
		})
	}
}

func TestGatewayRejectsNegativeIdleTimeout(t *testing.T) {
	registry := channel.NewRegistry()
	_, err := New(context.Background(), Config{Channels: registry, ConversationIdleTimeout: -time.Second,
		ResolveSession: func(context.Context, channel.Route) (*ctxpkg.Session, error) { return nil, nil },
		Factory:        gatedLifecycleFactory(make(chan lifecycleRun))})
	if err == nil {
		t.Fatal("negative runtime idle timeout accepted")
	}
}

func TestEvictionRevalidatesActivityAndBacklog(t *testing.T) {
	// Exercise the exact interleaving independently of scheduler/timer timing:
	// snapshot -> enqueue -> consume -> empty again -> stale eviction attempt.
	m := &ConversationManager{ctx: context.Background(), conversations: make(map[string]*Conversation)}
	msg := inbound("seq", "1", "task")
	c := &Conversation{Route: msg.Route, key: channel.BuildSessionKey(msg.Route), queue: make(chan channel.InboundMessage, ConversationQueueSize),
		closed: make(chan struct{}), reporter: delivery.NewChannelReporter(nil, msg.Route, delivery.ReportingPolicy{Mode: delivery.ReportModeQuiet})}
	m.conversations[c.key] = c
	seq, idle := m.idleSequence(c)
	if !idle {
		t.Fatal("empty worker not idle")
	}
	if _, err := m.enqueue(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if m.beginEviction(c, c.activitySeq) {
		t.Fatal("evicted with backlog")
	}
	<-c.queue
	if m.beginEviction(c, seq) {
		t.Fatal("stale activity snapshot evicted after new admission")
	}
	seq, idle = m.idleSequence(c)
	if !idle || seq != 1 || !m.beginEviction(c, seq) || m.beginEviction(c, seq) {
		t.Fatal("invalid idle -> closing transition")
	}
	if _, err := m.enqueue(context.Background(), msg); !errors.Is(err, ErrConversationClosing) || c.activitySeq != seq {
		t.Fatal("admitted after beginEviction", err)
	}
	if count := m.finishEviction(c); count != 0 {
		t.Fatal("finish did not remove conversation")
	}
	receive(t, c.closed)
	if m.beginEviction(c, seq) {
		t.Fatal("evicted removed worker")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.enqueue(ctx, msg); !errors.Is(err, context.Canceled) || len(m.conversations) != 0 {
		t.Fatal("canceled admission created conversation", err)
	}
}

func TestIdleEvictionAdmissionRace(t *testing.T) {
	const iterations = 200
	store := lifecycleStore{Store: sessionstore.NewMemoryStore(), release: func(_ string, lock sessionstore.SessionLock) error {
		time.Sleep(100 * time.Microsecond) // Widen the closing window with writer held.
		return lock.Release()
	}}
	runs := make(chan lifecycleRun, 8)
	g, _ := lifecycleGateway(t, time.Millisecond, store, gatedLifecycleFactory(runs), delivery.ReportingPolicy{Mode: delivery.ReportModeQuiet})
	for i := range iterations {
		msg := inbound("race", fmt.Sprintf("seed-%d", i), fmt.Sprintf("seed-%d", i))
		admit(t, g, msg)
		first := receive(t, runs)
		close(first.release)
		// Race admission on both sides of the real worker-local idle deadline.
		if i%3 != 0 {
			time.Sleep(time.Duration(700+i%3*400) * time.Microsecond)
		}
		next := inbound("race", fmt.Sprintf("race-%d", i), fmt.Sprintf("race-%d", i))
		errs := make(chan error, 2)
		for range 2 {
			go func() { errs <- g.Receive(context.Background(), next) }()
		}
		accepted := false
		for range 2 {
			err := receive(t, errs)
			if err == nil {
				accepted = true
			} else if !errors.Is(err, ErrConversationClosing) {
				t.Fatal("unexpected race admission error", err)
			}
		}
		deadline := time.Now().Add(3 * time.Second)
		for !accepted {
			err := g.Receive(context.Background(), next)
			if err == nil {
				accepted = true
			} else if !errors.Is(err, ErrConversationClosing) || time.Now().After(deadline) {
				t.Fatal("retry did not make progress", err)
			} else {
				time.Sleep(50 * time.Microsecond)
			}
		}
		second := receive(t, runs) // Every successful admission MUST reach Run.
		h := second.history
		if len(h) != 2*(i+1) || h[len(h)-2].Content != msg.Text || h[len(h)-1].Content != next.Text {
			t.Fatal("accepted message lost, duplicated or reordered", i, h)
		}
		c := conversationFor(t, g, msg.Route) // Second Run is gated, never evictable.
		assertWriterHeld(t, store.Store, second.session.ID)
		close(second.release)
		receive(t, c.closed)
		if g.ConversationCount() != 0 {
			t.Fatal("race left zombie runtime", i)
		}
	}
	if err := g.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-runs:
		t.Fatal("message ran twice", duplicate.history)
	default:
	}
}
