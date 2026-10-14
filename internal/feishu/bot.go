package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	chatQueueSize   = 64
	maxSeenMessages = 4096
	// Leave room for the surrounding API JSON, including a second layer of escaping.
	maxMessageContentBytes = 8 * 1024
)

type agentRunner interface {
	Run(context.Context, *ctxpkg.Session, engine.Reporter) error
}

type chatWorker struct {
	chatID   string
	session  *ctxpkg.Session
	reporter *FeishuReporter
	queue    chan string
}

// FeishuBot owns one ordered conversation and one engine per chat.
// Factories must construct independent, session-scoped engine dependencies.
type FeishuBot struct {
	resolveSession func(context.Context, string) (*ctxpkg.Session, error)
	runtimeFactory func(context.Context, *ctxpkg.Session, engine.Reporter) (*engine.AgentEngine, error)
	client         *lark.Client
	appID          string
	appSecret      string
	workDir        string
	factory        func(*ctxpkg.Session, engine.Reporter) agentRunner
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	chats          map[string]*chatWorker
	seen           map[string]time.Time
	workers        sync.WaitGroup
	startupErr     error
	botOpenID      string // protected by mu; empty disables normal group tasks
}

// NewFeishuBotWithSessions performs disk/auth work in per-chat workers, never
// under the bot-wide queue lock.
func NewFeishuBotWithSessions(workDir string, resolve func(context.Context, string) (*ctxpkg.Session, error), factory func(context.Context, *ctxpkg.Session, engine.Reporter) (*engine.AgentEngine, error)) (*FeishuBot, error) {
	if resolve == nil || factory == nil {
		return nil, errors.New("session runtime factory required")
	}
	b, err := newFeishuBot(workDir, func(*ctxpkg.Session, engine.Reporter) agentRunner { return nil })
	if err != nil {
		return nil, err
	}
	b.resolveSession = resolve
	b.runtimeFactory = factory
	return b, nil
}

func NewFeishuBotWithFactory(workDir string, factory func(*ctxpkg.Session, engine.Reporter) *engine.AgentEngine) (*FeishuBot, error) {
	if factory == nil {
		return nil, errors.New("飞书模式需要引擎工厂")
	}
	return newFeishuBot(workDir, func(sess *ctxpkg.Session, reporter engine.Reporter) agentRunner {
		eng := factory(sess, reporter)
		if eng == nil {
			return nil
		}
		return eng
	})
}

func newFeishuBot(workDir string, factory func(*ctxpkg.Session, engine.Reporter) agentRunner) (*FeishuBot, error) {
	appID := strings.TrimSpace(os.Getenv("FEISHU_APP_ID"))
	appSecret := strings.TrimSpace(os.Getenv("FEISHU_APP_SECRET"))
	if appID == "" || appSecret == "" {
		return nil, errors.New("请设置 FEISHU_APP_ID 和 FEISHU_APP_SECRET")
	}
	if factory == nil {
		return nil, errors.New("飞书模式需要引擎工厂")
	}
	if strings.TrimSpace(workDir) == "" {
		return nil, errors.New("飞书模式需要工作区目录")
	}
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("解析飞书工作区: %w", err)
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return nil, fmt.Errorf("读取飞书工作区: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("飞书工作区必须是目录")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &FeishuBot{
		client: lark.NewClient(appID, appSecret,
			lark.WithLogLevel(larkcore.LogLevelInfo),
			lark.WithReqTimeout(10*time.Second),
			lark.WithEnableTokenCache(true)),
		appID: appID, appSecret: appSecret, workDir: workDir,
		factory: factory, ctx: ctx, cancel: cancel,
		chats: make(map[string]*chatWorker), seen: make(map[string]time.Time),
	}, nil
}

// NewFeishuBot preserves source compatibility with older callers. A shared engine
// is serialized across chats; new integrations should use NewFeishuBotWithFactory.
// Configuration errors are returned by StartLongConnection, never os.Exit.
func NewFeishuBot(eng *engine.AgentEngine, sess *ctxpkg.Session) *FeishuBot {
	if eng == nil {
		return &FeishuBot{startupErr: errors.New("飞书模式需要引擎")}
	}
	workDir := ""
	if sess != nil {
		workDir = sess.WorkDir
	}
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return &FeishuBot{startupErr: err}
		}
	}
	shared := &serializedRunner{runner: eng}
	bot, err := newFeishuBot(workDir, func(*ctxpkg.Session, engine.Reporter) agentRunner { return shared })
	if err != nil {
		return &FeishuBot{startupErr: err}
	}
	return bot
}

type serializedRunner struct {
	mu     sync.Mutex
	runner agentRunner
}

func (r *serializedRunner) Run(ctx context.Context, sess *ctxpkg.Session, reporter engine.Reporter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.runner.Run(ctx, sess, reporter)
}

// Reporter is retained for source compatibility. A bot has no global reporter;
// the factory receives the immutable reporter belonging to its chat.
// Deprecated: use the reporter passed to NewFeishuBotWithFactory's factory.
func (b *FeishuBot) Reporter() *FeishuReporter { return nil }

func (b *FeishuBot) GetEventDispatcher() *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(b.handleMessage).
		OnP2MessageReadV1(func(context.Context, *larkim.P2MessageReadV1) error { return nil })
}

// Close cancels queued/running conversations and any outstanding approvals.
func (b *FeishuBot) Close() {
	if b != nil && b.cancel != nil {
		b.cancel()
	}
}

type longConnection interface {
	Start(context.Context) error
	Close()
}

func (b *FeishuBot) StartLongConnection(ctx context.Context) error {
	if b == nil {
		return errors.New("飞书机器人未初始化")
	}
	if b.startupErr != nil {
		return b.startupErr
	}
	if err := ctx.Err(); err != nil {
		b.Close()
		return err
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.loadBotIdentity(ctx); err != nil {
		if ctx.Err() != nil {
			b.Close()
			return ctx.Err()
		}
		if b.ctx.Err() != nil {
			return b.ctx.Err()
		}
		log.Printf("[Feishu] 无法确认机器人身份，群聊任务暂不可用；私聊继续: %v", err)
	}
	client := larkws.NewClient(b.appID, b.appSecret,
		larkws.WithEventHandler(b.GetEventDispatcher()),
		// SDK info logs include the connection URL. Keep tickets out of logs.
		larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithOnReady(func() { log.Print("[Feishu] 长连接已建立，等待消息") }),
	)
	return b.serveConnection(ctx, client)
}

// The pinned SDK exposes bot/v3/info through its authenticated raw Get method;
// the generated services do not include this endpoint. Identity is needed to
// distinguish this bot from other bots mentioned in a group with broad scopes.
func (b *FeishuBot) loadBotIdentity(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if b.ctx != nil {
		stop := context.AfterFunc(b.ctx, cancel)
		defer stop()
	}
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

func (b *FeishuBot) serveConnection(ctx context.Context, client longConnection) error {
	connectionCtx, cancel := context.WithCancel(ctx)
	defer b.stopConnection(client, cancel)
	// SDK v3.9.10 Start parks in select{} after connecting. Explicitly close
	// on cancellation instead of relying on Start to return (its goroutine
	// remains parked until the CLI process exits).
	errCh := make(chan error, 1)
	go func() { errCh <- client.Start(connectionCtx) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return b.ctx.Err()
	case err := <-errCh:
		return err
	}
}

// stopConnection gives canceled tools, approvals and trace exports time to finish.
// SDK Close can block on an in-flight connection attempt, so neither it nor an
// uncooperative tool may hold the CLI shutdown open indefinitely.
func (b *FeishuBot) stopConnection(client longConnection, cancel context.CancelFunc) {
	b.Close()
	cancel()
	// A callback which passed the cancellation check may still be adding its
	// worker. Taking the same lock establishes a barrier before Wait starts.
	b.mu.Lock()
	b.mu.Unlock()
	workersDone := make(chan struct{})
	connectionDone := make(chan struct{})
	go func() { b.workers.Wait(); close(workersDone) }()
	go func() { client.Close(); close(connectionDone) }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for workersDone != nil || connectionDone != nil {
		select {
		case <-workersDone:
			workersDone = nil
		case <-connectionDone:
			connectionDone = nil
		case <-timer.C:
			log.Printf("[Feishu] 关闭等待超时: workers_pending=%t connection_pending=%t", workersDone != nil, connectionDone != nil)
			return
		}
	}
}

func (b *FeishuBot) handleMessage(_ context.Context, event *larkim.P2MessageReceiveV1) error {
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
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.startupErr != nil {
		return b.startupErr
	}
	if b.ctx == nil {
		return errors.New("飞书机器人未初始化")
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}

	// Check the original mention metadata against the bootstrapped bot ID.
	// Other people/bots keep their display names in the prompt. Longest keys
	// are replaced first so @_user_1 cannot corrupt @_user_10.
	mentions := append([]*larkim.MentionEvent(nil), message.Mentions...)
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
		ownMention := b.botOpenID != "" && mention.Id != nil && value(mention.Id.OpenId) == b.botOpenID
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

	key := chatID + ":" + messageID
	if seenAt, exists := b.seen[key]; exists && time.Since(seenAt) < 24*time.Hour {
		return nil
	}

	// Approval commands bypass the conversation queue: its engine may be
	// waiting for this very approval. Resolution is scoped to the same chat.
	fields := strings.Fields(prompt)
	if len(fields) > 0 && (fields[0] == "approve" || fields[0] == "reject") {
		if len(fields) == 2 {
			allowed := fields[0] == "approve"
			reason := "人类管理员已批准操作"
			if !allowed {
				reason = "人类管理员已拒绝操作"
			}
			resolved := GlobalApprovalMgr.ResolveApprovalForChat(chatID, fields[1], allowed, reason)
			log.Printf("[Feishu] 审批指令已处理: matched=%t allowed=%t", resolved, allowed)
		}
		b.rememberMessage(key)
		return nil
	}

	// A broadly permissioned Feishu app can receive ordinary group chatter.
	// Only an explicit mention of this bot starts work; approvals above remain
	// usable as plain replies in their original chat, even without identity.
	if value(message.ChatType) == "group" && !mentionedSelf {
		return nil
	}

	worker := b.chats[chatID]
	if worker == nil {
		worker = &chatWorker{
			chatID:   chatID,
			session:  ctxpkg.NewSession(chatID, b.workDir),
			reporter: &FeishuReporter{client: b.client, chatId: chatID, ctx: b.ctx},
			queue:    make(chan string, chatQueueSize),
		}
		b.chats[chatID] = worker
		b.workers.Add(1)
		go b.runChat(worker)
	}
	select {
	case worker.queue <- prompt:
		b.rememberMessage(key)
		return nil
	default:
		// Returning an error allows Feishu to retry instead of silently losing
		// an acknowledged message; the ID is not marked as handled yet.
		return errors.New("当前飞书会话队列已满，请稍后重试")
	}
}

func value(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

// Caller holds b.mu. Bound deduplication memory even for a long-running bot.
func (b *FeishuBot) rememberMessage(key string) {
	if len(b.seen) >= maxSeenMessages {
		oldestKey := ""
		var oldest time.Time
		for id, at := range b.seen {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey, oldest = id, at
			}
		}
		delete(b.seen, oldestKey)
	}
	b.seen[key] = time.Now()
}

func (b *FeishuBot) runChat(worker *chatWorker) {
	defer b.workers.Done()
	defer func() {
		if err := worker.session.Close(); err != nil {
			log.Printf("[Feishu] 释放 Session writer lock 失败: %v", err)
		}
	}()
	var runner agentRunner
	sessionResolved := b.resolveSession == nil
	for {
		select {
		case <-b.ctx.Done():
			return
		case prompt := <-worker.queue:
			if b.ctx.Err() != nil {
				return
			}
			if runner == nil {
				if !sessionResolved {
					sess, err := b.resolveSession(b.ctx, worker.chatID)
					if err != nil {
						worker.reporter.OnMessage(b.ctx, fmt.Sprintf("❌ Session 恢复失败: %v", err))
						continue
					}
					worker.session = sess
					sessionResolved = true
				}
				if b.runtimeFactory != nil {
					eng, err := b.runtimeFactory(b.ctx, worker.session, worker.reporter)
					if err != nil {
						worker.reporter.OnMessage(b.ctx, fmt.Sprintf("❌ 模型不可用，请检查模型或重新登录: %v", err))
						continue
					}
					runner = eng
				} else {
					runner = b.factory(worker.session, worker.reporter)
				}
			}
			if runner == nil {
				worker.reporter.OnMessage(b.ctx, "❌ 无法创建会话引擎")
				continue
			}
			if err := worker.session.Append(b.ctx, schema.Message{Role: schema.RoleUser, Content: prompt}); err != nil {
				worker.reporter.OnMessage(b.ctx, fmt.Sprintf("❌ Session 保存失败: %v", err))
				continue
			}
			if err := runner.Run(b.ctx, worker.session, worker.reporter); err != nil && b.ctx.Err() == nil {
				worker.reporter.OnMessage(b.ctx, fmt.Sprintf("❌ Agent 运行失败: %v", err))
			}
		}
	}
}

// FeishuReporter is immutable after construction, so concurrent tool reports
// cannot accidentally switch their destination to a different chat.
type FeishuReporter struct {
	client *lark.Client
	chatId string
	ctx    context.Context
}

func (r *FeishuReporter) ChatID() string {
	if r == nil {
		return ""
	}
	return r.chatId
}

func (r *FeishuReporter) sendMsg(text string) {
	ctx := context.Background()
	if r != nil && r.ctx != nil {
		ctx = r.ctx
	}
	if err := r.sendMsgContext(ctx, text); err != nil {
		log.Printf("[Feishu] 发送消息失败: %v", err)
	}
}

func (r *FeishuReporter) sendMsgContext(ctx context.Context, text string) error {
	if r == nil || r.client == nil || r.chatId == "" {
		return errors.New("飞书消息发送器未配置")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, chunk := range splitMessage(text, maxMessageContentBytes) {
		contentBytes, err := json.Marshal(map[string]string{"text": chunk})
		if err != nil {
			return err
		}
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(r.chatId).
				MsgType(larkim.MsgTypeText).Content(string(contentBytes)).Build()).Build()
		resp, err := r.client.Im.Message.Create(ctx, req)
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

// splitMessage budgets the JSON-escaped content, and only splits on rune
// boundaries so Chinese text, emoji, quotes and newlines remain intact.
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

func (r *FeishuReporter) report(ctx context.Context, text string) {
	if err := r.sendMsgContext(ctx, text); err != nil {
		log.Printf("[Feishu] 发送消息失败: %v", err)
	}
}

func (r *FeishuReporter) OnThinking(ctx context.Context) {
	r.report(ctx, "🤔 模型正在慢思考 (Thinking)...")
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.report(ctx, fmt.Sprintf("🛠️ **正在执行工具**：`%s`\n参数：`%s`", toolName, args))
}

func (r *FeishuReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.report(ctx, fmt.Sprintf("⚠️ **执行报错** (%s)：\n%s", toolName, result))
	} else {
		r.report(ctx, fmt.Sprintf("✅ **执行成功** (%s)", toolName))
	}
}

func (r *FeishuReporter) OnMessage(ctx context.Context, content string) { r.report(ctx, content) }

var _ engine.Reporter = (*FeishuReporter)(nil)
