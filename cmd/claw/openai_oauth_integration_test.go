package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/app"
	"github.com/ketitongxue/go-tiny-claw/internal/config"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// Opt-in HUMAN test: opens a browser and waits for ChatGPT authorization. It uses
// an isolated Agent Home and never logs out/overwrites the user's real auth.json.
func TestOpenAIOAuthHumanIntegration(t *testing.T) {
	if os.Getenv("CLAW_OPENAI_OAUTH_INTEGRATION") != "1" {
		t.Skip("set CLAW_OPENAI_OAUTH_INTEGRATION=1 to explicitly enable browser authorization")
	}
	paths, err := config.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := app.Load(paths, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	cio := commandIO{input: os.Stdin, output: os.Stdout}
	if err := runCommand(ctx, []string{"auth", "login", "openai", "--method", "oauth"}, r, cio); err != nil {
		t.Fatal(err)
	}
	if err := runCommand(ctx, []string{"models"}, r, cio); err != nil {
		t.Fatal(err)
	}
	var ref string
	for _, model := range r.Manager.ListAvailable(ctx) {
		if model.ProviderID == "openai" {
			ref = model.Ref().String()
			break
		}
	}
	if ref == "" {
		t.Fatal("no tool-capable ChatGPT plan models were discovered")
	}
	if err := runCommand(ctx, []string{"model", "set", ref}, r, cio); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CLAW_OPENAI_OAUTH_INFERENCE") != "1" {
		t.Log("login/catalog/model selection passed; set CLAW_OPENAI_OAUTH_INFERENCE=1 to also spend plan quota on one Responses request")
		return
	}
	selected, _, err := config.ResolveSelectedModel(ref, r.Config)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := r.Manager.Create(ctx, selected)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := p.Generate(ctx, []schema.Message{{Role: schema.RoleUser, Content: "Reply with OK. Do not use tools."}}, nil)
	if err != nil || msg == nil || msg.Content == "" {
		t.Fatal("subscription inference failed", err)
	}
}
