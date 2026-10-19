package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/executor"
)

func TestApplicationSandboxPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy, network, domains string
		ro, fail                 bool
	}{
		{"coding", "denied", "", false, false}, {"strict", "denied", "", true, false},
		{"coding", "allowlist", "github.com, *.githubusercontent.com", false, false},
		{"coding", "allowlist", "", false, true}, {"coding", "denied", "github.com", false, true},
		{"strict", "unrestricted", "", false, false}, {"unknown", "denied", "", false, true},
		{"coding", "unrestricted", "github.com", false, true}, {"coding", "allowlist", "*", false, true},
	} {
		p, err := applicationSandboxPolicy(options{sandboxPolicy: tc.policy, sandboxNetwork: tc.network, sandboxDomains: tc.domains}, tc.ro)
		if (err != nil) != tc.fail {
			t.Fatalf("%+v: %v", tc, err)
		}
		if err == nil && (tc.policy == "strict") != (p.ResourceEnforcement == executor.ResourceRequired) {
			t.Fatalf("strict policy lost: %+v", p)
		}
		if err == nil && tc.ro && p.WorkspaceMode != executor.WorkspaceReadOnly {
			t.Fatal("Explorer not RO")
		}
	}
}

func TestSandboxDiagnosticWithoutProvider(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"sandbox", "-sandbox=local", "-workdir=" + t.TempDir(), "-command=echo diagnostic"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "UNSAFE") || !strings.Contains(out.String(), "diagnostic\n") || !strings.Contains(out.String(), "exit: 0") {
		t.Fatal(out.String())
	}
	for _, args := range [][]string{
		{"-command=echo", "-policy=explorer", "-sandbox=local"}, {"-command=echo", "-policy=strict", "-sandbox=native"},
		{"-command=echo", "-sandbox-network=allowlist"}, {"-command=echo", "-timeout=-1s"}, {},
	} {
		if err := sandboxCommand(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("invalid policy accepted: %v", args)
		}
	}
}

func TestNativeStartupRejectsWorkspaceSRTBeforeExecution(t *testing.T) {
	for _, mode := range []string{executor.ModeNative, executor.ModeAuto} {
		for _, entrypoint := range []string{"agent", "diagnostic"} {
			t.Run(mode+"/"+entrypoint, func(t *testing.T) {
				installDockerStub(t, `case "$1" in
version) echo 29.0.0 ;;
image) echo sha256:test ;;
run) echo docker-command-executed ;;
rm) exit 0 ;;
*) exit 2 ;;
esac`)
				work := t.TempDir()
				marker := filepath.Join(t.TempDir(), "srt-executed-on-host")
				// A plausible version must not let a project-local executable
				// run on the host, even when subsequent isolation probes fail.
				script := "#!/bin/sh\nprintf unexpected > '" + marker + "'\nif [ \"$1\" = --version ]; then echo 0.0.80; else exit 1; fi\n"
				if err := os.WriteFile(filepath.Join(work, "srt"), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", work+string(os.PathListSeparator)+os.Getenv("PATH"))

				var out bytes.Buffer
				var err error
				if entrypoint == "agent" {
					_, err = configureExecutors(context.Background(), options{mode: "demo", sandbox: mode, sandboxImage: executor.DefaultImage, workDir: work}, &out)
				} else {
					err = sandboxCommand(context.Background(), []string{"-sandbox=" + mode, "-workdir=" + work, "-command=true"}, &out)
				}
				if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
					t.Fatalf("workspace SRT ran before installation validation: %v (startup: %v)", statErr, err)
				}
				if mode == executor.ModeNative {
					if !errors.Is(err, executor.ErrInvalidWorkDir) {
						t.Fatalf("unsafe native installation accepted: %v", err)
					}
				} else if err != nil || !strings.Contains(out.String(), "backend: docker") {
					t.Fatalf("auto did not select Docker without running unsafe SRT: %v (%s)", err, out.String())
				}
			})
		}
	}
}

func TestNativeSessionFactoryIntegration(t *testing.T) {
	if _, err := exec.LookPath("srt"); err != nil {
		t.Skip("SKIPPED: srt is not installed")
	}
	opts := options{mode: "demo", sandbox: executor.ModeNative, sandboxImage: executor.DefaultImage, workDir: t.TempDir()}
	var out bytes.Buffer
	executors, err := configureExecutors(context.Background(), opts, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "backend: native") || !strings.Contains(out.String(), "best-effort") {
		t.Fatal(out.String())
	}
	runSessionFactoryRegression(t, opts, executors)
}
