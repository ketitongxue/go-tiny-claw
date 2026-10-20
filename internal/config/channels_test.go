package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestChannelConfigDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{"", `{}`, `{"messaging":{"report_mode":"quiet"}}`, `{"channels":{}}`} {
		if body != "" {
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cfg, err := Load(path)
		if err != nil || !reflect.DeepEqual(cfg.Channels, ChannelsConfig{}) {
			t.Fatalf("legacy config enabled a channel: %+v, %v", cfg, err)
		}
	}
	for _, channels := range []ChannelsConfig{
		{Feishu: ChannelConfig{Enabled: true}},
		{Telegram: ChannelConfig{Enabled: true}},
		{Feishu: ChannelConfig{Enabled: true}, Telegram: ChannelConfig{Enabled: true}},
	} {
		cfg := Config{DefaultModel: FallbackModel, Channels: channels, Messaging: MessagingConfig{ReportMode: "compact"}}
		if err := Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil || !reflect.DeepEqual(loaded, cfg) {
			t.Fatalf("config changed: %+v, %v", loaded, err)
		}
	}
}

func TestChannelConfigRejectsSecretsAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{
		`{"channels":{"telegram":{"enabled":true,"token":"secret-must-not-appear"}}}`,
		`{"channels":{"feishu":{"app_secret":"secret-must-not-appear"}}}`,
		`{"channels":{"telegram":{"enabled":"secret-must-not-appear"}}}`,
		`{"channels":{"slack":{"enabled":true}}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || strings.Contains(err.Error(), "secret-must-not-appear") {
			t.Fatalf("unsupported config accepted or leaked: %v", err)
		}
	}
}
