package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResponseLanguageConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Config{DefaultModel: FallbackModel, Language: "en-US"}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || !reflect.DeepEqual(loaded, c) {
		t.Fatal(loaded, err)
	}
	for _, language := range []string{"en\nInjected", strings.Repeat("a", 65)} {
		if err := Save(path, Config{DefaultModel: FallbackModel, Language: language}); err == nil {
			t.Fatal("invalid language saved")
		}
	}
	if err := os.WriteFile(path, []byte(`{"language":"en\nInjected"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("invalid language loaded")
	}
}
