package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/agenthome"
)

// FilesystemPolicy is independent of the SRT JSON wire format. Reads are
// deny-by-default; only the workspace, private runtime and OS toolchain reopen.
type FilesystemPolicy struct {
	DenyRead, AllowRead, AllowWrite, DenyWrite []string
}

// Literal entries avoid interpreting a workspace name as a glob. Older SRTs
// which cannot parse this schema must refuse it, never run with defaults.
type srtPath struct {
	Path    string `json:"path"`
	Literal bool   `json:"literal"`
}

type srtFilesystem struct {
	DenyRead   []srtPath `json:"denyRead"`
	AllowRead  []srtPath `json:"allowRead"`
	AllowWrite []srtPath `json:"allowWrite"`
	DenyWrite  []srtPath `json:"denyWrite"`
}

type srtNetwork struct {
	AllowedDomains      []string `json:"allowedDomains"`
	DeniedDomains       []string `json:"deniedDomains"`
	AllowUnixSockets    []string `json:"allowUnixSockets"`
	AllowAllUnixSockets bool     `json:"allowAllUnixSockets"`
	AllowLocalBinding   bool     `json:"allowLocalBinding"`
}

type srtGit struct {
	SafeDirectories []string `json:"safeDirectories"`
}

type srtSettings struct {
	Git        srtGit        `json:"git"`
	Filesystem srtFilesystem `json:"filesystem"`
	Network    srtNetwork    `json:"network"`
	// These must never be enabled to accommodate a broken installation.
	EnableWeakerNestedSandbox    bool `json:"enableWeakerNestedSandbox"`
	EnableWeakerNetworkIsolation bool `json:"enableWeakerNetworkIsolation"`
	AllowNestedUserNamespaces    bool `json:"allowNestedUserNamespaces"`
	AllowAppleEvents             bool `json:"allowAppleEvents"`
}

func literalPaths(paths []string) []srtPath {
	out := make([]srtPath, 0, len(paths))
	for _, path := range paths {
		out = append(out, srtPath{Path: path, Literal: true})
	}
	return out
}

func protectedHostPaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("%w: cannot determine absolute host HOME", ErrInvalidWorkDir)
	}
	claw, err := agenthome.Dir()
	if err != nil {
		return nil, err
	}
	paths := []string{claw}
	for _, name := range []string{".claw", ".ssh", ".aws", ".kube", ".config"} {
		paths = append(paths, filepath.Join(home, name))
	}
	paths = append(paths, "/var/run/docker.sock", "/run/docker.sock")
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		paths = append(paths, socket)
	}
	for i, path := range paths {
		paths[i], err = canonicalHostPath(path)
		if err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func buildFilesystemPolicy(workDir, runtimeDir string, policy SandboxPolicy) (FilesystemPolicy, error) {
	protected, err := protectedHostPaths()
	if err != nil {
		return FilesystemPolicy{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return FilesystemPolicy{}, err
	}
	home, err = canonicalHostPath(home)
	if err != nil {
		return FilesystemPolicy{}, err
	}
	fs := FilesystemPolicy{
		DenyRead: []string{"/", home}, AllowRead: []string{workDir, runtimeDir},
		AllowWrite: []string{runtimeDir},
		// SRT has shared temporary write conveniences; explicitly close them.
		DenyWrite: []string{filepath.Join(runtimeDir, "settings.json")},
	}
	for _, path := range []string{"/tmp/claude", "/private/tmp/claude", "/dev/tty", "/dev/dtracehelper", "/dev/autofs_nowait"} {
		resolved, err := canonicalHostPath(path)
		if err != nil {
			return FilesystemPolicy{}, err
		}
		fs.DenyWrite = append(fs.DenyWrite, resolved)
	}
	if policy.WorkspaceMode == WorkspaceReadWrite {
		fs.AllowWrite = append(fs.AllowWrite, workDir)
	} else {
		fs.DenyWrite = append(fs.DenyWrite, workDir)
	}
	// Do not expose /etc, /var, /tmp, /opt or /Library wholesale. They may
	// contain local credentials, sockets or sibling projects. Home-installed
	// toolchains intentionally remain unavailable; use a system installation.
	system := []string{"/usr/bin", "/usr/sbin", "/usr/lib", "/usr/libexec", "/usr/include", "/bin", "/sbin", "/lib", "/lib64", "/usr/local/go", "/usr/share/locale", "/usr/share/zoneinfo", "/usr/share/git-core", "/etc/hosts", "/etc/nsswitch.conf", "/etc/resolv.conf", "/etc/services", "/etc/localtime", "/etc/ld.so.cache", "/etc/ssl/certs", "/etc/ca-certificates", "/dev/null", "/dev/zero", "/dev/random", "/dev/urandom"}
	if runtime.GOOS == "darwin" {
		// Apple's curl initializes LibreSSL even for HTTP. Its reviewed public
		// config is necessary; never open all of /etc/ssl (private keys).
		system = append(system, "/System/Library", "/Library/Apple", "/Library/Developer/CommandLineTools", "/Library/Developer/DeveloperDiskImages", "/Applications/Xcode.app/Contents/Developer", "/private/etc/ssl/cert.pem", "/private/etc/ssl/openssl.cnf", "/private/var/db/dyld")
	}
	for _, path := range system {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return FilesystemPolicy{}, err
		}
		path, err = canonicalHostPath(path)
		if err != nil {
			return FilesystemPolicy{}, err
		}
		if pathContains(workDir, path) || pathContains(path, workDir) {
			return FilesystemPolicy{}, fmt.Errorf("%w: native workspace overlaps system toolchain", ErrInvalidWorkDir)
		}
		for _, secret := range protected {
			if pathContains(path, secret) || pathContains(secret, path) {
				return FilesystemPolicy{}, fmt.Errorf("%w: system path overlaps protected path", ErrInvalidPolicy)
			}
		}
		fs.AllowRead = append(fs.AllowRead, path)
	}
	fs.DenyRead = append(fs.DenyRead, protected...)
	fs.DenyWrite = append(fs.DenyWrite, protected...)
	return fs, nil
}

func buildSRTSettings(workDir, runtimeDir string, policy SandboxPolicy) (srtSettings, error) {
	if err := policy.Validate(); err != nil {
		return srtSettings{}, err
	}
	if err := requireCapabilities(ModeNative, NativeCapabilities(), policy); err != nil {
		return srtSettings{}, err
	}
	fs, err := buildFilesystemPolicy(workDir, runtimeDir, policy)
	if err != nil {
		return srtSettings{}, err
	}
	domains := []string{}
	if policy.NetworkEnabled {
		domains = append(domains, policy.AllowedDomains...)
	}
	return srtSettings{
		// SRT's Linux user namespace can change Git's observed owner UID.
		// Trust this canonical workspace only, never safe.directory='*'.
		Git:        srtGit{SafeDirectories: []string{workDir}},
		Filesystem: srtFilesystem{literalPaths(fs.DenyRead), literalPaths(fs.AllowRead), literalPaths(fs.AllowWrite), literalPaths(fs.DenyWrite)},
		Network:    srtNetwork{AllowedDomains: domains, DeniedDomains: []string{}, AllowUnixSockets: []string{}},
	}, nil
}

// shellQuote is used only for values inside the sandbox's command string.
// The host always invokes SRT with an argv slice, never through a shell.
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
