package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// Leave room for surrounding API JSON, including a second layer of escaping.
const maxMessageContentBytes = 8 * 1024

// Adapter owns only platform connection, identity, event normalization and send.
// Sessions, engines, queues, deduplication and control commands belong to Gateway.
type Adapter struct {
	client    *lark.Client
	appID     string
	appSecret string
	mu        sync.RWMutex
	ctx       context.Context
	handler   channel.InboundHandler
	started   bool
	botOpenID string // empty disables normal group tasks
}

func NewAdapter() (*Adapter, error) {
	appID := strings.TrimSpace(os.Getenv("FEISHU_APP_ID"))
	appSecret := strings.TrimSpace(os.Getenv("FEISHU_APP_SECRET"))
	if appID == "" || appSecret == "" {
		return nil, channel.Permanent(errors.New("请设置 FEISHU_APP_ID 和 FEISHU_APP_SECRET"))
	}
	return &Adapter{appID: appID, appSecret: appSecret, client: lark.NewClient(appID, appSecret,
		lark.WithLogLevel(larkcore.LogLevelInfo), lark.WithReqTimeout(10*time.Second), lark.WithEnableTokenCache(true))}, nil
}

func (*Adapter) ID() string { return "feishu" }

func (b *Adapter) GetEventDispatcher() *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(b.handleMessage).
		OnP2MessageReadV1(func(context.Context, *larkim.P2MessageReadV1) error { return nil })
}

func (b *Adapter) Start(ctx context.Context, handler channel.InboundHandler) error {
	if b == nil || b.client == nil {
		return channel.Permanent(errors.New("飞书 Adapter 未初始化"))
	}
	if handler == nil {
		return channel.Permanent(errors.New("飞书 Adapter 需要消息处理器"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return channel.Permanent(errors.New("飞书 Adapter 已启动"))
	}
	b.started, b.ctx, b.handler = true, ctx, handler
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.started, b.ctx, b.handler = false, nil, nil
		b.mu.Unlock()
	}()
	if err := b.loadBotIdentity(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("[Feishu] 无法确认机器人身份，群聊任务暂不可用；私聊继续: %v", err)
	}
	failed := make(chan error, 1)
	signalFailure := func(err error) {
		select {
		case failed <- classifyConnectionError(err):
		default:
		}
	}
	client := larkws.NewClient(b.appID, b.appSecret,
		larkws.WithEventHandler(b.GetEventDispatcher()),
		// Gateway owns retry timing. Surface post-connect disconnects too: the
		// pinned SDK's Start otherwise remains parked after a disconnect.
		larkws.WithAutoReconnect(false),
		larkws.WithOnError(signalFailure),
		larkws.WithOnDisconnected(func() { signalFailure(errors.New("connection closed")) }),
		// SDK info logs include connection URLs; keep tickets out of logs.
		larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithOnReady(func() { log.Print("[Feishu] 长连接已建立，等待消息") }),
	)
	return classifyConnectionError(b.serveConnectionUntil(ctx, client, failed))
}

func classifyConnectionError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || channel.IsPermanent(err) {
		return err
	}
	var rejected *larkws.ClientError
	if errors.As(err, &rejected) {
		return channel.Permanent(fmt.Errorf("飞书长连接配置/认证失败 (code=%d)", rejected.Code))
	}
	// SDK transport errors may contain connection URLs/tickets.
	return errors.New("飞书长连接中断或请求失败")
}

// The pinned SDK exposes bot/v3/info via authenticated raw Get. Identity, not
// MentionedType alone, distinguishes our bot from other group mentions.
func (b *Adapter) loadBotIdentity(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b.mu.Lock()
	b.botOpenID = ""
	b.mu.Unlock()
	resp, err := b.client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("获取飞书机器人身份请求失败")
	}
	if resp == nil {
		return errors.New("飞书机器人身份响应为空")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("飞书机器人身份请求失败 (HTTP %d)", resp.StatusCode)
	}
	var result struct {
		Code int `json:"code"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return errors.New("飞书机器人身份响应格式无效")
	}
	if result.Code != 0 {
		return fmt.Errorf("获取飞书机器人身份失败 (code=%d)", result.Code)
	}
	if strings.TrimSpace(result.Bot.OpenID) == "" {
		return errors.New("飞书机器人身份缺少 open_id")
	}
	b.mu.Lock()
	b.botOpenID = result.Bot.OpenID
	b.mu.Unlock()
	return nil
}

type longConnection interface {
	Start(context.Context) error
	Close()
}

func (b *Adapter) serveConnection(ctx context.Context, client longConnection) error {
	return b.serveConnectionUntil(ctx, client, nil)
}

func (b *Adapter) serveConnectionUntil(ctx context.Context, client longConnection, failed <-chan error) error {
	connectionCtx, cancel := context.WithCancel(ctx)
	defer b.stopConnection(client, cancel)
	// SDK v3.9.10 Start parks in select{}; its heartbeat also ignores ctx.
	// Close the socket explicitly on exit. These SDK background goroutines
	// remain until process exit (including after a supervised reconnect).
	errCh := make(chan error, 1)
	go func() { errCh <- client.Start(connectionCtx) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	case err := <-failed:
		return err
	}
}

func (b *Adapter) stopConnection(client longConnection, cancel context.CancelFunc) {
	cancel()
	connectionDone := make(chan struct{})
	go func() { client.Close(); close(connectionDone) }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-connectionDone:
	case <-timer.C:
		log.Print("[Feishu] 关闭长连接等待超时")
	}
}

func (b *Adapter) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil {
		return nil
	}
	if event.EventV2Base != nil && event.EventV2Base.Header != nil && event.EventV2Base.Header.AppID != "" && event.EventV2Base.Header.AppID != b.appID {
		return nil
	}
	message := event.Event.Message
	if value(event.Event.Sender.SenderType) != "user" || value(message.MessageType) != "text" {
		return nil
	}
	chatID, messageID := value(message.ChatId), value(message.MessageId)
	if chatID == "" || messageID == "" || message.Content == nil {
		return nil
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(*message.Content), &content); err != nil {
		log.Print("[Feishu] 忽略格式无效的文本消息")
		return nil
	}
	b.mu.RLock()
	lifetime, handler, botOpenID := b.ctx, b.handler, b.botOpenID
	b.mu.RUnlock()
	if lifetime == nil || handler == nil {
		return errors.New("飞书 Adapter 未启动")
	}
	if err := lifetime.Err(); err != nil {
		return err
	}
	mentions := append([]*larkim.MentionEvent(nil), message.Mentions...)
	// Replace longest keys first, so @_user_1 cannot corrupt @_user_10.
	sort.SliceStable(mentions, func(i, j int) bool {
		if mentions[i] == nil {
			return false
		}
		if mentions[j] == nil {
			return true
		}
		return len(value(mentions[i].Key)) > len(value(mentions[j].Key))
	})
	mentionedSelf := false
	originalText := content.Text
	for _, mention := range mentions {
		if mention == nil || value(mention.Key) == "" {
			continue
		}
		ownMention := botOpenID != "" && mention.Id != nil && value(mention.Id.OpenId) == botOpenID
		if ownMention && strings.Contains(originalText, *mention.Key) {
			mentionedSelf = true
		}
		replacement := value(mention.Key)
		if ownMention {
			replacement = ""
		} else if value(mention.Name) != "" {
			replacement = "@" + *mention.Name
		}
		content.Text = strings.ReplaceAll(content.Text, *mention.Key, replacement)
	}
	prompt := strings.TrimSpace(content.Text)
	if prompt == "" {
		return nil
	}
	chatType := channel.ChatDirect
	if value(message.ChatType) == "group" {
		chatType = channel.ChatGroup
	}
	userID := ""
	if event.Event.Sender.SenderId != nil {
		userID = value(event.Event.Sender.SenderId.OpenId)
	}
	// Keep one conversation per chat, including replies in Feishu topics. Gateway
	// applies the mention admission rule after routing immediate control replies.
	return handler(ctx, channel.InboundMessage{ID: messageID,
		Route:  channel.Route{Channel: b.ID(), AccountID: "default", ChatID: chatID},
		UserID: userID, ChatType: chatType, Text: prompt, MentionedBot: mentionedSelf, ReceivedAt: time.Now()})
}

func value(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func (b *Adapter) Send(ctx context.Context, target channel.Route, msg channel.OutboundMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if b == nil || b.client == nil {
		return errors.New("飞书消息发送器未配置")
	}
	if target.Channel != b.ID() || target.AccountID != "default" || target.ThreadID != "" {
		return errors.New("飞书消息目标不受支持")
	}
	for _, chunk := range splitMessage(msg.Text, maxMessageContentBytes) {
		if err := ctx.Err(); err != nil {
			return err
		}
		contentBytes, err := json.Marshal(map[string]string{"text": chunk})
		if err != nil {
			return err
		}
		req := larkim.NewCreateMessageReqBuilder().ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(target.ChatID).MsgType(larkim.MsgTypeText).Content(string(contentBytes)).Build()).Build()
		resp, err := b.client.Im.Message.Create(ctx, req)
		if err != nil {
			return fmt.Errorf("飞书消息请求失败: %w", err)
		}
		if resp == nil {
			return errors.New("飞书消息响应为空")
		}
		if !resp.Success() {
			return fmt.Errorf("飞书消息发送失败 (code=%d)", resp.Code)
		}
	}
	return nil
}

// splitMessage budgets JSON-escaped content and splits only at rune boundaries.
func splitMessage(text string, maxBytes int) []string {
	const overhead = len(`{"text":""}`)
	if maxBytes < overhead+6 {
		maxBytes = overhead + 6
	}
	chunks := make([]string, 0, 1)
	var current strings.Builder
	size := overhead
	for _, char := range text {
		encoded, _ := json.Marshal(string(char))
		cost := len(encoded) - 2
		if size+cost > maxBytes && current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
			size = overhead
		}
		current.WriteRune(char)
		size += cost
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

var _ channel.Adapter = (*Adapter)(nil)
