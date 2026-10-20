package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/feishu"
	"github.com/ketitongxue/go-tiny-claw/internal/telegram"
)

func channelsForMode(mode string, configured config.ChannelsConfig) (config.ChannelsConfig, error) {
	switch mode {
	case "demo":
		return config.ChannelsConfig{}, nil
	case "feishu":
		// Legacy startup must not implicitly activate new configured channels.
		feishu := configured.Feishu
		feishu.Enabled = true // Preserve access policy even through legacy startup.
		return config.ChannelsConfig{Feishu: feishu}, nil
	case "gateway":
		if configured.Feishu.Enabled || configured.Telegram.Enabled {
			return configured, nil
		}
		return configured, errors.New("claw gateway requires at least one enabled channel in config.json: channels.feishu.enabled or channels.telegram.enabled")
	default:
		return config.ChannelsConfig{}, fmt.Errorf("unknown messaging mode %q", mode)
	}
}

func validateEnvironment(channels config.ChannelsConfig, getenv func(string) string) error {
	var required []string
	if channels.Feishu.Enabled {
		required = append(required, "FEISHU_APP_ID", "FEISHU_APP_SECRET")
	}
	if channels.Telegram.Enabled {
		required = append(required, "TELEGRAM_BOT_TOKEN")
	}
	var missing []string
	for _, key := range required {
		if strings.TrimSpace(getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少环境变量: %s；请在本机设置后重新启动（参见 README.md）", strings.Join(missing, ", "))
	}
	return nil
}

// Constructors only: no platform protocol or network I/O belongs in CLI wiring.
func configureChannels(cfg config.Config) (*channel.Registry, error) {
	registry := channel.NewRegistry()
	if cfg.Channels.Feishu.Enabled {
		a, err := feishu.NewAdapter()
		if err != nil {
			return nil, err
		}
		if err := registry.Register(a); err != nil {
			return nil, err
		}
	}
	if cfg.Channels.Telegram.Enabled {
		a, err := telegram.NewAdapter()
		if err != nil {
			return nil, err
		}
		if err := registry.Register(a); err != nil {
			return nil, err
		}
	}
	if len(registry.All()) == 0 {
		return nil, errors.New("gateway requires at least one enabled channel")
	}
	return registry, nil
}
