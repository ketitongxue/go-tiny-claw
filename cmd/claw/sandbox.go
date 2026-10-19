package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/executor"
)

// Application wiring only. These settings never enter Bash tool arguments.
func applicationSandboxPolicy(opts options, explorer bool) (executor.SandboxPolicy, error) {
	p := executor.CodingPolicy()
	if explorer {
		p = executor.ExplorerPolicy()
	}
	switch opts.sandboxPolicy {
	case "", "coding":
	case "strict":
		p.ResourceEnforcement = executor.ResourceRequired
	default:
		return p, errors.New("sandbox-policy must be coding or strict")
	}
	switch opts.sandboxNetwork {
	case "", "denied":
		if opts.sandboxDomains != "" {
			return p, errors.New("sandbox-allowed-domains requires sandbox-network=allowlist")
		}
	case "allowlist":
		p.NetworkEnabled = true
		if strings.TrimSpace(opts.sandboxDomains) == "" {
			return p, errors.New("network allowlist requires nonempty sandbox-allowed-domains")
		}
		for _, domain := range strings.Split(opts.sandboxDomains, ",") {
			p.AllowedDomains = append(p.AllowedDomains, strings.TrimSpace(domain))
		}
	case "unrestricted":
		p.NetworkEnabled = true
		if opts.sandboxDomains != "" {
			return p, errors.New("unrestricted network must not specify allowed domains")
		}
	default:
		return p, errors.New("sandbox-network must be denied, allowlist or unrestricted")
	}
	return p, p.Validate()
}

// A credential-free, model-free diagnostic command makes security acceptance
// reproducible without relying on an LLM to choose the requested bash command.
func sandboxCommand(ctx context.Context, args []string, output io.Writer) error {
	fs := flag.NewFlagSet("claw sandbox", flag.ContinueOnError)
	fs.SetOutput(output)
	var opts options
	var command, policy string
	fs.StringVar(&opts.sandbox, "sandbox", executor.ModeAuto, "auto | native (SRT) | docker | local (UNSAFE)")
	fs.StringVar(&opts.sandboxImage, "sandbox-image", executor.DefaultImage, "trusted prebuilt Docker image")
	fs.StringVar(&opts.workDir, "workdir", ".", "existing workspace")
	fs.StringVar(&policy, "policy", "coding", "coding | explorer (RO) | strict (required CPU/memory/PID)")
	fs.StringVar(&opts.sandboxNetwork, "sandbox-network", "denied", "denied | allowlist | unrestricted")
	fs.StringVar(&opts.sandboxDomains, "sandbox-allowed-domains", "", "comma-separated application allowlist")
	fs.StringVar(&command, "command", "", "command to execute once; never retried on another backend")
	timeout := fs.Duration("timeout", executor.DefaultTimeout, "execution budget")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(command) == "" || *timeout <= 0 {
		return errors.New("usage: claw sandbox -sandbox auto|native|docker|local -workdir <dir> -policy coding|explorer|strict -command <bash> [-timeout 30s]")
	}
	if policy != "coding" && policy != "explorer" && policy != "strict" {
		return errors.New("policy must be coding, explorer or strict")
	}
	opts.sandboxPolicy = "coding"
	if policy == "strict" {
		opts.sandboxPolicy = "strict"
	}
	p, err := applicationSandboxPolicy(opts, policy == "explorer")
	if err != nil {
		return err
	}
	p.Timeout = *timeout
	e, err := executor.NewConfigured(ctx, opts.sandbox, opts.sandboxImage, opts.workDir, p)
	if err != nil {
		return err
	}
	if opts.sandbox == executor.ModeLocal {
		fmt.Fprint(output, localExecutorWarning)
	}
	if v, ok := e.(interface {
		ValidateWorkspace(context.Context, string) error
	}); ok {
		if err := v.ValidateWorkspace(ctx, opts.workDir); err != nil {
			return err
		}
	}
	fmt.Fprint(output, e.(interface{ Status() executor.Status }).Status().String())
	r, err := e.Execute(ctx, executor.Request{WorkDir: opts.workDir, Command: command, Timeout: *timeout}, func(p []byte) { _, _ = output.Write(p) })
	fmt.Fprintf(output, "\nexit: %d; duration: %s\n", r.ExitCode, r.Duration)
	if err != nil {
		return err
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("command exited with code %d", r.ExitCode)
	}
	return nil
}
