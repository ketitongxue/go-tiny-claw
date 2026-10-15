package telegram

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

func normalize(u update, bot user) (channel.InboundMessage, bool) {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot || strings.TrimSpace(m.Text) == "" || m.Chat.ID == 0 {
		return channel.InboundMessage{}, false
	}
	var chatType channel.ChatType
	switch m.Chat.Type {
	case "private":
		chatType = channel.ChatDirect
	case "group", "supergroup":
		chatType = channel.ChatGroup
	default:
		return channel.InboundMessage{}, false
	}
	text, mentioned := cleanMentions(m.Text, m.Entities, bot)
	if text == "" {
		return channel.InboundMessage{}, false
	}
	if reply := m.ReplyToMessage; reply != nil && reply.From != nil && reply.From.IsBot && reply.From.ID == bot.ID {
		mentioned = true
	}
	return channel.InboundMessage{
		// message_id is chat-local. Gateway dedup is channel/account scoped.
		ID:     strconv.FormatInt(u.UpdateID, 10),
		Route:  channel.Route{Channel: "telegram", AccountID: "default", ChatID: strconv.FormatInt(m.Chat.ID, 10)},
		UserID: strconv.FormatInt(m.From.ID, 10), UserName: m.From.Username,
		ChatType: chatType, Text: text, MentionedBot: mentioned, ReceivedAt: time.Now(),
	}, true
}

func cleanMentions(text string, entities []messageEntity, bot user) (string, bool) {
	type span struct{ start, end int }
	var own []span
	for _, entity := range entities {
		if entity.Type != "mention" && entity.Type != "text_mention" {
			continue
		}
		start, end, ok := utf16Range(text, entity.Offset, entity.Length)
		if !ok {
			continue
		}
		matches := entity.Type == "mention" && bot.Username != "" && strings.EqualFold(text[start:end], "@"+bot.Username)
		if entity.Type == "text_mention" && entity.User != nil && bot.ID != 0 && entity.User.ID == bot.ID {
			matches = true
		}
		if matches {
			own = append(own, span{start, end})
		}
	}
	sort.Slice(own, func(i, j int) bool { return own[i].start < own[j].start })
	var cleaned strings.Builder
	cursor := 0
	for _, s := range own {
		if s.start < cursor { // Duplicate/overlapping metadata cannot corrupt text.
			continue
		}
		cleaned.WriteString(text[cursor:s.start])
		cursor = s.end
	}
	cleaned.WriteString(text[cursor:])
	return strings.TrimSpace(cleaned.String()), len(own) != 0
}

// utf16Range converts Telegram entity code-unit offsets to UTF-8 byte bounds.
// Invalid bounds, overflows and boundaries inside a surrogate pair are rejected.
func utf16Range(text string, offset, length int) (int, int, bool) {
	end := offset + length
	if offset < 0 || length <= 0 || end <= offset {
		return 0, 0, false
	}
	startByte, ok := utf16ByteOffset(text, offset)
	if !ok {
		return 0, 0, false
	}
	endByte, ok := utf16ByteOffset(text, end)
	return startByte, endByte, ok
}

func utf16ByteOffset(text string, target int) (int, bool) {
	units := 0
	for byteOffset, r := range text {
		if units == target {
			return byteOffset, true
		}
		if units > target {
			return 0, false
		}
		units++
		if r > 0xFFFF {
			units++
		}
	}
	return len(text), units == target
}
