package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestTraceExportsPreserveEveryConcurrentRun(t *testing.T) {
	dir := t.TempDir()
	const count = 32
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, span := StartSpan(context.Background(), fmt.Sprintf("run-%d", i))
			span.EndSpan()
			if err := ExportTraceToFile(span, dir, "chat-a"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	paths, err := filepath.Glob(filepath.Join(dir, ".claw", "traces", "trace_chat-a_*.json"))
	if err != nil || len(paths) != count {
		t.Fatalf("lost trace exports: got %d, want %d (%v)", len(paths), count, err)
	}
	seen := make(map[string]bool)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var span Span
		if err := json.Unmarshal(data, &span); err != nil {
			t.Fatalf("invalid trace %s: %v", path, err)
		}
		seen[span.Name] = true
	}
	if len(seen) != count {
		t.Fatalf("trace contents were overwritten: got %d distinct runs", len(seen))
	}
}

func TestTraceSessionIDCannotEscapeTraceDirectory(t *testing.T) {
	for _, id := range []string{"../../escape", `..\..\escape`, strings.Repeat("long", 100), ""} {
		dir := t.TempDir()
		_, span := StartSpan(context.Background(), "run")
		span.EndSpan()
		if err := ExportTraceToFile(span, dir, id); err != nil {
			t.Errorf("export for %q failed: %v", id, err)
			continue
		}
		traceDir := filepath.Join(dir, ".claw", "traces")
		paths, err := filepath.Glob(filepath.Join(traceDir, "trace_*.json"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("trace not exported inside its directory: %v (%v)", paths, err)
		}
	}
}

func TestTraceExportReturnsDirectoryCreationError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claw"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	_, span := StartSpan(context.Background(), "run")
	if err := ExportTraceToFile(span, dir, "chat-a"); err == nil {
		t.Fatal("trace export succeeded without a writable trace directory")
	}
}
