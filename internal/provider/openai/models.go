package openai

import "github.com/ketitongxue/go-tiny-claw/internal/provider"

// A deliberately small static catalog; listing is not an entitlement check.
func Models() []provider.ModelInfo {
	return []provider.ModelInfo{
		{ProviderID: "openai", ID: "gpt-5.2", Name: "GPT-5.2", ToolCalling: true, Reasoning: true, BillingMode: provider.BillingAPI},
		{ProviderID: "openai", ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", ToolCalling: true, Reasoning: true, BillingMode: provider.BillingAPI},
	}
}
