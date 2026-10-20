package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/delivery"
	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

const FallbackModel = "zai-coding-cn/glm-5.3"

type MessagingConfig struct {
	ReportMode              string `json:"report_mode,omitempty"`
	ConversationIdleTimeout string `json:"conversation_idle_timeout,omitempty"`
}

// IdleTimeout parses user configuration before constructing the Gateway.
// Empty means 30m; zero disables eviction. Runtime Config uses time.Duration.
func (c MessagingConfig) IdleTimeout() (time.Duration, error) {
	if c.ConversationIdleTimeout == "" {
		return 30 * time.Minute, nil
	}
	d, err := time.ParseDuration(c.ConversationIdleTimeout)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid messaging.conversation_idle_timeout %q: must be a non-negative duration (0 disables eviction)", c.ConversationIdleTimeout)
	}
	return d, nil
}

// ReportingPolicy is the configuration boundary: Gateway receives a typed
// policy and never parses user-supplied strings. An empty value means compact.
func (c MessagingConfig) ReportingPolicy() (delivery.ReportingPolicy, error) {
	mode, err := delivery.ParseReportMode(c.ReportMode)
	return delivery.ReportingPolicy{Mode: mode}, err
}

// ChannelConfig contains no credentials. Secrets are read from the environment.
type ChannelConfig struct {
	Enabled bool `json:"enabled"`
}

type ChannelsConfig struct {
	Feishu   ChannelConfig `json:"feishu,omitempty"`
	Telegram ChannelConfig `json:"telegram,omitempty"`
}

type Config struct {
	DefaultModel string               `json:"default_model"`
	Language     string               `json:"language,omitempty"`
	Context      ctxpkg.ContextPolicy `json:"context,omitempty"`
	Messaging    MessagingConfig      `json:"messaging,omitempty"`
	Channels     ChannelsConfig       `json:"channels,omitempty"`
}

func Load(path string) (Config, error) {
	var c Config
	err := fileutil.ReadJSON(path, &c)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := validateLanguage(c.Language); err != nil {
		return c, err
	}
	if _, err := c.Messaging.ReportingPolicy(); err != nil {
		return c, err
	}
	if _, err := c.Messaging.IdleTimeout(); err != nil {
		return c, err
	}
	if c.Context.DefaultContextWindow < 0 || c.Context.DefaultReservedOutput < 0 || c.Context.MaxToolResultTokens < 0 || c.Context.SafetyMarginPercent < 0 || c.Context.SafetyMarginPercent >= 100 {
		return c, errors.New("invalid context policy")
	}
	if c.DefaultModel != "" {
		_, err = provider.ParseModelRef(c.DefaultModel)
	}
	return c, err
}
func validateLanguage(language string) error {
	if len(language) > 64 || strings.IndexFunc(language, unicode.IsControl) >= 0 {
		return errors.New("invalid response language")
	}
	return nil
}

func Save(path string, c Config) error {
	if err := validateLanguage(c.Language); err != nil {
		return err
	}
	if _, err := c.Messaging.ReportingPolicy(); err != nil {
		return err
	}
	if _, err := c.Messaging.IdleTimeout(); err != nil {
		return err
	}
	if _, err := provider.ParseModelRef(c.DefaultModel); err != nil {
		return err
	}
	return fileutil.WriteJSON(path, c)
}

// ResolveSelectedModel does not silently route to a different provider. legacy
// is true only for an unqualified explicit CLI flag, never for saved config.
func ResolveSelectedModel(explicit string, c Config) (ref provider.ModelRef, legacy bool, err error) {
	value := explicit
	if value != "" && !strings.Contains(value, "/") {
		value = "zai-coding-cn/" + value
		legacy = true
	}
	if value == "" {
		value = c.DefaultModel
	}
	if value == "" {
		value = FallbackModel
	}
	ref, err = provider.ParseModelRef(value)
	return
}
