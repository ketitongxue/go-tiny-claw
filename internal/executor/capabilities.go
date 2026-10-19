package executor

import (
	"errors"
	"fmt"
	"strings"
)

var ErrUnsupportedPolicy = errors.New("backend cannot enforce sandbox policy")

type Capabilities struct {
	FilesystemIsolation bool
	NetworkIsolation    bool
	NetworkAllowlist    bool
	UnrestrictedNetwork bool
	MemoryLimit         bool
	CPULimit            bool
	PIDLimit            bool
}

func NativeCapabilities() Capabilities {
	return Capabilities{FilesystemIsolation: true, NetworkIsolation: true, NetworkAllowlist: true}
}
func DockerCapabilities() Capabilities {
	return Capabilities{FilesystemIsolation: true, NetworkIsolation: true, UnrestrictedNetwork: true, MemoryLimit: true, CPULimit: true, PIDLimit: true}
}
func LocalCapabilities() Capabilities { return Capabilities{UnrestrictedNetwork: true} }

func (p SandboxPolicy) RequiredCapabilities() Capabilities {
	c := Capabilities{FilesystemIsolation: true, NetworkIsolation: true}
	if p.NetworkEnabled {
		c.NetworkAllowlist = len(p.AllowedDomains) > 0
		c.UnrestrictedNetwork = len(p.AllowedDomains) == 0
	}
	if p.ResourceEnforcement == ResourceRequired {
		c.MemoryLimit, c.CPULimit, c.PIDLimit = true, true, true
	}
	return c
}

func (c Capabilities) missing(required Capabilities) []string {
	var names []string
	for _, field := range []struct {
		name       string
		have, need bool
	}{
		{"filesystem", c.FilesystemIsolation, required.FilesystemIsolation},
		{"network", c.NetworkIsolation, required.NetworkIsolation},
		{"network-allowlist", c.NetworkAllowlist, required.NetworkAllowlist},
		{"unrestricted-network", c.UnrestrictedNetwork, required.UnrestrictedNetwork},
		{"memory", c.MemoryLimit, required.MemoryLimit},
		{"cpu", c.CPULimit, required.CPULimit},
		{"pids", c.PIDLimit, required.PIDLimit},
	} {
		if field.need && !field.have {
			names = append(names, field.name)
		}
	}
	return names
}

func requireCapabilities(backend string, c Capabilities, p SandboxPolicy) error {
	if missing := c.missing(p.RequiredCapabilities()); len(missing) > 0 {
		return fmt.Errorf("%w: %w: %s sandbox cannot enforce required capabilities: %s", ErrInvalidPolicy, ErrUnsupportedPolicy, backend, strings.Join(missing, ","))
	}
	return nil
}

// Explicit local is an UNSAFE opt-out of ordinary isolation, not a backend
// satisfying RequiredCapabilities. Enforcement-dependent policies still refuse.
func validateLocalPolicy(p SandboxPolicy) error {
	if p.WorkspaceMode == WorkspaceReadOnly || p.ResourceEnforcement == ResourceRequired || p.NetworkEnabled && len(p.AllowedDomains) > 0 {
		return fmt.Errorf("%w: LocalExecutor is UNSAFE and cannot enforce read-only, required resources or network allowlists", ErrUnsupportedPolicy)
	}
	return nil
}

// Status exposes application-owned configuration without credentials or paths.
type Status struct {
	Backend      string
	Policy       SandboxPolicy
	Capabilities Capabilities
}

func (s Status) String() string {
	if s.Backend == ModeLocal {
		return "Sandbox:\n  backend: local (UNSAFE)\n  filesystem: host user permissions\n  network: unrestricted\n  resources: unenforced\n"
	}
	network := "denied"
	if s.Policy.NetworkEnabled {
		if len(s.Policy.AllowedDomains) > 0 {
			network = "allowlist"
		} else {
			network = "unrestricted (isolated namespace)"
		}
	}
	text := fmt.Sprintf("Sandbox:\n  backend: %s\n  filesystem: restricted (workspace %s)\n  network: %s\n", s.Backend, s.Policy.WorkspaceMode, network)
	if s.Capabilities.MemoryLimit && s.Capabilities.CPULimit && s.Capabilities.PIDLimit {
		return text + fmt.Sprintf("  memory: %d MB\n  cpus: %g\n  pids: %d\n", s.Policy.MemoryMB, s.Policy.CPUs, s.Policy.Pids)
	}
	return text + "  resources: best-effort (CPU/memory/PID NOT enforced; timeout enforced)\n"
}

func (e *NativeExecutor) Status() Status {
	return Status{ModeNative, e.policy.clone(), NativeCapabilities()}
}
func (e *DockerExecutor) Status() Status {
	return Status{ModeDocker, e.policy.clone(), DockerCapabilities()}
}
func (e *LocalExecutor) Status() Status {
	return Status{Backend: ModeLocal, Capabilities: LocalCapabilities()}
}
