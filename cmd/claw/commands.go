package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/app"
	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/openai"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
	"golang.org/x/term"
)

type commandIO struct {
	input      io.Reader
	output     io.Writer
	readSecret func() (string, error)
	// Tests inject the entire login transport; no real OAuth/browser in CLI tests.
	oauthLogin func(context.Context, auth.LoginOptions) (auth.OAuthCredential, error)
}

func loadRuntime() (*app.Runtime, error) {
	home, err := config.AgentHomeDir()
	if err != nil {
		return nil, err
	}
	paths, err := config.NewPaths(home)
	if err != nil {
		return nil, err
	}
	return app.Load(paths, os.Getenv)
}
func dispatchCommand(ctx context.Context, args []string, cio commandIO) (bool, error) {
	if len(args) > 0 && (args[0] == "trust" || args[0] == "prompt") {
		return true, promptResourceCommand(ctx, args, cio)
	}
	if len(args) == 0 || args[0] != "auth" && args[0] != "models" && args[0] != "model" && args[0] != "sessions" && args[0] != "session" {
		return false, nil
	}
	r, err := loadRuntime()
	if err != nil {
		return true, err
	}
	return true, runCommand(ctx, args, r, cio)
}
func runCommand(ctx context.Context, args []string, r *app.Runtime, cio commandIO) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch args[0] {
	case "sessions", "session":
		store, err := sessionstore.NewFileStore(r.Paths.Sessions)
		if err != nil {
			return err
		}
		if args[0] == "sessions" && len(args) == 1 {
			list, err := store.List(ctx)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cio.output, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION ID\tMODEL\tWORKDIR\tMESSAGES\tUPDATED")
			for _, m := range list {
				fmt.Fprintf(w, "%s\t%s/%s\t%s\t%d\t%s\n", m.ID, m.ProviderID, m.ModelID, m.WorkDir, m.Messages, m.UpdatedAt.Format(time.RFC3339))
			}
			return w.Flush()
		}
		if len(args) == 3 && args[0] == "session" {
			if args[1] == "delete" {
				return store.Delete(ctx, args[2])
			}
			if args[1] == "show" {
				s, err := store.Load(ctx, args[2])
				if err != nil {
					return err
				}
				fmt.Fprintf(cio.output, "Session: %s\nModel: %s/%s\nWorkDir: %s\nMessages: %d\nUpdated: %s\nUsage: %+v\n", s.Metadata.ID, s.Metadata.ProviderID, s.Metadata.ModelID, s.Metadata.WorkDir, len(s.History), s.Metadata.UpdatedAt.Format(time.RFC3339), s.Usage)
				return nil
			}
		}
		return errors.New("usage: claw sessions | claw session show <id> | claw session delete <id>")
	case "auth":
		if len(args) < 2 {
			return errors.New("usage: claw auth login [provider] | claw auth logout <provider>")
		}
		if args[1] == "login" {
			return loginCommand(ctx, args[2:], r, cio)
		}
		if args[1] == "logout" && len(args) == 3 {
			if err := r.Auth.Logout(ctx, args[2]); err != nil {
				return err
			}
			if args[2] == "openai" {
				if err := r.ClearOpenAICatalog(ctx); err != nil {
					fmt.Fprintln(cio.output, "OAuth model cache cleanup failed; local credentials are still cleared.")
				}
				fmt.Fprintln(cio.output, "Local OpenAI credentials cleared. Remote revocation was not attempted; Host ID retained.")
			}
			fmt.Fprintln(cio.output, "AuthStore 凭据已清除；环境变量仍可能提供认证。")
			return nil
		}
		return errors.New("usage: claw auth login [provider] | claw auth logout <provider>")
	case "models":
		if len(args) != 1 {
			return errors.New("usage: claw models")
		}
		if err := r.RefreshOpenAICatalog(ctx); err != nil {
			// An OpenAI entitlement/outage must not hide ZAI/custom catalogs.
			// Refresh already removed stale OpenAI entries; no fallback is used.
			fmt.Fprintf(cio.output, "OpenAI OAuth models unavailable: %v\n", err)
		}
		w := tabwriter.NewWriter(cio.output, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PROVIDER\tMODEL\tNAME\tAUTH\tTOOLS\tBILLING\tAVAILABLE")
		for _, model := range r.Models.List() {
			info, authErr := r.Auth.Inspect(ctx, model.ProviderID)
			method := "missing"
			available := "no"
			if authErr == nil {
				method = string(info.Type)
				if model.ToolCalling {
					available = "yes"
				}
			}
			if effective, _, err := r.Manager.Inspect(ctx, model.Ref()); err == nil {
				model = effective
			}
			if info.NeedsRefresh {
				available += " (refresh required)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", model.ProviderID, model.ID, model.Name, method, yesNo(model.ToolCalling), model.BillingMode, available)
		}
		return w.Flush()
	case "model":
		if len(args) == 2 && args[1] == "current" {
			ref, _, err := config.ResolveSelectedModel("", r.Config)
			if err != nil {
				return err
			}
			model, ok := r.Models.Get(ref)
			if !ok {
				return fmt.Errorf("未知模型: %s。运行 claw models", ref)
			}
			d, _ := r.Providers.Get(ref.ProviderID)
			effective, info, err := r.Manager.Inspect(ctx, ref)
			if err == nil {
				model = effective
			}
			fmt.Fprintf(cio.output, "Current default model: %s\nProvider: %s\nAuth: %s\nBilling: %s\nTool Calling: %s\nConfig: %s\n", ref, d.Name, info.Type, model.BillingMode, yesNo(model.ToolCalling), r.Paths.Config)
			if err != nil {
				fmt.Fprintf(cio.output, "Unavailable: %v\n", err)
			} else {
				fmt.Fprintf(cio.output, "Credential: available (%s)\n", info.Source)
			}
			return nil
		}
		if len(args) == 3 && args[1] == "set" {
			ref, err := provider.ParseModelRef(args[2])
			if err != nil {
				return err
			}
			if _, _, err := r.Manager.Inspect(ctx, ref); err != nil {
				return err
			}
			if err := config.Save(r.Paths.Config, func() config.Config { c := r.Config; c.DefaultModel = ref.String(); return c }()); err != nil {
				return err
			}
			fmt.Fprintf(cio.output, "Default model: %s\nSaved: %s\n", ref, r.Paths.Config)
			return nil
		}
		return errors.New("usage: claw model current | claw model set <provider/model>")
	}
	return errors.New("unknown command")
}
func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
func loginCommand(ctx context.Context, args []string, r *app.Runtime, cio commandIO) error {
	if len(args) == 0 {
		fmt.Fprintln(cio.output, "Available Providers (run claw auth login <provider>):")
		for _, p := range r.Providers.List() {
			fmt.Fprintf(cio.output, "  %s — %s\n", p.ID, p.Name)
		}
		return nil
	}
	id := args[0]
	definition, ok := r.Providers.Get(id)
	if !ok {
		return fmt.Errorf("unknown provider %s", id)
	}
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fs.SetOutput(cio.output)
	method := fs.String("method", "", "oauth (Sign in with ChatGPT) or api-key")
	fromEnv := fs.Bool("from-env", false, "save the provider API key from environment")
	manual := fs.Bool("manual", false, "paste the full final redirect URL (requires fixed loopback port)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected login arguments")
	}
	if *method == "" {
		if id == "openai" && !*fromEnv {
			*method = "oauth"
		} else {
			*method = "api-key"
		}
	}
	if *method == "oauth" {
		if id != "openai" || *fromEnv {
			return errors.New("OAuth login is supported only for OpenAI, without --from-env")
		}
		login := cio.oauthLogin
		if login == nil {
			client, err := r.OpenAILoginClient(ctx)
			if err != nil {
				return err
			}
			login = client.Login
		}
		fmt.Fprint(cio.output, "OpenAI Authentication\n\n")
		if !*manual {
			fmt.Fprintln(cio.output, "Opening browser...")
		}
		fmt.Fprintln(cio.output, "Continue with ChatGPT")
		options := auth.LoginOptions{Manual: *manual, OpenBrowser: func(value string) error { return openBrowser(ctx, value) }, PresentURL: func(value string) {
			fmt.Fprintf(cio.output, "Open this private authorization URL (do not share it):\n%s\n", value)
		}, ReadRedirect: func(ctx context.Context) (string, error) {
			fmt.Fprintln(cio.output, "Paste the full final redirect URL (including state and issued client_id):")
			return readLine(ctx, cio.input)
		}}
		credential, err := login(ctx, options)
		if err != nil {
			return err
		}
		if err := r.Auth.Login(ctx, id, credential); err != nil {
			return err
		}
		// A newly granted authorization must not inherit an earlier grant's
		// catalog if discovery subsequently fails, even for the same subject.
		if err := r.ClearOpenAICatalog(ctx); err != nil {
			return fmt.Errorf("OpenAI credentials saved, but OAuth catalog reset failed: %w", err)
		}
		fmt.Fprintln(cio.output, "✓ OpenAI account connected")
		if credential.HasScope(openai.PlanScope) {
			fmt.Fprintln(cio.output, "✓ ChatGPT plan usage enabled")
		}
		fmt.Fprintln(cio.output, "✓ Credentials saved")
		if !credential.HasScope(openai.PlanScope) {
			if err := r.LoadOpenAICatalog(ctx); err != nil {
				return err
			}
			fmt.Fprintln(cio.output, "ChatGPT Plan Usage: disabled\n"+openai.ErrPlanUsageDisabled.Error())
			return nil
		}
		if err := r.RefreshOpenAICatalog(ctx); err != nil {
			return fmt.Errorf("OpenAI credentials saved, but model discovery failed: %w; retry claw models", err)
		}
		fmt.Fprintln(cio.output, "\nAvailable models:")
		count := 0
		for _, model := range r.Models.List() {
			if model.ProviderID == "openai" {
				fmt.Fprintf(cio.output, "  %s — %s\n", model.Ref(), model.Name)
				count++
			}
		}
		if count == 0 {
			fmt.Fprintln(cio.output, "  No visible models were granted to this account.")
		}
		return nil
	} else if *method == "api-key" || *method == "api_key" {
		if *manual {
			return errors.New("--manual is only for OAuth")
		}
		allowed := false
		for _, m := range definition.AuthMethods {
			if m == auth.CredentialAPIKey {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("provider %s does not require API key authentication", id)
		}
		if *fromEnv {
			if err := r.Auth.LoginFromEnv(ctx, id); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(cio.output, "API Key for %s (hidden input): ", id)
			reader := cio.readSecret
			if reader == nil {
				reader = readAPIKey
			}
			key, err := reader()
			fmt.Fprintln(cio.output)
			if err != nil {
				return err
			}
			if err := r.Auth.Login(ctx, id, auth.APIKeyCredential{Key: strings.TrimSpace(key)}); err != nil {
				return err
			}
		}
	} else {
		return errors.New("--method must be oauth or api-key")
	}
	if id == "openai" {
		if err := r.ClearOpenAICatalog(ctx); err != nil {
			return err
		}
	}
	fmt.Fprintf(cio.output, "Authenticated: %s\nCredential store: %s (0600)\n", id, r.Paths.Auth)
	return nil
}
func readAPIKey() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("API key input requires a terminal; set the provider env variable and use --from-env")
	}
	data, err := term.ReadPassword(int(os.Stdin.Fd()))
	return string(data), err
}
func readLine(ctx context.Context, input io.Reader) (string, error) {
	if input == nil {
		return "", errors.New("missing OAuth input")
	}
	type result struct {
		value string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(input).ReadString('\n')
		if err == io.EOF && line != "" {
			err = nil
		}
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.value, r.err
	}
}
func openBrowser(ctx context.Context, value string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", value)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", value)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", value)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}
