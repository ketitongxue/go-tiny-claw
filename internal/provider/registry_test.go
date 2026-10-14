package provider

import (
	"context"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
)

func testDefinition(id string) ProviderDefinition {
	return ProviderDefinition{ID: id, Name: id, AuthMethods: []AuthMethod{auth.CredentialAPIKey}, Models: []ModelInfo{{ProviderID: id, ID: "model", ToolCalling: true}}, Factory: func(context.Context, ModelInfo, auth.Credential, ProviderConfig) (LLMProvider, error) {
		return nil, nil
	}}
}
func TestRegistry(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"zai-coding-cn", "openai"} {
		if err := r.Register(testDefinition(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Register(testDefinition("openai")); err == nil {
		t.Fatal("overrode built-in")
	}
	if err := r.Register(testDefinition("../invalid")); err == nil {
		t.Fatal("invalid ID")
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("missing found")
	}
	p, ok := r.Get("openai")
	if !ok || p.ID != "openai" || len(r.List()) != 2 || r.List()[0].ID != "openai" {
		t.Fatal("registry mismatch")
	}
	p.Models[0].ID = "mutated"
	p, _ = r.Get("openai")
	if p.Models[0].ID != "model" {
		t.Fatal("mutable registry")
	}
	p.ID = "other"
	p.Models = append(p.Models, p.Models[0])
	for i := range p.Models {
		p.Models[i].ProviderID = p.ID
	}
	if err := r.Register(p); err == nil {
		t.Fatal("duplicate model allowed")
	}
}
func TestModelRegistry(t *testing.T) {
	r := NewModelRegistry()
	m := ModelInfo{ProviderID: "openai", ID: "a", ToolCalling: true}
	if err := r.Register(m); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(m); err == nil {
		t.Fatal("duplicate model")
	}
	if got, ok := r.Get(m.Ref()); !ok || got.BillingMode != BillingUnknown {
		t.Fatal(got, ok)
	}
	if len(r.List()) != 1 {
		t.Fatal("list")
	}
}
