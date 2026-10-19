// sandbox-bench measures disposable sandbox startup + execution + cleanup.
// It uses generated fixtures only; no model, credentials or public network.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/executor"
)

func main() {
	mode := flag.String("sandbox", "both", "native | docker | both (measured independently; never fallback)")
	image := flag.String("sandbox-image", executor.DefaultImage, "prebuilt trusted Docker image")
	flag.Parse()
	if *mode != "both" && *mode != executor.ModeNative && *mode != executor.ModeDocker {
		fmt.Fprintln(os.Stderr, "invalid sandbox")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := benchmark(ctx, *mode, *image); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func benchmark(ctx context.Context, mode, image string) error {
	work, err := os.MkdirTemp("", "claw-sandbox-bench-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	for name, content := range map[string]string{
		"go.mod":      "module sandboxbench\n\ngo 1.26.2\n",
		"sum.go":      "package sandboxbench\nfunc Sum(a,b int) int { return a+b }\n",
		"sum_test.go": "package sandboxbench\nimport \"testing\"\nfunc TestSum(t *testing.T) { if Sum(1,2)!=3 { t.Fatal(\"sum\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(initCtx, "git", "init", "--quiet", work).CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w (%s)", err, out)
	}
	fmt.Println("backend\tcommand\tn\tmedian_ms\tp95_ms")
	for _, backend := range []string{executor.ModeNative, executor.ModeDocker} {
		if mode != "both" && mode != backend {
			continue
		}
		p := executor.CodingPolicy()
		p.Timeout = 2 * time.Minute
		e, err := executor.NewConfigured(ctx, backend, image, work, p)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Printf("SKIPPED %s: %v\n", backend, err)
			continue
		}
		for _, task := range []struct {
			name, command string
			n             int
		}{
			{"echo", "echo hello", 30}, {"git-status", "git status --porcelain", 30}, {"go-test-small", "go test ./...", 10},
		} {
			var samples []time.Duration
			for i := 0; i < task.n; i++ {
				r, err := e.Execute(ctx, executor.Request{Command: task.command, WorkDir: work}, nil)
				if err != nil || r.ExitCode != 0 {
					return fmt.Errorf("%s %s sample %d: exit=%d err=%v stdout=%s stderr=%s", backend, task.name, i, r.ExitCode, err, r.Stdout, r.Stderr)
				}
				samples = append(samples, r.Duration)
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			median := samples[len(samples)/2]
			if len(samples)%2 == 0 {
				median = (samples[len(samples)/2-1] + median) / 2
			}
			p95 := samples[int(math.Ceil(float64(len(samples))*.95))-1]
			fmt.Printf("%s\t%s\t%d\t%.2f\t%.2f\n", backend, task.name, task.n, float64(median)/float64(time.Millisecond), float64(p95)/float64(time.Millisecond))
		}
	}
	return nil
}
