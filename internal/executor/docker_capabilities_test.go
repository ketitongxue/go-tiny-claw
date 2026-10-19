package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDockerDaemonMustActuallySupportResourceLimits(t *testing.T) {
	for _, tc := range []struct {
		name, support string
		ok            bool
	}{
		{"supported", "true true true true true", true},
		{"no-memory", "false true true true true", false},
		{"no-swap", "true false true true true", false},
		{"no-cpu", "true true false true true", false},
		{"no-period", "true true true false true", false},
		{"no-pids", "true true true true false", false},
		{"unknown", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			marker, binary := filepath.Join(root, "command-ran"), filepath.Join(root, "docker")
			script := "#!/bin/sh\ncase \"$1\" in info) printf '%s' " + shellQuote(tc.support) + " ;; run) touch " + shellQuote(marker) + " ;; rm) exit 0 ;; esac\n"
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			e, err := NewDockerExecutor(DefaultImage, StrictCodingPolicy())
			if err != nil {
				t.Fatal(err)
			}
			e.dockerBinary = binary
			err = e.CheckResourceSupport(context.Background())
			if (err == nil) != tc.ok {
				t.Fatalf("capability check: %v", err)
			}
			if !tc.ok {
				_, err := e.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "side-effect"}, nil)
				if !errors.Is(err, ErrUnsupportedPolicy) {
					t.Fatalf("unsupported daemon executed: %v", err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("command ran before required capabilities were verified")
				}
			}
		})
	}
}
