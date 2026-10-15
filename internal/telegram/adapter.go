package telegram

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

const maxMessageRunes = 4096

// Adapter owns only Telegram transport and normalization. Start is restartable;
// the mutex guards lifecycle admission, while offset belongs to the active Start.
type Adapter struct {
	client  *client
	mu      sync.Mutex
	started bool
	offset  int64 // Survives supervised reconnects; not a durable-session cursor.
}

func NewAdapter() (*Adapter, error) {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		return nil, channel.Permanent(errors.New("请设置 TELEGRAM_BOT_TOKEN"))
	}
	return &Adapter{client: newClient(token)}, nil
}

func (*Adapter) ID() string { return "telegram" }

func (a *Adapter) Start(ctx context.Context, handler channel.InboundHandler) error {
	if a == nil || a.client == nil || a.client.http == nil {
		return channel.Permanent(errors.New("telegram adapter is not initialized"))
	}
	if handler == nil {
		return channel.Permanent(errors.New("telegram adapter requires an inbound handler"))
	}
	if err := validateToken(a.client.token); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return channel.Permanent(errors.New("telegram adapter already started"))
	}
	a.started = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.started = false
		a.mu.Unlock()
	}()

	bot, err := a.client.getMe(ctx)
	if err != nil {
		return err
	}
	if !bot.IsBot || bot.ID == 0 || strings.TrimSpace(bot.Username) == "" {
		return channel.Permanent(errors.New("telegram getMe: unsupported bot identity"))
	}
	webhook, err := a.client.getWebhookInfo(ctx)
	if err != nil {
		return err
	}
	if webhook.URL != "" {
		// Do not silently mutate remote settings or discard pending updates.
		return channel.Permanent(errors.New("Telegram webhook is configured; long polling cannot start; remove the webhook explicitly"))
	}
	log.Print("[Telegram] connected; long polling started")
	for {
		updates, err := a.client.getUpdates(ctx, a.offset)
		if err != nil {
			return err
		}
		for _, u := range updates {
			if err := ctx.Err(); err != nil {
				return err
			}
			if u.UpdateID < a.offset {
				continue
			}
			if msg, ok := normalize(u, bot); ok {
				if err := handler(ctx, msg); err != nil {
					// Never acknowledge a rejected message (e.g. queue full).
					return err
				}
			}
			// Ignored media/bot events are acknowledged too, avoiding a poison loop.
			a.offset = u.UpdateID + 1
		}
	}
}

func (a *Adapter) Send(ctx context.Context, target channel.Route, msg channel.OutboundMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if a == nil || a.client == nil || a.client.http == nil {
		return errors.New("telegram sender is not initialized")
	}
	if target.Channel != a.ID() || target.AccountID != "default" || target.ThreadID != "" {
		return errors.New("unsupported telegram destination")
	}
	for _, chunk := range splitMessage(msg.Text, maxMessageRunes) {
		if err := a.client.sendMessage(ctx, target.ChatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// splitMessage preserves text exactly and prefers a newline in the latter half
// of the window. Otherwise it splits at the rune limit, never inside UTF-8.
func splitMessage(text string, limit int) []string {
	if text == "" {
		return nil
	}
	limit = max(limit, 1)
	runes := []rune(text)
	var chunks []string
	for len(runes) > limit {
		end := limit
		for i := limit - 1; i >= limit/2; i-- {
			if runes[i] == '\n' {
				end = i + 1
				break
			}
		}
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

var _ channel.Adapter = (*Adapter)(nil)
