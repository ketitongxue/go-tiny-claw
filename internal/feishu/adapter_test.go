package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestAdapterNormalizesRouteMetadataAndForwardsControlWithoutParsing(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "cli_test")
	t.Setenv("FEISHU_APP_SECRET", "test")
	a, err := NewAdapter()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan channel.InboundMessage, 4)
	a.ctx, a.botOpenID = ctx, "bot-self"
	a.handler = func(actual context.Context, msg channel.InboundMessage) error {
		if actual != ctx {
			t.Error("callback context replaced")
		}
		received <- msg
		return nil
	}
	own := botMention("@_user_1", "bot-self", "Our Bot")
	user := botMention("@_user_10", "user-two", "张三")
	event := groupEvent("chat", "m1", "@_user_1 帮我看看 @_user_10 的代码", own, user)
	thread, userID := "ignored-current-topic", "user-one"
	event.Event.Message.ThreadId = &thread
	event.Event.Sender.SenderId = &larkim.UserId{OpenId: &userID}
	before := time.Now()
	if err := a.handleMessage(ctx, event); err != nil {
		t.Fatal(err)
	}
	msg := receive(t, received)
	if msg.Route != feishuRoute("chat") || msg.ID != "m1" || msg.UserID != userID || msg.Text != "帮我看看 @张三 的代码" || msg.ChatType != channel.ChatGroup || !msg.MentionedBot || msg.ReceivedAt.Before(before) {
		t.Fatalf("incorrect normalization: %+v", msg)
	}
	// The adapter forwards both commands and unmentioned chatter as normalized
	// input. Core routes commands before applying ordinary group admission.
	for i, text := range []string{"approve task123", "reject task123", "ordinary"} {
		if err := a.handleMessage(ctx, groupEvent("chat", string(rune('a'+i)), text)); err != nil {
			t.Fatal(err)
		}
		if got := receive(t, received); got.Text != text || got.MentionedBot {
			t.Fatal(got)
		}
	}
	a.handler = func(context.Context, channel.InboundMessage) error { return gateway.ErrQueueFull }
	if err := a.handleMessage(ctx, textEvent("chat", "overflow", "retry")); !errors.Is(err, gateway.ErrQueueFull) {
		t.Fatal("adapter swallowed retry error", err)
	}
	cancel()
	if err := a.handleMessage(ctx, textEvent("chat", "late", "late")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
