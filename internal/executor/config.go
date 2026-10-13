package executor

import (
	"context"
	"fmt"
)

const (
	ModeAuto   = "auto"
	ModeDocker = "docker"
	ModeLocal  = "local"
)

func ValidateMode(mode string) error {
	switch mode {
	case ModeAuto, ModeDocker, ModeLocal:
		return nil
	default:
		return fmt.Errorf("unknown sandbox mode %q; choose docker, local or auto", mode)
	}
}

// NewConfigured checks the selected backend before an agent starts. Auto is
// fail-closed: it ONLY selects Docker. Local must always be explicitly requested.
func NewConfigured(ctx context.Context, mode, image string, policy SandboxPolicy) (Executor, error) {
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
		if policy.WorkspaceMode == WorkspaceReadOnly {
			return nil, fmt.Errorf("LocalExecutor is unsafe and cannot enforce a read-only workspace")
		}
		return NewLocalExecutor(), nil
	}
	e, err := NewDockerExecutor(image, policy)
	if err != nil {
		return nil, err
	}
	if err := DockerAvailable(ctx); err != nil {
		return nil, fmt.Errorf("no usable Docker Sandbox; explicitly use -sandbox=local only in a trusted environment: %w", err)
	}
	if err := e.CheckImage(ctx); err != nil {
		return nil, err
	}
	return e, nil
}
