package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
)

func TestLegacyFeishuPreservesAccessAndNeverEnablesTelegram(t *testing.T) {
	configured := config.ChannelsConfig{
		Feishu:   config.ChannelConfig{Access: config.AccessConfig{Mode: "restricted", Operators: []string{"ou_Case"}, Approvers: []string{"ou_Case"}, GroupChats: []string{"oc_group"}}},
		Telegram: config.ChannelConfig{Enabled: true, Access: config.AccessConfig{Mode: "restricted", Operators: []string{"42"}}},
	}
	selected, err := channelsForMode("feishu", configured)
	if err != nil || !selected.Feishu.Enabled || selected.Telegram.Enabled || !reflect.DeepEqual(selected.Feishu.Access, configured.Feishu.Access) {
		t.Fatal("legacy startup discarded authorization or enabled another channel", selected, err)
	}
	a, err := selected.Authorizer()
	if err != nil {
		t.Fatal(err)
	}
	msg := channel.InboundMessage{Route: channel.Route{Channel: "feishu", AccountID: "default", ChatID: "private"}, UserID: "ou_Case", ChatType: channel.ChatDirect}
	if !a.CanUse(msg).Allowed || !a.CanApprove(msg).Allowed {
		t.Fatal("legacy entry point rejected configured operator/approver")
	}
	msg.UserID = "42"
	if a.CanUse(msg).Allowed || a.CanApprove(msg).Allowed {
		t.Fatal("legacy entry point bypassed restricted access")
	}
}

func TestModelSetPreservesMessagingAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("ZAI_CODING_CN_API_KEY", "offline-model-secret")
	path := filepath.Join(home, "config.json")
	cfg := config.Config{DefaultModel: config.FallbackModel, Language: "zh-CN",
		Channels: config.ChannelsConfig{
			Telegram: config.ChannelConfig{Enabled: true, Access: config.AccessConfig{Mode: "restricted", Operators: []string{"100", "200"}, Approvers: []string{"100"}, GroupChats: []string{"-1001"}}},
			Feishu:   config.ChannelConfig{Enabled: true, Access: config.AccessConfig{Mode: "restricted", Operators: []string{"ou_Case"}}},
		}, Messaging: config.MessagingConfig{ReportMode: "compact", ConversationIdleTimeout: "15m"}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	// A real model change must preserve both platforms' complete access config.
	if err := run(context.Background(), []string{"model", "set", "zai-coding-cn/glm-5.2"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	cfg.DefaultModel = "zai-coding-cn/glm-5.2"
	loaded, err := config.Load(path)
	if err != nil || !reflect.DeepEqual(loaded, cfg) {
		t.Fatal("model set lost access configuration", loaded, err)
	}
}

func TestInvalidAccessFailsStartupOffline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("PATH", t.TempDir())
	workspace := filepath.Join(t.TempDir(), "not-created")
	for _, body := range []string{
		`{"channels":{"telegram":{"enabled":true,"access":{"mode":"restricted"}}}}`,
		`{"channels":{"feishu":{"access":{"mode":"restricted","operators":["ou_one"],"approvers":["ou_other"]}}}}`,
	} {
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"gateway"}, {"gateway", "-check-config"}, {"-mode=feishu"}, {"-mode=demo"}} {
			err := run(context.Background(), append(args, "-workdir="+workspace), io.Discard)
			if err == nil || !strings.Contains(err.Error(), "access") && !strings.Contains(err.Error(), "operators") {
				t.Fatal("invalid access reached channel/Sandbox/model startup", args, err)
			}
		}
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("invalid access created workspace", err)
	}
}
