package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/feishu"
	"github.com/ketitongxue/go-tiny-claw/internal/telegram"
)

func TestGatewayCommandAndChannelSelection(t *testing.T) {
	opts, err := parseOptions([]string{"gateway", "-workdir", "./test-workspace"}, io.Discard)
	if err != nil || opts.mode != "gateway" || !filepath.IsAbs(opts.workDir) {
		t.Fatalf("gateway options: %+v, %v", opts, err)
	}
	for _, args := range [][]string{{"gateway", "-mode", "demo"}, {"gateway", "-mode=gateway"}, {"gateway", "unexpected"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatal("ambiguous gateway command accepted", args)
		}
	}
	both := config.ChannelsConfig{Feishu: config.ChannelConfig{Enabled: true}, Telegram: config.ChannelConfig{Enabled: true}}
	for _, tc := range []struct {
		mode string
		want config.ChannelsConfig
	}{
		{"gateway", both},
		{"feishu", config.ChannelsConfig{Feishu: config.ChannelConfig{Enabled: true}}},
		{"demo", config.ChannelsConfig{}},
	} {
		got, err := channelsForMode(tc.mode, both)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatal(tc.mode, got, err)
		}
	}
	if _, err := channelsForMode("gateway", config.ChannelsConfig{}); err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Fatal("empty gateway configuration accepted", err)
	}
}

func TestOnlyEnabledChannelsRequireCredentials(t *testing.T) {
	for _, tc := range []struct {
		channels config.ChannelsConfig
		keys     string
	}{
		{config.ChannelsConfig{}, ""},
		{config.ChannelsConfig{Feishu: config.ChannelConfig{Enabled: true}}, "FEISHU_APP_ID, FEISHU_APP_SECRET"},
		{config.ChannelsConfig{Telegram: config.ChannelConfig{Enabled: true}}, "TELEGRAM_BOT_TOKEN"},
		{config.ChannelsConfig{Feishu: config.ChannelConfig{Enabled: true}, Telegram: config.ChannelConfig{Enabled: true}}, "FEISHU_APP_ID, FEISHU_APP_SECRET, TELEGRAM_BOT_TOKEN"},
	} {
		var requested []string
		err := validateEnvironment(tc.channels, func(key string) string { requested = append(requested, key); return " \t" })
		if strings.Join(requested, ", ") != tc.keys || tc.keys != "" && (err == nil || !strings.Contains(err.Error(), tc.keys)) || tc.keys == "" && err != nil {
			t.Fatal("incorrect required credentials", requested, err)
		}
		if err := validateEnvironment(tc.channels, func(string) string { return "secret-must-not-appear" }); err != nil {
			t.Fatal("available credentials rejected", err)
		}
	}
}

func TestConfigureChannelsRegistersOnlyEnabledRealAdapters(t *testing.T) {
	for _, tc := range []struct {
		name             string
		feishu, telegram bool
	}{{"feishu", true, false}, {"telegram", false, true}, {"both", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FEISHU_APP_ID", "")
			t.Setenv("FEISHU_APP_SECRET", "")
			t.Setenv("TELEGRAM_BOT_TOKEN", "")
			if tc.feishu {
				t.Setenv("FEISHU_APP_ID", "offline-app")
				t.Setenv("FEISHU_APP_SECRET", "offline-secret")
			}
			if tc.telegram {
				t.Setenv("TELEGRAM_BOT_TOKEN", "123:offline-secret")
			}
			registry, err := configureChannels(config.Config{Channels: config.ChannelsConfig{
				Feishu: config.ChannelConfig{Enabled: tc.feishu}, Telegram: config.ChannelConfig{Enabled: tc.telegram}}})
			if err != nil {
				t.Fatal(err)
			}
			f, hasFeishu := registry.Get("feishu")
			tg, hasTelegram := registry.Get("telegram")
			if hasFeishu != tc.feishu || hasTelegram != tc.telegram {
				t.Fatal("incorrect registry contents")
			}
			if hasFeishu {
				if _, ok := f.(*feishu.Adapter); !ok {
					t.Fatalf("not a real Feishu adapter: %T", f)
				}
			}
			if hasTelegram {
				if _, ok := tg.(*telegram.Adapter); !ok {
					t.Fatalf("not a real Telegram adapter: %T", tg)
				}
			}
		})
	}
}

func TestGatewayCheckConfigAndLegacyFeishuCompatibility(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("ZAI_CODING_CN_API_KEY", "offline-model-secret")
	t.Setenv("FEISHU_APP_ID", "")
	t.Setenv("FEISHU_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:offline-bot-secret")
	t.Setenv("PATH", t.TempDir()) // No check may contact Docker or a live channel.
	path := filepath.Join(home, "config.json")
	cfg := config.Config{DefaultModel: config.FallbackModel, Channels: config.ChannelsConfig{Telegram: config.ChannelConfig{Enabled: true}}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "not-created")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"gateway", "-check-config", "-workdir=" + dir}, &out); err != nil {
		t.Fatal("Telegram-only gateway required Feishu credentials", err)
	}
	if !strings.Contains(out.String(), "mode=gateway") || strings.Contains(out.String(), "offline-") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("check-config created workspace", err)
	}
	// Legacy Feishu ignores Telegram enablement and does not require its secret.
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("FEISHU_APP_ID", "offline-app")
	t.Setenv("FEISHU_APP_SECRET", "offline-secret")
	out.Reset()
	if err := run(context.Background(), []string{"-mode=feishu", "-check-config", "-workdir=" + dir}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "WARNING: -mode feishu is deprecated; use claw gateway") {
		t.Fatal("legacy deprecation warning absent")
	}
	// Demo never requires messaging credentials, even if config enables channels.
	t.Setenv("FEISHU_APP_ID", "")
	t.Setenv("FEISHU_APP_SECRET", "")
	if err := run(context.Background(), []string{"-mode=demo", "-check-config", "-workdir=" + dir}, io.Discard); err != nil {
		t.Fatal("demo required messaging credentials", err)
	}
	if err := run(context.Background(), []string{"gateway", "-check-config", "-workdir=" + dir}, io.Discard); err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") || strings.Contains(err.Error(), "FEISHU_APP_ID") {
		t.Fatal("incorrect enabled-channel error", err)
	}
	cfg.Channels = config.ChannelsConfig{}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"gateway", "-workdir=" + dir}, io.Discard); err == nil || !strings.Contains(err.Error(), "at least one enabled channel") {
		t.Fatal("empty gateway started or probed Docker", err)
	}
}
