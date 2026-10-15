package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
)

func TestMessagingConfigDefaultsAndModes(t *testing.T) {
	for _, tc := range []struct {
		name, json string
		want       delivery.ReportMode
	}{
		{"missing-file", "", delivery.ReportModeCompact},
		{"empty-config", `{}`, delivery.ReportModeCompact},
		{"legacy-config", `{"default_model":"openai/gpt-5.2","language":"zh-CN"}`, delivery.ReportModeCompact},
		{"empty-messaging", `{"messaging":{}}`, delivery.ReportModeCompact},
		{"empty-mode", `{"messaging":{"report_mode":""}}`, delivery.ReportModeCompact},
		{"quiet", `{"messaging":{"report_mode":"quiet"}}`, delivery.ReportModeQuiet},
		{"compact", `{"messaging":{"report_mode":"compact"}}`, delivery.ReportModeCompact},
		{"verbose", `{"messaging":{"report_mode":"verbose"}}`, delivery.ReportModeVerbose},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if tc.json != "" {
				if err := os.WriteFile(path, []byte(tc.json), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := c.Messaging.ReportingPolicy()
			if err != nil || policy.Mode != tc.want {
				t.Fatalf("policy = %+v, %v; want %q", policy, err, tc.want)
			}
		})
	}
}

func TestMessagingConfigRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, mode := range []string{"", "quiet", "compact", "verbose"} {
		c := Config{DefaultModel: FallbackModel, Language: "zh-CN", Messaging: MessagingConfig{ReportMode: mode}}
		if err := Save(path, c); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil || loaded != c {
			t.Fatalf("round trip changed config: %+v, %v", loaded, err)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"comapct", "COMPACT", " compact", "debug"} {
		c := Config{DefaultModel: FallbackModel, Messaging: MessagingConfig{ReportMode: mode}}
		if _, err := c.Messaging.ReportingPolicy(); err == nil {
			t.Fatalf("invalid config mode %q parsed", mode)
		}
		if err := Save(path, c); err == nil || !strings.Contains(err.Error(), "messaging.report_mode") {
			t.Fatalf("invalid mode %q saved: %v", mode, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("failed save damaged config: %v", err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"messaging":{"report_mode":"comapct"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "messaging.report_mode") {
		t.Fatalf("typo silently loaded: %v", err)
	}
}
