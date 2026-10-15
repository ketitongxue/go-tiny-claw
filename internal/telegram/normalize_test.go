package telegram

import (
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

var testBot = user{ID: 123, IsBot: true, Username: "claw_bot"}

func textUpdate(updateID, messageID, chatID int64, chatType, text string) update {
	return update{UpdateID: updateID, Message: &message{MessageID: messageID,
		From: &user{ID: 456, Username: "alice"}, Chat: chat{ID: chatID, Type: chatType}, Text: text}}
}

func TestNormalizeTextAndMentions(t *testing.T) {
	for _, tc := range []struct {
		name, chatType, text, want string
		entities                   []messageEntity
		reply                      *message
		mentioned                  bool
	}{
		{name: "private", chatType: "private", text: " hello ", want: "hello"},
		{name: "group-self", chatType: "group", text: "@claw_bot 帮我看看 @zhangsan", want: "帮我看看 @zhangsan",
			entities: []messageEntity{{Type: "mention", Offset: 0, Length: 9}, {Type: "mention", Offset: 15, Length: 9}}, mentioned: true},
		{name: "group-no-mention", chatType: "group", text: "ordinary", want: "ordinary"},
		{name: "metadata-required", chatType: "group", text: "@claw_bot task", want: "@claw_bot task"},
		{name: "other-mention", chatType: "supergroup", text: "@otherbot task", want: "@otherbot task",
			entities: []messageEntity{{Type: "mention", Offset: 0, Length: 9}}},
		{name: "reply-self", chatType: "supergroup", text: "continue", want: "continue",
			reply: &message{From: &testBot}, mentioned: true},
		{name: "reply-other-bot", chatType: "group", text: "continue", want: "continue",
			reply: &message{From: &user{ID: 999, IsBot: true}}},
		{name: "emoji", chatType: "group", text: "😀 @claw_bot hi", want: "😀  hi",
			entities: []messageEntity{{Type: "mention", Offset: 3, Length: 9}}, mentioned: true},
		{name: "chinese", chatType: "group", text: "你好 @claw_bot 分析", want: "你好  分析",
			entities: []messageEntity{{Type: "mention", Offset: 3, Length: 9}}, mentioned: true},
		{name: "emoji-chinese", chatType: "supergroup", text: "😀中文 @CLAW_BOT 分析", want: "😀中文  分析",
			entities: []messageEntity{{Type: "mention", Offset: 5, Length: 9}}, mentioned: true},
		{name: "text-mention", chatType: "group", text: "小爪 task", want: "task",
			entities: []messageEntity{{Type: "text_mention", Offset: 0, Length: 2, User: &testBot}}, mentioned: true},
		{name: "invalid-surrogate-bound", chatType: "group", text: "😀 @claw_bot hi", want: "😀 @claw_bot hi",
			entities: []messageEntity{{Type: "mention", Offset: 1, Length: 9}}},
		{name: "negative-offset", chatType: "group", text: "@claw_bot hi", want: "@claw_bot hi",
			entities: []messageEntity{{Type: "mention", Offset: -1, Length: 9}}},
		{name: "control-approve", chatType: "group", text: "approve task", want: "approve task"},
		{name: "control-reject", chatType: "group", text: "reject task", want: "reject task"},
		{name: "multiple-unordered", chatType: "group", text: "@claw_bot task @claw_bot", want: "task",
			entities: []messageEntity{{Type: "mention", Offset: 15, Length: 9}, {Type: "mention", Offset: 0, Length: 9}, {Type: "mention", Offset: 0, Length: 9}}, mentioned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := textUpdate(987, 1, -100123, tc.chatType, tc.text)
			u.Message.Entities, u.Message.ReplyToMessage = tc.entities, tc.reply
			before := time.Now()
			got, ok := normalize(u, testBot)
			wantType := channel.ChatGroup
			if tc.chatType == "private" {
				wantType = channel.ChatDirect
			}
			if !ok || got.Text != tc.want || got.MentionedBot != tc.mentioned || got.ChatType != wantType {
				t.Fatalf("unexpected normalization: %+v, %t", got, ok)
			}
			if got.ID != "987" || got.Route != (channel.Route{Channel: "telegram", AccountID: "default", ChatID: "-100123"}) || got.UserID != "456" || got.UserName != "alice" || got.ReceivedAt.Before(before) {
				t.Fatal("route/metadata changed", got)
			}
		})
	}
}

func TestNormalizeIgnoresUnsupportedUpdates(t *testing.T) {
	bot := textUpdate(1, 2, 3, "private", "bot chatter")
	bot.Message.From.IsBot = true
	missingSender := textUpdate(1, 2, 3, "private", "anonymous")
	missingSender.Message.From = nil
	mentionOnly := textUpdate(1, 2, 3, "group", "@claw_bot")
	mentionOnly.Message.Entities = []messageEntity{{Type: "mention", Length: 9}}
	for _, u := range []update{
		{}, bot, missingSender, mentionOnly,
		textUpdate(1, 2, 3, "private", ""),
		textUpdate(1, 2, 3, "private", "  \n"),
		textUpdate(1, 2, 3, "channel", "channel post"),
		textUpdate(1, 2, 0, "private", "bad chat"),
	} {
		if got, ok := normalize(u, testBot); ok {
			t.Fatal("unsupported event forwarded", got)
		}
	}
}

func TestInboundIDUsesUpdateIDNotChatLocalMessageID(t *testing.T) {
	a, _ := normalize(textUpdate(100, 1, 42, "private", "first"), testBot)
	b, _ := normalize(textUpdate(101, 1, 43, "private", "second"), testBot)
	retry, _ := normalize(textUpdate(100, 1, 42, "private", "first"), testBot)
	if a.ID == b.ID || a.ID != retry.ID || a.ID != "100" || b.ID != "101" {
		t.Fatal("chat-local IDs used for dedup", a.ID, b.ID, retry.ID)
	}
}

func TestUTF16EntityRange(t *testing.T) {
	text := "a中😀b"
	for _, tc := range []struct {
		offset, length int
		want           string
		ok             bool
	}{
		{0, 1, "a", true}, {1, 1, "中", true}, {2, 2, "😀", true},
		{4, 1, "b", true}, {1, 3, "中😀", true}, {0, 5, text, true},
		{3, 1, "", false}, {2, 1, "", false}, {5, 1, "", false},
		{-1, 2, "", false}, {0, 0, "", false}, {0, -1, "", false},
		{int(^uint(0) >> 1), 10, "", false},
	} {
		start, end, ok := utf16Range(text, tc.offset, tc.length)
		if ok != tc.ok || ok && text[start:end] != tc.want {
			t.Fatalf("utf16Range(%d,%d) = %d,%d,%t", tc.offset, tc.length, start, end, ok)
		}
	}
}
