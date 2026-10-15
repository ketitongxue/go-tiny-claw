package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Telegram")
		var zero T
		return zero
	}
}

func apiResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func TestStartPreservesOffsetAcrossRejectionAndPollingFailure(t *testing.T) {
	polls := make(chan int64, 4)
	var pollCalls atomic.Int32
	a := httpAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected JSON POST")
		}
		switch r.URL.Path {
		case "/bot123:test-secret/getMe":
			apiResult(w, testBot)
		case "/bot123:test-secret/getWebhookInfo":
			apiResult(w, webhookInfo{})
		case "/bot123:test-secret/getUpdates":
			var req struct {
				Offset         int64    `json:"offset"`
				Timeout        int      `json:"timeout"`
				Limit          int      `json:"limit"`
				AllowedUpdates []string `json:"allowed_updates"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Timeout != 30 || req.Limit != 100 || len(req.AllowedUpdates) != 1 || req.AllowedUpdates[0] != "message" {
				t.Error("incorrect long-poll request", req)
			}
			polls <- req.Offset
			switch pollCalls.Add(1) {
			case 1:
				bot := textUpdate(101, 1, 42, "private", "ignored bot")
				bot.Message.From.IsBot = true
				apiResult(w, []update{textUpdate(100, 1, 42, "private", "first"), bot, {UpdateID: 102}, textUpdate(103, 1, 43, "private", "retry me")})
			case 2:
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"ok":false,"error_code":503}`)
			case 3:
				apiResult(w, []update{textUpdate(103, 1, 43, "private", "retry me"), textUpdate(103, 1, 43, "private", "retry me"), {UpdateID: 104}})
			default:
				<-r.Context().Done()
			}
		default:
			t.Error("unexpected API method")
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rejected := errors.New("queue full")
	received := make(chan channel.InboundMessage, 5)
	rejectOnce := true
	handler := func(_ context.Context, msg channel.InboundMessage) error {
		received <- msg
		if msg.ID == "103" && rejectOnce {
			rejectOnce = false
			return rejected
		}
		return nil
	}
	if err := a.Start(ctx, handler); !errors.Is(err, rejected) {
		t.Fatal("handler rejection swallowed", err)
	}
	if got := await(t, polls); got != 0 || a.offset != 103 {
		t.Fatal("rejected update acknowledged", got, a.offset)
	}
	if err := a.Start(ctx, handler); err == nil || channel.IsPermanent(err) || a.offset != 103 {
		t.Fatal("polling failure was not retryable or changed offset", err, a.offset)
	}
	if got := await(t, polls); got != 103 {
		t.Fatal("restart acknowledged rejected update", got)
	}
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx, handler) }()
	for _, want := range []int64{103, 105} {
		if got := await(t, polls); got != want {
			t.Fatal("offset lost across restart/ignored update", got, want)
		}
	}
	// Send and lifecycle admission may race with an active poll, never offset.
	if err := a.Start(ctx, handler); !channel.IsPermanent(err) {
		t.Fatal("concurrent Start accepted", err)
	}
	cancel()
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, want := range []string{"100", "103", "103"} {
		if got := await(t, received); got.ID != want {
			t.Fatal(got)
		}
	}
	select {
	case msg := <-received:
		t.Fatal("duplicate or ignored update forwarded", msg)
	default:
	}
}

func TestStartRejectsWebhookWithoutChangingRemoteConfiguration(t *testing.T) {
	var calls atomic.Int32
	a := httpAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			apiResult(w, testBot)
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			apiResult(w, webhookInfo{URL: "https://private-webhook.invalid/test-secret"})
		default:
			t.Error("adapter must not delete webhook or start polling")
		}
	})
	err := a.Start(context.Background(), func(context.Context, channel.InboundMessage) error { return nil })
	if !channel.IsPermanent(err) || !strings.Contains(err.Error(), "webhook") || strings.Contains(err.Error(), "test-secret") || calls.Load() != 2 {
		t.Fatal("webhook conflict not surfaced safely", err, calls.Load())
	}
}

func TestStartRejectsUnsupportedIdentity(t *testing.T) {
	a := httpAdapter(t, func(w http.ResponseWriter, r *http.Request) { apiResult(w, user{ID: 123}) })
	if err := a.Start(context.Background(), func(context.Context, channel.InboundMessage) error { return nil }); !channel.IsPermanent(err) {
		t.Fatal("non-bot account accepted", err)
	}
}

func TestStartCancellationInterruptsBootstrapAndLongPoll(t *testing.T) {
	for _, blockedMethod := range []string{"getMe", "getUpdates"} {
		t.Run(blockedMethod, func(t *testing.T) {
			blocked := make(chan struct{})
			a := httpAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				// Read the body so the HTTP server observes disconnect promptly.
				var body any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if strings.HasSuffix(r.URL.Path, "/"+blockedMethod) {
					close(blocked)
					<-r.Context().Done()
					return
				}
				if strings.HasSuffix(r.URL.Path, "/getMe") {
					apiResult(w, testBot)
				} else {
					apiResult(w, webhookInfo{})
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- a.Start(ctx, func(context.Context, channel.InboundMessage) error { return nil }) }()
			await(t, blocked)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancel did not interrupt request")
			}
		})
	}
}

func TestSendPlainTextUnicodeChunksAndConcurrentRequests(t *testing.T) {
	sent := make(chan string, 64)
	a := httpAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") || len(req) != 2 {
			t.Error("unexpected API method or non-plain-text fields", req)
		}
		var chatID, text string
		_ = json.Unmarshal(req["chat_id"], &chatID)
		_ = json.Unmarshal(req["text"], &text)
		if chatID != "-100123" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxMessageRunes {
			t.Error("invalid chunk", chatID, len(text))
		}
		sent <- text
		apiResult(w, message{MessageID: 1})
	})
	route := channel.Route{Channel: "telegram", AccountID: "default", ChatID: "-100123"}
	text := strings.Repeat("中😀e", 1200) + "\n" + strings.Repeat("x", 2000) + " **literal** _Markdown_"
	if err := a.Send(context.Background(), route, channel.OutboundMessage{Text: text}); err != nil {
		t.Fatal(err)
	}
	chunks := []string{await(t, sent), await(t, sent)}
	if strings.Join(chunks, "") != text || !strings.HasSuffix(chunks[0], "\n") {
		t.Fatal("chunking lost text or ignored a nearby newline")
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.Send(context.Background(), route, channel.OutboundMessage{Text: fmt.Sprint(i)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for range 16 {
		await(t, sent)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Send(ctx, route, channel.OutboundMessage{Text: "never send"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, target := range []channel.Route{{}, {Channel: "feishu", AccountID: "default", ChatID: "1"}, {Channel: "telegram", AccountID: "other", ChatID: "1"}, {Channel: "telegram", AccountID: "default", ChatID: "1", ThreadID: "topic"}} {
		if err := a.Send(context.Background(), target, channel.OutboundMessage{Text: "never send"}); err == nil {
			t.Fatal("unsupported route accepted", target)
		}
	}
	if err := a.Send(context.Background(), route, channel.OutboundMessage{}); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-sent:
		t.Fatal("invalid/empty send reached Telegram", text)
	default:
	}
}

func TestSplitMessageRuneBoundaries(t *testing.T) {
	for _, text := range []string{"", "a", strings.Repeat("😀", 8193), strings.Repeat("中文e\n", 3000), strings.Repeat("x", 4096)} {
		chunks := splitMessage(text, maxMessageRunes)
		if strings.Join(chunks, "") != text {
			t.Fatal("text corrupted")
		}
		for _, chunk := range chunks {
			if chunk == "" || !utf8.ValidString(chunk) || utf8.RuneCountInString(chunk) > maxMessageRunes {
				t.Fatal("invalid chunk")
			}
		}
	}
}
