package executor

import (
	"context"
	"errors"
	"fmt"
)

const (
	ModeAuto   = "auto"
	ModeNative = "native"
	ModeDocker = "docker"
	ModeLocal  = "local"
)

func ValidateMode(mode string) error {
	switch mode {
	case ModeAuto, ModeNative, ModeDocker, ModeLocal:
		return nil
	default:
		return fmt.Errorf("unknown sandbox mode %q; choose native, docker, local or auto", mode)
	}
}

type backendCandidate struct {
	mode         string
	capabilities Capabilities
	build        func(context.Context, SandboxPolicy) (Executor, error)
}

func configuredCandidates(image, workDir string) []backendCandidate {
	return []backendCandidate{
		{ModeNative, NativeCapabilities(), func(ctx context.Context, p SandboxPolicy) (Executor, error) {
			return NewNativeExecutor(ctx, workDir, p)
		}},
		{ModeDocker, DockerCapabilities(), func(ctx context.Context, p SandboxPolicy) (Executor, error) {
			e, err := NewDockerExecutor(image, p)
			if err != nil {
				return nil, err
			}
			if err := DockerAvailable(ctx); err != nil {
				return nil, err
			}
			if err := e.CheckResourceSupport(ctx); err != nil {
				return nil, err
			}
			if err := e.CheckImage(ctx); err != nil {
				return nil, err
			}
			return e, nil
		}},
	}
}

// NewConfigured returns ONE concrete backend. There is intentionally no routing
// executor: once Execute starts, no error can trigger a cross-backend replay.
// workDir is required so Native can reject project-writable SRT installations
// before running any host-side version or enforcement probes.
func NewConfigured(ctx context.Context, mode, image, workDir string, policy SandboxPolicy) (Executor, error) {
	candidates := configuredCandidates(image, workDir)
	return selectConfigured(ctx, mode, policy, candidates)
}

func selectConfigured(ctx context.Context, mode string, policy SandboxPolicy, candidates []backendCandidate) (Executor, error) {
	if err := ValidateMode(mode); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if mode == ModeLocal {
		if err := validateLocalPolicy(policy); err != nil {
			return nil, err
		}
		return NewLocalExecutor(), nil
	}
	var failures []error
	for _, candidate := range candidates {
		if candidate.mode == ModeLocal {
			continue
		} // Local is NEVER an auto candidate.
		if mode != ModeAuto && mode != candidate.mode {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := requireCapabilities(candidate.mode, candidate.capabilities, policy); err != nil {
			failures = append(failures, err)
			continue
		}
		e, err := candidate.build(ctx, policy.clone())
		if err == nil && e != nil {
			return e, nil
		}
		if err == nil {
			err = errors.New("constructor returned no executor")
		}
		failures = append(failures, fmt.Errorf("%s: %w", candidate.mode, err))
	}
	return nil, fmt.Errorf("no usable Sandbox for %s (Local is never automatic): %w", mode, errors.Join(failures...))
}
