package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/engine"
	"github.com/ketitongxue/go-tiny-claw/internal/gateway"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestGatewayIdleTimeoutCheckConfigIsOffline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("ZAI_CODING_CN_API_KEY", "offline-model-secret")
	t.Setenv("FEISHU_APP_ID", "offline-app")
	t.Setenv("FEISHU_APP_SECRET", "offline-secret")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:offline-bot-secret")
	t.Setenv("PATH", t.TempDir()) // Cannot start/probe Sandbox binaries.
	path := filepath.Join(home, "config.json")
	workspace := filepath.Join(t.TempDir(), "not-created")
	for _, tc := range []struct{ setting, printed string }{
		{"", "30m0s"}, {"15m", "15m0s"}, {"0", "0s (disabled)"},
	} {
		cfg := config.Config{DefaultModel: config.FallbackModel,
			Channels:  config.ChannelsConfig{Feishu: config.ChannelConfig{Enabled: true}, Telegram: config.ChannelConfig{Enabled: true}},
			Messaging: config.MessagingConfig{ReportMode: "compact", ConversationIdleTimeout: tc.setting}}
		if err := config.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := run(context.Background(), []string{"gateway", "-check-config", "-workdir=" + workspace}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Conversation idle timeout: "+tc.printed) || strings.Contains(out.String(), "offline-") {
			t.Fatal("missing timeout or leaked credential", out.String())
		}
		if _, err := os.Stat(workspace); !os.IsNotExist(err) {
			t.Fatal("check-config created workspace", err)
		}
		entries, err := os.ReadDir(filepath.Join(home, "sessions"))
		if err != nil || len(entries) != 0 {
			t.Fatal("check-config created a session/writer", entries, err)
		}
	}
	for _, setting := range []string{"-1m", "abc"} {
		if err := os.WriteFile(path, []byte(`{"messaging":{"conversation_idle_timeout":"`+setting+`"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"gateway", "-check-config"}, {"gateway"}, {"-mode=feishu"}, {"-mode=demo"}} {
			err := run(context.Background(), append(args, "-workdir="+workspace), io.Discard)
			if err == nil || !strings.Contains(err.Error(), "messaging.conversation_idle_timeout") {
				t.Fatal("invalid timeout reached startup", args, err)
			}
		}
	}
}

func TestModelSetPreservesMessagingLifecycleConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("ZAI_CODING_CN_API_KEY", "offline-model-secret")
	path := filepath.Join(home, "config.json")
	cfg := config.Config{DefaultModel: config.FallbackModel, Language: "zh-CN",
		Channels:  config.ChannelsConfig{Feishu: config.ChannelConfig{Enabled: true}, Telegram: config.ChannelConfig{Enabled: true}},
		Messaging: config.MessagingConfig{ReportMode: "verbose", ConversationIdleTimeout: "15m"}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"model", "set", config.FallbackModel}, io.Discard); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil || loaded != cfg {
		t.Fatal("model set lost channels/report mode/idle timeout", loaded, err)
	}
}

type lifecycleAdapter string

func (a lifecycleAdapter) ID() string { return string(a) }
func (lifecycleAdapter) Start(ctx context.Context, _ channel.InboundHandler) error {
	<-ctx.Done()
	return ctx.Err()
}
func (lifecycleAdapter) Send(context.Context, channel.Route, channel.OutboundMessage) error {
	return nil
}

type lifecycleRunner func(context.Context, *ctxpkg.Session, engine.Reporter) error

func (f lifecycleRunner) Run(ctx context.Context, s *ctxpkg.Session, r engine.Reporter) error {
	return f(ctx, s, r)
}

func lifecycleReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("lifecycle operation timed out")
		var zero T
		return zero
	}
}

func TestIdleEvictionKeepsLegacyFeishuAndTelegramHistoryIsolated(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "sessions")
	store, err := sessionstore.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := ctxpkg.NewSessionService(store)
	legacy, err := svc.Create(ctx, "oc_legacy", dir, provider.ModelInfo{ProviderID: "fake", ID: "old-model"}, "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Append(ctx, schema.Message{Role: schema.RoleUser, Content: "remember FEISHU-LIFE"}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	registry := channel.NewRegistry()
	for _, name := range []string{"feishu", "telegram"} {
		if err := registry.Register(lifecycleAdapter(name)); err != nil {
			t.Fatal(err)
		}
	}
	sessions := gateway.DurableSessions{Service: svc, LegacyID: legacyFeishuSessionID,
		Create: func(ctx context.Context, id string) (*ctxpkg.Session, error) {
			return svc.Create(ctx, id, dir, provider.ModelInfo{ProviderID: "fake", ID: "new-model"}, "none")
		}}
	type run struct {
		session *ctxpkg.Session
		history []schema.Message
		release chan struct{}
	}
	runs := make(chan run, 2)
	var factories atomic.Int32
	g, err := gateway.New(ctx, gateway.Config{Channels: registry, ResolveSession: sessions.Resolve,
		ConversationIdleTimeout: 20 * time.Millisecond, ReportingPolicy: delivery.ReportingPolicy{Mode: delivery.ReportModeQuiet},
		Factory: func(context.Context, *ctxpkg.Session, channel.Route, engine.Reporter, *approval.Service) (gateway.AgentRunner, error) {
			factories.Add(1)
			return lifecycleRunner(func(ctx context.Context, s *ctxpkg.Session, _ engine.Reporter) error {
				r := run{s, s.GetWorkingMemory(0), make(chan struct{})}
				select {
				case runs <- r:
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case <-r.release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := g.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	previous := map[string]*ctxpkg.Session{}
	for cycle := range 2 {
		for _, name := range []string{"feishu", "telegram"} {
			// Deliberately identical platform ChatIDs: only Feishu may fall back.
			route := channel.Route{Channel: name, AccountID: "default", ChatID: "oc_legacy"}
			text := "query history"
			if cycle == 0 && name == "telegram" {
				text = "remember TG-LIFE"
			}
			if err := g.Receive(ctx, channel.InboundMessage{ID: strconv.Itoa(cycle), Route: route, ChatType: channel.ChatDirect, Text: text}); err != nil {
				t.Fatal(err)
			}
			current := lifecycleReceive(t, runs)
			wantID, wantMemory, wantModel, wantLen := "oc_legacy", "remember FEISHU-LIFE", "old-model", 2+cycle
			if name == "telegram" {
				wantID, wantMemory, wantModel, wantLen = channel.BuildSessionKey(route), "remember TG-LIFE", "new-model", 1+cycle
			}
			if current.session == previous[name] || current.session.ID != wantID || current.session.ModelRef().ModelID != wantModel || len(current.history) != wantLen || current.history[0].Content != wantMemory {
				t.Fatal("legacy/cross-channel history forked, mixed or lost", name, current.session.ID, current.history)
			}
			previous[name] = current.session
			close(current.release)
		}
		deadline := time.Now().Add(3 * time.Second)
		for g.ConversationCount() != 0 {
			if time.Now().After(deadline) {
				t.Fatal("conversations did not evict")
			}
			time.Sleep(time.Millisecond)
		}
		// Independent FileStore/SessionService must reacquire both released writers.
		other, err := sessionstore.NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		outside := ctxpkg.NewSessionService(other)
		for _, s := range previous {
			resumed, err := outside.Resume(ctx, s.ID)
			if err != nil {
				t.Fatal("writer leaked after eviction", err)
			}
			if err := resumed.Close(); err != nil {
				t.Fatal(err)
			}
		}
		canonical := channel.BuildSessionKey(channel.Route{Channel: "feishu", AccountID: "default", ChatID: "oc_legacy"})
		if _, err := store.Load(ctx, canonical); !errors.Is(err, sessionstore.ErrNotFound) {
			t.Fatal("legacy history forked into canonical session", err)
		}
	}
	if factories.Load() != 4 {
		t.Fatal("engines not recreated after eviction", factories.Load())
	}
}
