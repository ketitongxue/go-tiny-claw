package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBackendCapabilitySelection(t *testing.T) {
	for _, tc := range []struct {
		name, mode             string
		strict, native, docker bool
		want                   string
	}{
		{"normal-prefer-native", ModeAuto, false, true, true, ModeNative},
		{"normal-docker-only", ModeAuto, false, false, true, ModeDocker},
		{"normal-unavailable", ModeAuto, false, false, false, ""},
		{"strict-docker", ModeAuto, true, true, true, ModeDocker},
		{"strict-no-docker", ModeAuto, true, true, false, ""},
		{"explicit-native-strict", ModeNative, true, true, true, ""},
		{"explicit-docker-strict", ModeDocker, true, true, true, ModeDocker},
		{"explicit-native-unavailable", ModeNative, false, false, true, ""},
		{"explicit-docker-unavailable", ModeDocker, false, true, false, ""},
		{"explicit-local", ModeLocal, false, false, false, ModeLocal},
		{"explicit-local-strict", ModeLocal, true, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := CodingPolicy()
			if tc.strict {
				p = StrictCodingPolicy()
			}
			var nativeBuilds, dockerBuilds int
			candidates := []backendCandidate{
				{ModeNative, NativeCapabilities(), func(context.Context, SandboxPolicy) (Executor, error) {
					nativeBuilds++
					if !tc.native {
						return nil, ErrNativeUnavailable
					}
					return &NativeExecutor{policy: p}, nil
				}},
				{ModeDocker, DockerCapabilities(), func(context.Context, SandboxPolicy) (Executor, error) {
					dockerBuilds++
					if !tc.docker {
						return nil, ErrDockerUnavailable
					}
					return &DockerExecutor{policy: p}, nil
				}},
				{ModeLocal, Capabilities{}, func(context.Context, SandboxPolicy) (Executor, error) {
					t.Fatal("auto selected Local")
					return nil, nil
				}},
			}
			e, err := selectConfigured(context.Background(), tc.mode, p, candidates)
			if tc.want == "" {
				if err == nil || e != nil {
					t.Fatalf("unsafe choice: %T %v", e, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			status := e.(interface{ Status() Status }).Status()
			if status.Backend != tc.want {
				t.Fatalf("selected %s, want %s", status.Backend, tc.want)
			}
			if tc.want == ModeNative && dockerBuilds != 0 || tc.strict && nativeBuilds != 0 {
				t.Fatal("built incompatible / unnecessary backend")
			}
		})
	}
	if _, err := NewConfigured(context.Background(), ModeNative, DefaultImage, t.TempDir(), StrictCodingPolicy()); !errors.Is(err, ErrUnsupportedPolicy) || !strings.Contains(err.Error(), "memory,cpu,pids") {
		t.Fatalf("native resource downgrade: %v", err)
	}
	if _, err := NewConfigured(context.Background(), ModeLocal, "", t.TempDir(), ExplorerPolicy()); err == nil {
		t.Fatal("local enforced RO")
	}
}

func TestBackendRuntimeFailureIsNeverReplayed(t *testing.T) {
	for _, mode := range []string{ModeNative, ModeDocker} {
		t.Run(mode, func(t *testing.T) {
			var builds, executions int
			runtimeErr := errors.New("sandbox failed after partial side effect")
			candidates := []backendCandidate{
				{mode, DockerCapabilities(), func(context.Context, SandboxPolicy) (Executor, error) {
					builds++
					return failingExecutor{&executions, runtimeErr}, nil
				}},
				{"other-backend", DockerCapabilities(), func(context.Context, SandboxPolicy) (Executor, error) {
					t.Fatal("runtime fallback attempted")
					return nil, nil
				}},
			}
			e, err := selectConfigured(context.Background(), ModeAuto, CodingPolicy(), candidates)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.Execute(context.Background(), Request{Command: "side-effect"}, nil)
			if !errors.Is(err, runtimeErr) || builds != 1 || executions != 1 {
				t.Fatalf("replayed command: builds=%d execs=%d err=%v", builds, executions, err)
			}
		})
	}
}

type failingExecutor struct {
	executions *int
	err        error
}

func (e failingExecutor) Execute(context.Context, Request, OutputHandler) (Result, error) {
	(*e.executions)++
	return Result{ExitCode: -1}, e.err
}

func TestNetworkCapabilitiesRouteBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domains []string
		strict  bool
		want    string
	}{
		{"allowlist", []string{"github.com"}, false, ModeNative},
		{"unrestricted", nil, false, ModeDocker},
		{"strict-and-allowlist", []string{"github.com"}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := CodingPolicy()
			p.NetworkEnabled, p.AllowedDomains = true, tc.domains
			if tc.strict {
				p.ResourceEnforcement = ResourceRequired
			}
			var chosen string
			var candidates []backendCandidate
			for _, b := range configuredCandidates(DefaultImage, t.TempDir()) {
				backend := b.mode
				b.build = func(context.Context, SandboxPolicy) (Executor, error) {
					chosen = backend
					return &NativeExecutor{}, nil
				}
				candidates = append(candidates, b)
			}
			e, err := selectConfigured(context.Background(), ModeAuto, p, candidates)
			if chosen != tc.want || (tc.want == "") != (err != nil) || tc.want == "" && e != nil {
				t.Fatalf("chosen=%s err=%v", chosen, err)
			}
		})
	}
	p := CodingPolicy()
	p.NetworkEnabled = true
	p.AllowedDomains = []string{"github.com"}
	if _, err := NewConfigured(context.Background(), ModeLocal, "", t.TempDir(), p); err == nil {
		t.Fatal("Local claimed domain enforcement")
	}
}

func TestCapabilityRequirements(t *testing.T) {
	if n := NativeCapabilities(); n.MemoryLimit || n.CPULimit || n.PIDLimit || !n.FilesystemIsolation || !n.NetworkAllowlist {
		t.Fatalf("inaccurate native capabilities: %+v", n)
	}
	if d := DockerCapabilities(); !d.MemoryLimit || !d.CPULimit || !d.PIDLimit || d.NetworkAllowlist {
		t.Fatalf("inaccurate Docker capabilities: %+v", d)
	}
	p := CodingPolicy()
	p.NetworkEnabled = true
	p.AllowedDomains = []string{"github.com"}
	if err := requireCapabilities(ModeDocker, DockerCapabilities(), p); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatal("Docker claimed domain filtering")
	}
	p.ResourceEnforcement = ResourceRequired
	for _, c := range []Capabilities{NativeCapabilities(), DockerCapabilities()} {
		if len(c.missing(p.RequiredCapabilities())) == 0 {
			t.Fatal("no backend can enforce strict resources + allowlist yet")
		}
	}
	p = CodingPolicy()
	p.ResourceEnforcement = "invalid"
	if p.Validate() == nil {
		t.Fatal("invalid enforcement accepted")
	}
}
