package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putPromptFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestPromptCLIAndProjectTrustOffline(t *testing.T) {
	home, base := t.TempDir(), t.TempDir()
	t.Setenv("CLAW_HOME", home)
	t.Setenv("PATH", t.TempDir()) // cannot contact Docker or execute git
	// These commands must not parse auth/model state or use any provider.
	putPromptFile(t, filepath.Join(home, "auth.json"), "damaged auth")
	putPromptFile(t, filepath.Join(home, "models.json"), "damaged models")
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(repo, "backend")
	putPromptFile(t, filepath.Join(home, "AGENTS.md"), "USER CONTEXT SECRET")
	putPromptFile(t, filepath.Join(home, "SYSTEM.md"), "USER CORE SECRET")
	putPromptFile(t, filepath.Join(home, "APPEND_SYSTEM.md"), "USER APPEND SECRET")
	putPromptFile(t, filepath.Join(repo, "AGENTS.md"), "REPO CONTEXT SECRET")
	putPromptFile(t, filepath.Join(workspace, "AGENTS.override.md"), "NESTED CONTEXT SECRET")
	putPromptFile(t, filepath.Join(workspace, "AGENTS.md"), "IGNORED CONTEXT SECRET")
	putPromptFile(t, filepath.Join(workspace, ".claw", "SYSTEM.md"), "PROJECT CORE SECRET")
	putPromptFile(t, filepath.Join(workspace, ".claw", "APPEND_SYSTEM.md"), "PROJECT APPEND SECRET")
	putPromptFile(t, filepath.Join(workspace, ".claw", "skills", "go-testing", "SKILL.md"), "---\nname: go-testing\ndescription: Go unit tests\n---\nSKILL BODY SECRET")
	runCLI := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatal(args, err, out.String())
		}
		return out.String()
	}
	if out := runCLI("trust", "status", "-workdir", workspace); !strings.Contains(out, "Trusted: false") {
		t.Fatal(out)
	}
	out := runCLI("prompt", "show", "-workdir", workspace)
	if !strings.Contains(out, "claw trust") || !strings.Contains(out, "SECTION") || strings.Contains(out, "SECRET") {
		t.Fatal(out)
	}
	out = runCLI("prompt", "render", "-workdir", workspace)
	if !strings.Contains(out, "WARNING: may contain") || !strings.Contains(out, "USER CORE SECRET") || strings.Contains(out, "PROJECT CORE SECRET") || strings.Contains(out, "SKILL BODY SECRET") {
		t.Fatal(out)
	}
	if out := runCLI("trust", "-workdir", workspace); !strings.Contains(out, "Trusted: true") || !strings.Contains(out, "Sandbox") {
		t.Fatal(out)
	}
	if out := runCLI("trust", "status", "-workdir", repo); !strings.Contains(out, "Trusted: true") {
		t.Fatal("trust not keyed to root", out)
	}
	out = runCLI("prompt", "show", "-workdir", workspace)
	if !strings.Contains(out, "1 skills") || !strings.Contains(out, "SYSTEM.md") || strings.Contains(out, "SECRET") {
		t.Fatal(out)
	}
	out = runCLI("prompt", "render", "-workdir", workspace, "-plan")
	for _, want := range []string{"PROJECT CORE SECRET", "USER CONTEXT SECRET", "REPO CONTEXT SECRET", "NESTED CONTEXT SECRET", "USER APPEND SECRET", "PROJECT APPEND SECRET", "<name>go-testing</name>", "Plan Mode: ON"} {
		if !strings.Contains(out, want) {
			t.Fatal(want, out)
		}
	}
	for _, absent := range []string{"USER CORE SECRET", "IGNORED CONTEXT SECRET", "SKILL BODY SECRET"} {
		if strings.Contains(out, absent) {
			t.Fatal(absent, out)
		}
	}
	out = runCLI("prompt", "render", "-workdir", workspace, "-agent", "explorer", "-sandbox", "docker", "-plan")
	if !strings.Contains(out, "Explorer Subagent") || !strings.Contains(out, "bash") || strings.Contains(out, "write_file") || strings.Contains(out, "edit_file") || strings.Contains(out, "Plan Mode: ON") {
		t.Fatal(out)
	}
	out = runCLI("prompt", "render", "-workdir", workspace, "-agent", "explorer", "-sandbox", "local")
	if strings.Contains(out, "bash") || !strings.Contains(out, "read_file") {
		t.Fatal(out)
	}
	if out := runCLI("trust", "revoke", "-workdir", repo); !strings.Contains(out, "Trusted: false") {
		t.Fatal(out)
	}
	out = runCLI("prompt", "render", "-workdir", workspace)
	if strings.Contains(out, "PROJECT CORE SECRET") || strings.Contains(out, "<name>go-testing</name>") {
		t.Fatal(out)
	}
	// Inspection creates no conversation or trace files.
	for _, path := range []string{filepath.Join(home, "sessions"), filepath.Join(workspace, ".claw", "traces")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal(path, err)
		}
	}
}

func TestPromptResourceCLIArgumentsAndHomeBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	for _, args := range [][]string{
		{"trust", "unexpected"}, {"trust", "status", "-workdir", home}, {"trust", "revoke", "extra"},
		{"prompt"}, {"prompt", "unknown"}, {"prompt", "show", "-agent", "writer"}, {"prompt", "show", "-workdir", home}, {"prompt", "render", "-sandbox", "bad"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err == nil {
			t.Fatal("bad args accepted", args, out.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"trust", "status"}, &bytes.Buffer{}); err != context.Canceled {
		t.Fatal(err)
	}
}
