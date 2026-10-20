package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/access"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

func TestAccessConfigurationDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{`{}`, `{"channels":{"telegram":{"enabled":true}}}`, `{"channels":{"telegram":{"access":{"mode":"open"}}}}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		a, err := cfg.Channels.Authorizer()
		msg := channel.InboundMessage{Route: channel.Route{Channel: "telegram", AccountID: "default", ChatID: "private"}, ChatType: channel.ChatDirect}
		if err != nil || !a.CanUse(msg).Allowed || !a.CanApprove(msg).Allowed {
			t.Fatal("absent access must preserve open behavior", err)
		}
	}
	for _, policy := range []AccessConfig{
		{Mode: "open"},
		{Mode: "restricted", Operators: []string{"100"}}, // Empty approvers/groups are valid fail-closed policies.
		{Mode: "restricted", Operators: []string{" 100 ", "100", "ou_Case"}, Approvers: []string{"100", " 100 "}, GroupChats: []string{" -1001 ", "-1001"}},
	} {
		cfg := Config{DefaultModel: FallbackModel, Channels: ChannelsConfig{
			Telegram: ChannelConfig{Enabled: true, Access: policy},
			Feishu:   ChannelConfig{Enabled: true, Access: AccessConfig{Mode: "restricted", Operators: []string{"ou_Case"}}},
		}}
		if err := Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil || !reflect.DeepEqual(loaded, cfg) {
			t.Fatal("access configuration lost on round trip", loaded, err)
		}
		a, err := loaded.Channels.Authorizer()
		if err != nil {
			t.Fatal(err)
		}
		msg := channel.InboundMessage{Route: channel.Route{Channel: "telegram", AccountID: "default", ChatID: "-1001"}, UserID: "100", ChatType: channel.ChatGroup}
		want := policy.Mode == "open" || len(policy.GroupChats) != 0
		if a.CanUse(msg).Allowed != want || a.CanApprove(msg).Allowed != want {
			t.Fatal("normalized configuration compiled incorrectly")
		}
		msg.Route.Channel = "feishu"
		if a.CanUse(msg).Reason != access.ReasonUserNotAllowed {
			t.Fatal("configuration mixed channel identities")
		}
	}
}

func TestAccessConfigurationValidationLoadAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, policy := range []AccessConfig{
		{Mode: "secret-must-not-appear"},
		{Mode: "restricted"},
		{Mode: "restricted", Operators: []string{" \t"}},
		{Mode: "restricted", Operators: []string{"100"}, Approvers: []string{"200"}},
		{Mode: "restricted", Operators: []string{"ou_Case"}, Approvers: []string{"ou_case"}},
		{Operators: []string{""}}, {Approvers: []string{""}}, {GroupChats: []string{" \n"}},
	} {
		for _, enabled := range []bool{true, false} {
			valid := Config{DefaultModel: FallbackModel}
			if err := Save(path, valid); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg := Config{DefaultModel: FallbackModel, Channels: ChannelsConfig{Telegram: ChannelConfig{Enabled: enabled, Access: policy}}}
			if err := Save(path, cfg); err == nil || strings.Contains(err.Error(), "secret-must-not-appear") {
				t.Fatal("invalid access saved or leaked", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("invalid access save damaged configuration", err)
			}
			body, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || strings.Contains(err.Error(), "secret-must-not-appear") {
				t.Fatal("invalid access loaded or leaked", err)
			}
		}
	}
}

func TestAccessConfigurationRejectsUnknownFieldsAndTypes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{
		`{"channels":{"telegram":{"access":{"operator":["secret-must-not-appear"]}}}}`,
		`{"channels":{"telegram":{"access":{"token":"secret-must-not-appear"}}}}`,
		`{"channels":{"telegram":{"access":{"mode":true}}}}`,
		`{"channels":{"telegram":{"access":{"operators":[100]}}}}`,
		`{"channels":{"telegram":{"access":{"approvers":"secret-must-not-appear"}}}}`,
		`{"channels":{"feishu":{"access":{"group_chats":[{}]}}}}`,
		`{"channels":{"feishu":{"access":{"mode":"open","mode":"restricted"}}}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || strings.Contains(err.Error(), "secret-must-not-appear") {
			t.Fatal("unknown access field/type accepted or leaked", err)
		}
	}
}
