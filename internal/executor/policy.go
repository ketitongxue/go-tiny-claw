package executor

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type WorkspaceMode string

const (
	WorkspaceReadOnly  WorkspaceMode = "ro"
	WorkspaceReadWrite WorkspaceMode = "rw"
)

// SandboxPolicy is chosen by application wiring, never by model/tool arguments.
// AllowedEnv is an exact allowlist of explicitly supplied Request.Env keys; it
// does not request automatic inheritance from the host.
type SandboxPolicy struct {
	WorkspaceMode  WorkspaceMode
	NetworkEnabled bool
	MemoryMB       int
	CPUs           float64
	Pids           int
	Timeout        time.Duration
	AllowedEnv     []string
}

func CodingPolicy() SandboxPolicy {
	return SandboxPolicy{
		WorkspaceMode: WorkspaceReadWrite,
		MemoryMB:      1024,
		CPUs:          2,
		Pids:          128,
		Timeout:       DefaultTimeout,
		AllowedEnv:    []string{"PATH", "LANG", "LC_ALL", "TERM"},
	}
}

func ExplorerPolicy() SandboxPolicy {
	return SandboxPolicy{
		WorkspaceMode: WorkspaceReadOnly,
		MemoryMB:      512,
		CPUs:          1,
		Pids:          64,
		Timeout:       DefaultTimeout,
		AllowedEnv:    []string{"PATH", "LANG", "LC_ALL", "TERM"},
	}
}

func (p SandboxPolicy) Validate() error {
	if p.WorkspaceMode != WorkspaceReadOnly && p.WorkspaceMode != WorkspaceReadWrite {
		return fmt.Errorf("%w: unknown workspace mode %q", ErrInvalidPolicy, p.WorkspaceMode)
	}
	if p.MemoryMB <= 0 || p.CPUs <= 0 || math.IsNaN(p.CPUs) || math.IsInf(p.CPUs, 0) || p.Pids <= 0 || p.Timeout <= 0 {
		return fmt.Errorf("%w: memory, CPUs, PIDs and timeout must be finite and positive", ErrInvalidPolicy)
	}
	for _, key := range p.AllowedEnv {
		if !validEnvName(key) {
			return fmt.Errorf("%w: invalid allowed environment name %q", ErrInvalidPolicy, key)
		}
	}
	return nil
}

func validEnvName(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func (p SandboxPolicy) environment(env map[string]string) ([]string, error) {
	allowed := make(map[string]bool, len(p.AllowedEnv))
	for _, key := range p.AllowedEnv {
		allowed[key] = true
	}
	for key, value := range env {
		if !allowed[key] || !validEnvName(key) {
			return nil, fmt.Errorf("%w: environment key %q is not allowed", ErrInvalidPolicy, key)
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("%w: environment value for %q contains NUL", ErrInvalidPolicy, key)
		}
	}
	return environmentList(env), nil
}

func (p SandboxPolicy) executionTimeout(requested time.Duration) (time.Duration, error) {
	if requested < 0 {
		return 0, fmt.Errorf("%w: requested timeout must not be negative", ErrInvalidPolicy)
	}
	if requested == 0 || requested > p.Timeout {
		return p.Timeout, nil
	}
	return requested, nil
}
