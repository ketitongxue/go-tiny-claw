//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/app"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestSessionCLIProcessHelper(t *testing.T) {
	if os.Getenv("CLAW_SESSION_CLI_HELPER") != "1" {
		return
	}
	paths, err := config.NewPaths(os.Getenv("CLAW_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	action := os.Getenv("CLAW_SESSION_CLI_ACTION")
	args := []string{"session", action, "active-chat"}
	if action == "list" {
		args = []string{"sessions"}
	}
	var out bytes.Buffer
	if err := runCommand(context.Background(), args, &app.Runtime{Paths: paths}, commandIO{output: &out}); err != nil {
		t.Fatal(err)
	}
	fmt.Print(out.String())
}

func TestSessionCLIWithWriterInAnotherProcess(t *testing.T) {
	home := t.TempDir()
	paths, err := config.NewPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.NewFileStore(paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ctxpkg.NewSessionService(store).Create(context.Background(), "active-chat", t.TempDir(), provider.ModelInfo{ProviderID: "fake", ID: "pinned"}, "none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Append(context.Background(), schema.Message{Role: schema.RoleUser, Content: "private text"}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(action string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSessionCLIProcessHelper$")
		cmd.Env = append(os.Environ(), "CLAW_HOME="+home, "CLAW_SESSION_CLI_HELPER=1", "CLAW_SESSION_CLI_ACTION="+action)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, action := range []string{"list", "show"} {
		out, err := command(action)
		if err != nil || !strings.Contains(out, "fake/pinned") || strings.Contains(out, "private text") {
			t.Fatalf("active %s output=%s, err=%v", action, out, err)
		}
	}
	out, err := command("delete")
	if err == nil || !strings.Contains(out, sessionstore.ErrInUse.Error()) {
		t.Fatalf("active delete output=%s, err=%v", out, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := command("delete"); err != nil {
		t.Fatalf("inactive delete output=%s, err=%v", out, err)
	}
}
