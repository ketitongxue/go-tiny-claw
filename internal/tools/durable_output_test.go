package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileKeepsCompleteUnicodeOutput(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("中文内容😀", 10000)
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := NewReadFileTool(dir).Execute(context.Background(), []byte(`{"path":"large.txt"}`))
	if err != nil || got != content {
		t.Fatal("tool truncated durable facts", err)
	}
}
