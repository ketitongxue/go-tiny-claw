package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type runnerFunc func(context.Context, *ctxpkg.Session, engine.Reporter) error

func (fn runnerFunc) Run(ctx context.Context, sess *ctxpkg.Session, reporter engine.Reporter) error {
	return fn(ctx, sess, reporter)
}

func textEvent(chatID, messageID, text string) *larkim.P2MessageReceiveV1 {
	content, _ := json.Marshal(map[string]string{"text": text})
	kind, sender, serialized := "text", "user", string(content)
	return &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: "cli_test"}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender:  &larkim.EventSender{SenderType: &sender},
			Message: &larkim.EventMessage{ChatId: &chatID, MessageId: &messageID, MessageType: &kind, Content: &serialized},
		},
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for bot")
		var zero T
		return zero
	}
}

func TestBotConfigurationErrors(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "")
	t.Setenv("FEISHU_APP_SECRET", "secret-must-not-appear")
	_, err := NewAdapter()
	if err == nil || strings.Contains(err.Error(), "secret-must-not-appear") {
		t.Fatalf("invalid credential error: %v", err)
	}
	var unconfigured *Adapter
	if err := unconfigured.Start(context.Background(), nil); err == nil {
		t.Fatal("unconfigured adapter must return startup error, not exit")
	}
	t.Setenv("FEISHU_APP_ID", "cli_test")
	a, err := NewAdapter()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background(), nil); err == nil {
		t.Fatal("nil handler accepted")
	}
	// Runtime factories are now validated by Gateway, not by the adapter.
	if _, err := gateway.New(context.Background(), gateway.Config{Channels: channel.NewRegistry()}); err == nil {
		t.Fatal("nil runtime factory accepted")
	}
}

func TestReceiveDecodesMentionsFiltersAndDeduplicates(t *testing.T) {
	prompts := make(chan string, 8)
	b := testBot(t, func(*ctxpkg.Session, engine.Reporter) agentRunner {
		return runnerFunc(func(_ context.Context, sess *ctxpkg.Session, _ engine.Reporter) error {
			history := sess.GetWorkingMemory(0)
			prompts <- history[len(history)-1].Content
			return nil
		})
	})
	ignored := []*larkim.P2MessageReceiveV1{nil, {}, {Event: &larkim.P2MessageReceiveV1Data{}}}
	botEvent := textEvent("a", "bot", "ignore")
	bot := "bot"
	botEvent.Event.Sender.SenderType = &bot
	ignored = append(ignored, botEvent)
	imageEvent := textEvent("a", "image", "ignore")
	image := "image"
	imageEvent.Event.Message.MessageType = &image
	ignored = append(ignored, imageEvent)
	invalid := textEvent("a", "invalid", "ignore")
	badJSON := `{"text":`
	invalid.Event.Message.Content = &badJSON
	ignored = append(ignored, invalid)
	noContent := textEvent("a", "nil-content", "ignore")
	noContent.Event.Message.Content = nil
	ignored = append(ignored, noContent)
	wrongApp := textEvent("a", "wrong-app", "ignore")
	wrongApp.EventV2Base.Header.AppID = "other"
	ignored = append(ignored, wrongApp)
	for _, event := range ignored {
		if err := b.handleMessage(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	text := "@_user_1 检查\n\"C:\\tmp\" 的文件 😀"
	event := textEvent("a", "valid", text)
	key, mentionType, ownID := "@_user_1", "bot", "bot-self"
	b.botOpenID = ownID
	event.Event.Message.Mentions = []*larkim.MentionEvent{nil, {Key: &key, MentionedType: &mentionType, Id: &larkim.UserId{OpenId: &ownID}}}
	for i := 0; i < 2; i++ {
		if err := b.handleMessage(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.handleMessage(context.Background(), textEvent("a", "next", "sentinel")); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, prompts); got != strings.TrimSpace(strings.TrimPrefix(text, key)) {
		t.Fatalf("decoded text = %q", got)
	}
	if got := receive(t, prompts); got != "sentinel" {
		t.Fatalf("unexpected duplicate/ignored prompt: %q", got)
	}
}

func TestConversationsStayOrderedAndIndependent(t *testing.T) {
	type run struct {
		chat, prompt, dir, reporterChat string
		session                         *ctxpkg.Session
		historyLen                      int
	}
	runs := make(chan run, 8)
	release := make(chan struct{})
	var mu sync.Mutex
	factories := make(map[string]int)
	b := testBot(t, func(sess *ctxpkg.Session, reporter engine.Reporter) agentRunner {
		mu.Lock()
		factories[sess.ID]++
		mu.Unlock()
		return runnerFunc(func(ctx context.Context, current *ctxpkg.Session, actual engine.Reporter) error {
			history := current.GetWorkingMemory(0)
			prompt := history[len(history)-1].Content
			route := actual.(*delivery.ChannelReporter).Route()
			runs <- run{route.ChatID, prompt, current.WorkDir, route.ChatID, current, len(history)}
			if prompt == "first" {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			current.Append(ctx, schema.Message{Role: schema.RoleAssistant, Content: "response"})
			return nil
		})
	})
	if err := b.handleMessage(context.Background(), textEvent("a", "1", "first")); err != nil {
		t.Fatal(err)
	}
	first := receive(t, runs)
	if err := b.handleMessage(context.Background(), textEvent("a", "2", "second")); err != nil {
		t.Fatal(err)
	}
	if err := b.handleMessage(context.Background(), textEvent("b", "3", "independent")); err != nil {
		t.Fatal(err)
	}
	other := receive(t, runs)
	if other.chat != "b" || other.prompt != "independent" {
		t.Fatalf("second chat blocked or same-chat order broken: %+v", other)
	}
	if first.dir != b.workDir || first.reporterChat != "a" || other.reporterChat != "b" || first.session == other.session {
		t.Fatal("workspace/session/reporter isolation failed")
	}
	close(release)
	second := receive(t, runs)
	if second.prompt != "second" || second.session != first.session || second.historyLen != 3 {
		t.Fatalf("conversation continuity failed: %+v", second)
	}
	mu.Lock()
	defer mu.Unlock()
	if factories[channel.BuildSessionKey(feishuRoute("a"))] != 1 || factories[channel.BuildSessionKey(feishuRoute("b"))] != 1 {
		t.Fatalf("factory calls = %v", factories)
	}
}

func TestApprovalBypassesBusyChatQueue(t *testing.T) {
	manager := approval.NewApprovalManager()
	ready := make(chan struct{}, 1)
	results := make(chan string, 4)
	b := testBot(t, func(*ctxpkg.Session, engine.Reporter) agentRunner {
		return runnerFunc(func(ctx context.Context, sess *ctxpkg.Session, _ engine.Reporter) error {
			h := sess.GetWorkingMemory(0)
			if h[len(h)-1].Content == "danger" {
				allowed, _ := manager.Wait(ctx, approval.Request{SessionID: sess.ID, Route: feishuRoute("a"), TaskID: "task-one", ToolName: "bash", Args: "rm -r target"},
					approvalNotifierFunc(func(context.Context, approval.Request) error { ready <- struct{}{}; return nil }))
				results <- fmt.Sprintf("approved=%t", allowed)
			} else {
				results <- "next"
			}
			return nil
		})
	}, manager)
	b.botOpenID = "bot-self"
	own := botMention("@_user_1", "bot-self", "Our Bot")
	if err := b.handleMessage(context.Background(), groupEvent("a", "1", "@_user_1 danger", own)); err != nil {
		t.Fatal(err)
	}
	receive(t, ready)
	if err := b.handleMessage(context.Background(), groupEvent("a", "2", "@_user_1 queued", own)); err != nil {
		t.Fatal(err)
	}
	if err := b.handleMessage(context.Background(), groupEvent("b", "3", "approve task-one")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-results:
		t.Fatalf("wrong chat resolved approval: %s", got)
	default:
	}
	// Approvals stay available without mentions even if identity becomes unavailable.
	b.mu.Lock()
	b.botOpenID = ""
	b.mu.Unlock()
	approval := groupEvent("a", "4", "approve task-one")
	if err := b.handleMessage(context.Background(), approval); err != nil {
		t.Fatal(err)
	}
	if err := b.handleMessage(context.Background(), approval); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, results); got != "approved=true" {
		t.Fatalf("approval result = %s", got)
	}
	if got := receive(t, results); got != "next" {
		t.Fatalf("queue did not resume: %s", got)
	}
}

func TestReporterSendsLongUnicodeJSONAndHonorsCancellation(t *testing.T) {
	var mu sync.Mutex
	var received []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		var body struct {
			ReceiveID string `json:"receive_id"`
			Content   string `json:"content"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if body.ReceiveID != "chat-a" {
			t.Errorf("wrong destination: %q", body.ReceiveID)
		}
		if len(body.Content) > maxMessageContentBytes {
			t.Errorf("oversize content: %d", len(body.Content))
		}
		var content struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
			t.Error(err)
		}
		if !utf8.ValidString(content.Text) {
			t.Error("broken unicode")
		}
		mu.Lock()
		received = append(received, content.Text)
		mu.Unlock()
		fmt.Fprint(w, `{"code":0,"data":{"message_id":"test-message"}}`)
	})
	client := &http.Client{Transport: botRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Result(), nil
	})}
	adapter := &Adapter{client: lark.NewClient("reporter-app", "secret", lark.WithHttpClient(client), lark.WithEnableTokenCache(false))}
	text := strings.Repeat("中文😀\n\"quoted\"\\path <>&\u2028\x01", 1500)
	if err := adapter.Send(context.Background(), feishuRoute("chat-a"), channel.OutboundMessage{Kind: channel.OutboundText, Text: text}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(received) < 2 || strings.Join(received, "") != text {
		t.Fatal("split text was truncated or changed")
	}
	before := len(received)
	mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := adapter.Send(ctx, feishuRoute("chat-a"), channel.OutboundMessage{Kind: channel.OutboundText, Text: "must not send"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled send returned %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != before {
		t.Fatal("canceled send issued HTTP request")
	}
}

type fakeConnection struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (c *fakeConnection) Start(context.Context) error { close(c.started); <-c.closed; return nil }
func (c *fakeConnection) Close()                      { c.once.Do(func() { close(c.closed) }) }

func TestConnectionCancellationStopsWorkers(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	cancelSeen, cleanupRelease := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-cleanupRelease:
		default:
			close(cleanupRelease)
		}
	}()
	b := testBot(t, func(*ctxpkg.Session, engine.Reporter) agentRunner {
		return runnerFunc(func(ctx context.Context, _ *ctxpkg.Session, _ engine.Reporter) error {
			close(started)
			<-ctx.Done()
			close(cancelSeen)
			<-cleanupRelease
			close(stopped)
			return ctx.Err()
		})
	})
	if err := b.handleMessage(context.Background(), textEvent("a", "1", "run")); err != nil {
		t.Fatal(err)
	}
	receive(t, started)
	connection := &fakeConnection{started: make(chan struct{}), closed: make(chan struct{})}
	b.platform.connection = connection
	done := make(chan error, 1)
	go func() { done <- b.gateway.Run() }()
	receive(t, connection.started)
	b.Close()
	receive(t, cancelSeen)
	select {
	case err := <-done:
		t.Fatalf("serve returned before worker cleanup: %v", err)
	default:
	}
	close(cleanupRelease)
	if err := receive(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error: %v", err)
	}
	select {
	case <-connection.closed:
	default:
		t.Fatal("serve returned before connection closed")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("serve returned before worker cleanup")
	}
}

// Exercise the actual SDK request encoding without a network listener.
type botRoundTripFunc func(*http.Request) (*http.Response, error)

func (f botRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFullChatQueueDoesNotBlockOrDeduplicateRejectedMessage(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	processed := make(chan string, gateway.ConversationQueueSize+4)
	b := testBot(t, func(*ctxpkg.Session, engine.Reporter) agentRunner {
		return runnerFunc(func(ctx context.Context, sess *ctxpkg.Session, _ engine.Reporter) error {
			h := sess.GetWorkingMemory(0)
			prompt := h[len(h)-1].Content
			if prompt == "blocked" {
				close(started)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
				}
			}
			processed <- prompt
			return nil
		})
	})
	if err := b.handleMessage(context.Background(), textEvent("a", "blocked", "blocked")); err != nil {
		t.Fatal(err)
	}
	receive(t, started)
	for i := 0; i < gateway.ConversationQueueSize; i++ {
		id := fmt.Sprintf("queued-%d", i)
		if err := b.handleMessage(context.Background(), textEvent("a", id, id)); err != nil {
			t.Fatal(err)
		}
	}
	overflow := textEvent("a", "overflow", "overflow")
	returned := make(chan error, 1)
	go func() { returned <- b.handleMessage(context.Background(), overflow) }()
	if err := receive(t, returned); err == nil {
		t.Fatal("overflow should be rejected for later retry")
	}
	close(release)
	for i := 0; i <= gateway.ConversationQueueSize; i++ {
		receive(t, processed)
	}
	if err := b.handleMessage(context.Background(), overflow); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, processed); got != "overflow" {
		t.Fatalf("retry was lost: %q", got)
	}
}

func TestReporterPropagatesAPIErrorWithoutResponseBody(t *testing.T) {
	client := &http.Client{Transport: botRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
		} else {
			fmt.Fprint(w, `{"code":99991672,"msg":"raw-payload-must-not-be-logged"}`)
		}
		return w.Result(), nil
	})}
	adapter := &Adapter{client: lark.NewClient("failing-app", "secret", lark.WithHttpClient(client), lark.WithEnableTokenCache(false))}
	err := adapter.Send(context.Background(), feishuRoute("a"), channel.OutboundMessage{Kind: channel.OutboundText, Text: "approval notice"})
	if err == nil || !strings.Contains(err.Error(), "99991672") || strings.Contains(err.Error(), "raw-payload") {
		t.Fatalf("unexpected send error: %v", err)
	}
}

func groupEvent(chatID, messageID, text string, mentions ...*larkim.MentionEvent) *larkim.P2MessageReceiveV1 {
	event := textEvent(chatID, messageID, text)
	group := "group"
	event.Event.Message.ChatType = &group
	event.Event.Message.Mentions = mentions
	return event
}

func botMention(key, openID, name string) *larkim.MentionEvent {
	kind := "bot"
	return &larkim.MentionEvent{Key: &key, Id: &larkim.UserId{OpenId: &openID}, Name: &name, MentionedType: &kind}
}

func botIdentityClient(t *testing.T, status int, payload string) *lark.Client {
	t.Helper()
	client := &http.Client{Transport: botRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"identity-token","expire":7200}`)
		} else {
			if req.URL.Path != "/open-apis/bot/v3/info" || req.Method != http.MethodGet {
				t.Errorf("incorrect bot identity API: %s %s", req.Method, req.URL.Path)
			}
			if req.Header.Get("Authorization") != "Bearer identity-token" {
				t.Error("identity API must use tenant token")
			}
			deadline, ok := req.Context().Deadline()
			if !ok || time.Until(deadline) > 10*time.Second {
				t.Error("identity bootstrap must have a bounded deadline")
			}
			w.WriteHeader(status)
			fmt.Fprint(w, payload)
		}
		return w.Result(), nil
	})}
	return lark.NewClient("identity-"+t.Name(), "secret", lark.WithHttpClient(client), lark.WithEnableTokenCache(true))
}

func TestGroupRequiresOwnBotMentionAndPreservesOthers(t *testing.T) {
	prompts := make(chan string, 4)
	b := testBot(t, func(*ctxpkg.Session, engine.Reporter) agentRunner {
		return runnerFunc(func(_ context.Context, sess *ctxpkg.Session, _ engine.Reporter) error {
			h := sess.GetWorkingMemory(0)
			prompts <- h[len(h)-1].Content
			return nil
		})
	})
	own := botMention("@_user_1", "bot-self", "Our Bot")
	other := botMention("@_user_10", "other-bot", "Other Bot")
	assertIgnored := func(event *larkim.P2MessageReceiveV1) {
		t.Helper()
		if err := b.handleMessage(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if b.gateway.ConversationCount() != 0 {
			t.Fatal("group event created a conversation without verified own-bot mention")
		}
	}
	// A bot-typed mention alone cannot establish identity.
	assertIgnored(groupEvent("group", "unknown", "@_user_1 task", own))
	b.client = botIdentityClient(t, http.StatusOK, `{"code":0,"bot":{"open_id":"bot-self","app_name":"Our Bot"}}`)
	if err := b.loadBotIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIgnored(groupEvent("group", "ordinary", "ordinary group chatter"))
	assertIgnored(groupEvent("group", "other", "@_user_10 task", other))
	assertIgnored(groupEvent("group", "metadata-only", "ordinary group chatter", own))
	user := botMention("@_user_2", "user-two", "Alice")
	kind := "user"
	user.MentionedType = &kind
	if err := b.handleMessage(context.Background(), groupEvent("group", "self", "@_user_1 ask @_user_10 and @_user_2", own, other, user)); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, prompts); got != "ask @Other Bot and @Alice" {
		t.Fatalf("mention cleaning changed other targets: %q", got)
	}
	if err := b.handleMessage(context.Background(), textEvent("p2p", "direct", "direct message")); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, prompts); got != "direct message" {
		t.Fatalf("p2p changed: %q", got)
	}
}

func TestBotIdentityFailureKeepsGroupsClosedAndP2PAvailable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		payload string
	}{
		{"http", http.StatusForbidden, `{"msg":"private-response-body"}`},
		{"api", http.StatusOK, `{"code":99991672,"msg":"private-response-body"}`},
		{"malformed", http.StatusOK, `private-response-body`},
		{"empty-id", http.StatusOK, `{"code":0,"bot":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompts := make(chan string, 1)
			b := testBot(t, func(*ctxpkg.Session, engine.Reporter) agentRunner {
				return runnerFunc(func(_ context.Context, sess *ctxpkg.Session, _ engine.Reporter) error {
					h := sess.GetWorkingMemory(0)
					prompts <- h[len(h)-1].Content
					return nil
				})
			})
			b.client = botIdentityClient(t, tc.status, tc.payload)
			err := b.loadBotIdentity(context.Background())
			if err == nil || strings.Contains(err.Error(), "private-response-body") {
				t.Fatalf("unsafe or absent bootstrap error: %v", err)
			}
			if err := b.handleMessage(context.Background(), groupEvent("group", "group-task", "@_user_1 task", botMention("@_user_1", "bot-self", "Our Bot"))); err != nil {
				t.Fatal(err)
			}
			if err := b.handleMessage(context.Background(), textEvent("p2p", "direct", "direct")); err != nil {
				t.Fatal(err)
			}
			if got := receive(t, prompts); got != "direct" {
				t.Fatalf("unverified group executed: %q", got)
			}
			if b.gateway.ConversationCount() != 1 {
				t.Fatal("unverified group conversation was created")
			}
		})
	}
}
