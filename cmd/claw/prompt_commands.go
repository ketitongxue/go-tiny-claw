package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/ketitongxue/go-tiny-claw/internal/config"
	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/executor"
	"github.com/ketitongxue/go-tiny-claw/internal/project"
	"github.com/ketitongxue/go-tiny-claw/internal/tools"
)

// Offline commands intentionally do not load Provider/Auth/Model state, contact
// Docker, or execute tools. They work even without credentials or a model.
func promptResourceCommand(ctx context.Context, args []string, cio commandIO) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	home, err := config.AgentHomeDir()
	if err != nil {
		return err
	}
	paths, err := config.NewPaths(home)
	if err != nil {
		return err
	}
	if args[0] == "trust" {
		return trustCommand(ctx, args[1:], paths, cio)
	}
	return promptCommand(ctx, args[1:], paths, cio)
}

func trustCommand(ctx context.Context, args []string, paths config.Paths, cio commandIO) error {
	action := "grant"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	if action != "grant" && action != "status" && action != "revoke" {
		return errors.New("usage: claw trust [status|revoke] [-workdir <path>]")
	}
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	fs.SetOutput(cio.output)
	workDir := fs.String("workdir", ".", "workspace (default: current directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*workDir) == "" {
		return errors.New("usage: claw trust [status|revoke] [-workdir <path>]")
	}
	if err := paths.ValidateWorkspace(*workDir); err != nil {
		return err
	}
	id, err := project.Discover(*workDir)
	if err != nil {
		return err
	}
	if err := paths.ValidateWorkspace(id.Root); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store := project.NewTrustStore(paths.Home)
	switch action {
	case "grant":
		if err := store.Trust(id.Root); err != nil {
			return err
		}
	case "revoke":
		if err := store.Revoke(id.Root); err != nil {
			return err
		}
	}
	trusted, err := store.IsTrusted(id.Root)
	if err != nil {
		return err
	}
	fmt.Fprintf(cio.output, "Project: %s\nTrusted: %t\nRegistry: %s\n", id.Root, trusted, store.Path)
	if action == "grant" {
		fmt.Fprintln(cio.output, "Project Trust permits prompt resources; it does not change Sandbox or Human Approval.")
	}
	return nil
}

func promptCommand(ctx context.Context, args []string, paths config.Paths, cio commandIO) error {
	if len(args) == 0 || (args[0] != "show" && args[0] != "render") {
		return errors.New("usage: claw prompt show|render [-workdir <path>] [-agent main|explorer] [-sandbox auto|docker|local] [-plan]")
	}
	action := args[0]
	fs := flag.NewFlagSet("prompt "+action, flag.ContinueOnError)
	fs.SetOutput(cio.output)
	workDir := fs.String("workdir", ".", "workspace (default: current directory)")
	agent := fs.String("agent", "main", "main or explorer")
	sandbox := fs.String("sandbox", executor.ModeAuto, "tool selection only; does not connect to Docker")
	plan := fs.Bool("plan", false, "include plan workflow for Main")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*workDir) == "" || (*agent != "main" && *agent != "explorer") {
		return errors.New("invalid prompt arguments")
	}
	if err := executor.ValidateMode(*sandbox); err != nil {
		return err
	}
	if err := paths.ValidateWorkspace(*workDir); err != nil {
		return err
	}
	c, err := config.Load(paths.Config)
	if err != nil {
		return err
	}
	var registry tools.Registry
	if *agent == "explorer" {
		var exec executor.Executor
		if *sandbox != executor.ModeLocal {
			// Constructor is offline; Execute/daemon inspection are never called.
			exec, err = executor.NewDockerExecutor(executor.DefaultImage, executor.ExplorerPolicy())
			if err != nil {
				return err
			}
		}
		registry = newExplorerToolRegistry(*workDir, exec)
	} else {
		registry = newMainToolRegistry(*workDir, nil)
		registry.Register(tools.NewSubagentTool(nil, nil, nil))
	}
	service := ctxpkg.PromptService{Resources: ctxpkg.ResourceLoader{Home: paths.Home}, Language: c.Language, OnDiagnostic: func(d ctxpkg.Diagnostic) {
		fmt.Fprintf(cio.output, "%s: %s: %s\n", d.Level, d.Path, d.Message)
	}}
	state, err := service.Build(*workDir, tools.PromptMetadata(registry), *plan, *agent == "explorer")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if action == "render" {
		fmt.Fprintln(cio.output, "WARNING: may contain project instructions and sensitive information; local debug only.")
		_, err = fmt.Fprintln(cio.output, state.Content)
		return err
	}
	w := tabwriter.NewWriter(cio.output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SECTION\tSOURCE\tSIZE (bytes)\tEST. TOKENS")
	estimator := ctxpkg.HeuristicEstimator{}
	for _, section := range state.Sections {
		sources := state.Sources[section.Name]
		quoted := make([]string, len(sources))
		for i, source := range sources {
			quoted[i] = strconv.Quote(source)
		}
		source := strings.Join(quoted, ", ")
		if section.Name == "skills" {
			source = fmt.Sprintf("%d skills", state.SkillsCount)
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", section.Name, source, len(section.Content), estimator.EstimateText(section.Content))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(cio.output, "Total: %d bytes; estimated tokens: %v; skills: %d; context files: %d\n", len(state.Content), state.Metrics()["prompt.estimated_tokens"], state.SkillsCount, state.ContextFilesCount)
	return nil
}
