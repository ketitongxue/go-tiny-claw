package zai

import "github.com/ketitongxue/go-tiny-claw/internal/provider"

func Models() []provider.ModelInfo {
	var models []provider.ModelInfo
	for _, id := range []string{"glm-5.3", "glm-5.3-flash", "glm-5.3-flashx", "glm-5.2"} {
		// This endpoint uses Coding Plan entitlements, not the ordinary pay-per-token API.
		models = append(models, provider.ModelInfo{ProviderID: ID, ID: id, Name: id, ToolCalling: true, Reasoning: true, BillingMode: provider.BillingSubscription})
	}
	return models
}
