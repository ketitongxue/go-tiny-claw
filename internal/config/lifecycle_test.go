package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConversationIdleTimeoutConfig(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       time.Duration
		invalid    bool
	}{
		{"missing-file", "", 30 * time.Minute, false},
		{"empty-config", `{}`, 30 * time.Minute, false},
		{"empty-messaging", `{"messaging":{}}`, 30 * time.Minute, false},
		{"empty-timeout", `{"messaging":{"conversation_idle_timeout":""}}`, 30 * time.Minute, false},
		{"configured", `{"messaging":{"conversation_idle_timeout":"15m"}}`, 15 * time.Minute, false},
		{"disabled", `{"messaging":{"conversation_idle_timeout":"0"}}`, 0, false},
		{"zero-duration", `{"messaging":{"conversation_idle_timeout":"0s"}}`, 0, false},
		{"negative", `{"messaging":{"conversation_idle_timeout":"-1m"}}`, 0, true},
		{"invalid", `{"messaging":{"conversation_idle_timeout":"abc"}}`, 0, true},
		{"overflow", `{"messaging":{"conversation_idle_timeout":"999999999999999h"}}`, 0, true},
		{"number-not-string", `{"messaging":{"conversation_idle_timeout":1000}}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if tc.body != "" {
				if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load(path)
			if tc.invalid {
				if err == nil || tc.name != "number-not-string" && !strings.Contains(err.Error(), "conversation_idle_timeout") {
					t.Fatalf("invalid timeout loaded: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			d, err := cfg.Messaging.IdleTimeout()
			if err != nil || d != tc.want {
				t.Fatalf("timeout = %s, %v; want %s", d, err, tc.want)
			}
		})
	}
}

func TestConversationIdleTimeoutSavePreservesConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, timeout := range []string{"", "0", "15m"} {
		cfg := Config{DefaultModel: "openai/gpt-5.2", Language: "zh-CN",
			Channels:  ChannelsConfig{Feishu: ChannelConfig{Enabled: true}, Telegram: ChannelConfig{Enabled: true}},
			Messaging: MessagingConfig{ReportMode: "verbose", ConversationIdleTimeout: timeout}}
		if err := Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil || !reflect.DeepEqual(loaded, cfg) {
			t.Fatalf("configuration lost on save: %+v, %v", loaded, err)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"-1m", "abc"} {
		cfg := Config{DefaultModel: FallbackModel, Messaging: MessagingConfig{ConversationIdleTimeout: invalid}}
		if _, err := cfg.Messaging.IdleTimeout(); err == nil {
			t.Fatal("invalid duration parsed", invalid)
		}
		if err := Save(path, cfg); err == nil || !strings.Contains(err.Error(), "conversation_idle_timeout") {
			t.Fatal("invalid duration saved", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatal("invalid save changed config", err)
		}
	}
}
