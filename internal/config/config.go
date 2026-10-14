package config

import (
	"errors"
	"os"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/fileutil"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
)

const FallbackModel = "zai-coding-cn/glm-5.3"

type Config struct {
	DefaultModel string `json:"default_model"`
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
	if c.DefaultModel != "" {
		_, err = provider.ParseModelRef(c.DefaultModel)
	}
	return c, err
}
func Save(path string, c Config) error {
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
