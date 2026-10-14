package config

import (
	"errors"
	"os"
	"strings"
	"unicode"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

const FallbackModel = "zai-coding-cn/glm-5.3"

type Config struct {
	DefaultModel string               `json:"default_model"`
	Language     string               `json:"language,omitempty"`
	Context      ctxpkg.ContextPolicy `json:"context,omitempty"`
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
