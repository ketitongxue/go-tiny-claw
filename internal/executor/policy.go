package executor

import (
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

type WorkspaceMode string

const (
	WorkspaceReadOnly  WorkspaceMode = "ro"
	WorkspaceReadWrite WorkspaceMode = "rw"
)

type ResourceEnforcement string

const (
	ResourceBestEffort ResourceEnforcement = "best-effort"
	ResourceRequired   ResourceEnforcement = "required"
)

// SandboxPolicy is chosen by application wiring, never by model/tool arguments.
// AllowedEnv is an exact allowlist of explicitly supplied Request.Env keys; it
// does not request automatic inheritance from the host.
type SandboxPolicy struct {
	WorkspaceMode       WorkspaceMode
	NetworkEnabled      bool
	AllowedDomains      []string // enabled + empty means unrestricted, NOT deny-all
	ResourceEnforcement ResourceEnforcement
	MemoryMB            int
	CPUs                float64
	Pids                int
	Timeout             time.Duration
	AllowedEnv          []string
}

func CodingPolicy() SandboxPolicy {
	return SandboxPolicy{
		WorkspaceMode:       WorkspaceReadWrite,
		ResourceEnforcement: ResourceBestEffort,
		MemoryMB:            1024,
		CPUs:                2,
		Pids:                128,
		Timeout:             DefaultTimeout,
		AllowedEnv:          []string{"PATH", "LANG", "LC_ALL", "TERM"},
	}
}

func ExplorerPolicy() SandboxPolicy {
	return SandboxPolicy{
		WorkspaceMode:       WorkspaceReadOnly,
		ResourceEnforcement: ResourceBestEffort,
		MemoryMB:            512,
		CPUs:                1,
		Pids:                64,
		Timeout:             DefaultTimeout,
		AllowedEnv:          []string{"PATH", "LANG", "LC_ALL", "TERM"},
	}
}

func StrictCodingPolicy() SandboxPolicy {
	p := CodingPolicy()
	p.ResourceEnforcement = ResourceRequired
	return p
}

func (p SandboxPolicy) Validate() error {
	if p.ResourceEnforcement != "" && p.ResourceEnforcement != ResourceBestEffort && p.ResourceEnforcement != ResourceRequired {
		return fmt.Errorf("%w: unknown resource enforcement %q", ErrInvalidPolicy, p.ResourceEnforcement)
	}
	if p.WorkspaceMode != WorkspaceReadOnly && p.WorkspaceMode != WorkspaceReadWrite {
		return fmt.Errorf("%w: unknown workspace mode %q", ErrInvalidPolicy, p.WorkspaceMode)
	}
	if p.MemoryMB <= 0 || p.CPUs <= 0 || math.IsNaN(p.CPUs) || math.IsInf(p.CPUs, 0) || p.Pids <= 0 || p.Timeout <= 0 {
		return fmt.Errorf("%w: memory, CPUs, PIDs and timeout must be finite and positive", ErrInvalidPolicy)
	}
	for _, domain := range p.AllowedDomains {
		if !validAllowedDomain(domain) {
			return fmt.Errorf("%w: invalid allowed domain %q", ErrInvalidPolicy, domain)
		}
	}
	for _, key := range p.AllowedEnv {
		if !validEnvName(key) || key == "HOME" || key == "TMPDIR" || key == "CLAW_HOME" || key == "CLAUDE_CODE_TMPDIR" || key == "CLAUDE_TMPDIR" {
			return fmt.Errorf("%w: invalid or reserved allowed environment name %q", ErrInvalidPolicy, key)
		}
	}
	return nil
}

// Deliberately narrower than URLs: names / *.names / IP literals, optionally
// with a port. No broad '*' wildcard, credentials, scheme, path or whitespace.
func validAllowedDomain(entry string) bool {
	host := entry
	if strings.Contains(entry, ":") {
		if strings.HasPrefix(entry, "[") && strings.HasSuffix(entry, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(entry, "["), "]")
		} else {
			var port string
			var err error
			host, port, err = net.SplitHostPort(entry)
			if err != nil {
				return false
			}
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
				return false
			}
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	wildcard := strings.HasPrefix(host, "*.")
	host = strings.TrimPrefix(host, "*.")
	if host == "localhost" && !wildcard {
		return true
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 || len(host) > 253 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
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

func (p SandboxPolicy) clone() SandboxPolicy {
	p.AllowedEnv = append([]string(nil), p.AllowedEnv...)
	p.AllowedDomains = append([]string(nil), p.AllowedDomains...)
	return p
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
