package executor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxWorkspaceProtectedPaths(t *testing.T) {
	home, claw := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAW_HOME", claw)
	for _, name := range []string{".claw", ".ssh", ".aws", ".kube", ".config"} {
		dir := filepath.Join(home, name, "child")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{filepath.Dir(dir), dir} {
			real, _ := canonicalWorkDir(path)
			if err := validateSandboxWorkDir(context.Background(), real); !errors.Is(err, ErrInvalidWorkDir) {
				t.Fatalf("accepted %s: %v", path, err)
			}
		}
	}
	for _, path := range []string{home, filepath.Dir(home), claw} {
		real, _ := canonicalWorkDir(path)
		if err := validateSandboxWorkDir(context.Background(), real); !errors.Is(err, ErrInvalidWorkDir) {
			t.Fatalf("accepted %s: %v", path, err)
		}
	}
	link := filepath.Join(t.TempDir(), "claw-link")
	if err := os.Symlink(claw, link); err != nil {
		t.Fatal(err)
	}
	real, _ := canonicalWorkDir(link)
	if err := validateSandboxWorkDir(context.Background(), real); !errors.Is(err, ErrInvalidWorkDir) {
		t.Fatalf("symlink bypass: %v", err)
	}
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalHostPath(filepath.Join(loop, "auth.json")); err == nil {
		t.Fatal("canonicalization error ignored")
	}
}

func TestSandboxRejectsSocketAndDeviceSymlinks(t *testing.T) {
	root, err := os.MkdirTemp("", "claw-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, target := range []string{socket, "/dev/null"} {
		work := t.TempDir()
		if err := os.Symlink(target, filepath.Join(work, "capability")); err != nil {
			t.Fatal(err)
		}
		if _, err := buildDockerArgs(Request{WorkDir: work}, CodingPolicy()); !errors.Is(err, ErrInvalidWorkDir) {
			t.Fatalf("Docker accepted capability: %v", err)
		}
		e := fakeSRT(t, CodingPolicy(), "exit 0")
		if err := e.ValidateWorkspace(context.Background(), work); !errors.Is(err, ErrInvalidWorkDir) {
			t.Fatalf("Native accepted capability: %v", err)
		}
	}
}

func TestNativeDoesNotAcceptUnenforcedSRT(t *testing.T) {
	// A CLI that ignores settings and reports a plausible version must not
	// qualify as an available native backend.
	path := filepath.Join(t.TempDir(), "srt")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 0.0.80; else echo enforcement-ok; fi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := newNativeExecutor(context.Background(), path, t.TempDir(), CodingPolicy()); !errors.Is(err, ErrNativeUnavailable) {
		t.Fatalf("unenforced SRT accepted: %v", err)
	}
}

func TestNativeSettingsFailureCleanup(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)
	// Force protected-path construction to fail after creating the runtime.
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	e := fakeSRT(t, CodingPolicy(), "exit 0")
	// Invalid CLAW_HOME fails before runtime allocation too; no directory leaks.
	t.Setenv("CLAW_HOME", loop)
	if _, err := e.Execute(context.Background(), Request{WorkDir: t.TempDir()}, nil); err == nil {
		t.Fatal("unsafe settings accepted")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatal("runtime leaked")
	}
}
