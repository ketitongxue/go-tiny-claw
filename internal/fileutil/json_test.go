package fileutil

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAtomicWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	type config struct {
		Value string `json:"value"`
	}
	if err := WriteJSON(path, config{Value: "first"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				var c config
				if err := ReadJSON(path, &c); err != nil || c.Value == "" {
					t.Error("partial JSON observed", err)
				}
			}
		}()
	}
	for range 20 {
		if err := WriteJSON(path, config{Value: "updated"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".claw-*.tmp"))
	if len(files) != 0 {
		t.Fatal("temp leaked")
	}
}
func TestInvalidAndDuplicateJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "json")
	for _, data := range []string{`{"x":1,"x":2}`, `{"providers":{"id":{},"id":{}}}`, `null`, `{} {}`, `{"x":[{"a":1,"a":2}]}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var dst map[string]any
		if err := ReadJSON(path, &dst); err == nil {
			t.Fatal("invalid accepted", data)
		}
	}
}
