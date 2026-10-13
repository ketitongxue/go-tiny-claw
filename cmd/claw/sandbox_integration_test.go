package main

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/executor"
)

// Exercise real registry/engine wiring and Docker isolation with an offline
// provider: no API key, network access, model nondeterminism or API cost required.
func TestDockerSessionFactoryIntegration(t *testing.T) {
	if err := executor.DockerAvailable(context.Background()); err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	image := os.Getenv("CLAW_SANDBOX_TEST_IMAGE")
	if image == "" {
		image = executor.DefaultImage
	}
	probe, err := executor.NewDockerExecutor(image, executor.CodingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.CheckImage(context.Background()); err != nil {
		t.Skipf("build the sandbox image first: %v", err)
	}
	opts := options{mode: "demo", model: "glm-5.3", workDir: t.TempDir(), sandbox: executor.ModeDocker, sandboxImage: image}
	executors, err := configureExecutors(context.Background(), opts, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runSessionFactoryRegression(t, opts, executors)
}
