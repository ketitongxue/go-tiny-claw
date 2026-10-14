package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomModels(t *testing.T) {
	valid := `{"providers":{"local":{"name":"Local","api":"openai-compatible","base_url":"http://localhost:11434/v1","auth":{"type":"none"},"models":[{"id":"org/qwen","tool_calling":true}]}}}`
	path := filepath.Join(t.TempDir(), "models.json")
	if _, err := LoadModels(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	models, err := LoadModels(path)
	if err != nil || models.Providers["local"].Models[0].ProviderID != "local" {
		t.Fatal(models, err)
	}
	for _, bad := range []string{
		strings.Replace(valid, `"local":`, `"openai":`, 1),
		strings.Replace(valid, `"local":`, `"zai-coding-cn":`, 1),
		strings.Replace(valid, `"local":`, `"a/b":`, 1),
		strings.Replace(valid, "http://localhost:11434/v1", "file:///tmp", 1),
		strings.Replace(valid, "http://localhost:11434/v1", "", 1),
		strings.Replace(valid, "http://localhost:11434/v1", "https://secret@host/v1", 1),
		strings.Replace(valid, `"type":"none"`, `"type":"oauth"`, 1),
		strings.Replace(valid, `"type":"none"`, `"type":"api_key","env":"$(command)"`, 1),
		strings.Replace(valid, `"type":"none"`, `"type":"api_key","env":"BAD-NAME"`, 1),
		strings.Replace(valid, `"type":"none"`, `"type":"api_key","api_key":"literal-secret"`, 1),
		strings.Replace(valid, `"tool_calling":true`, `"tool_calling":true,"billing":"free"`, 1),
		`{"providers":`,
	} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadModels(path); err == nil {
			t.Fatalf("accepted invalid config %s", bad)
		}
	}
	valid = strings.Replace(valid, `"type":"none"`, `"type":"api_key","env":"COMPANY_AI_API_KEY"`, 1)
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModels(path); err != nil {
		t.Fatal(err)
	}
}
