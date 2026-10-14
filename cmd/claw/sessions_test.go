package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/app"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestSessionCommandsOnlyMetadataAndDeleteStore(t *testing.T) {
	home := t.TempDir()
	paths, err := config.NewPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &app.Runtime{Paths: paths}
	store, err := sessionstore.NewFileStore(paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sess, err := ctxpkg.NewSessionService(store).Create(context.Background(), "demo-id", dir, provider.ModelInfo{ProviderID: "custom", ID: "model"}, "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(schema.Message{Role: schema.RoleUser, Content: "private prompt must not display"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"sessions"}, {"session", "show", "demo-id"}} {
		var out bytes.Buffer
		if err := runCommand(context.Background(), args, runtime, commandIO{output: &out}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "custom/model") || strings.Contains(out.String(), "private prompt") {
			t.Fatal(out.String())
		}
	}
	var out bytes.Buffer
	if err := runCommand(context.Background(), []string{"session", "delete", "demo-id"}, runtime, commandIO{output: &out}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("delete removed workdir", err)
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatal(err)
	}
}
