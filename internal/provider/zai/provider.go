// Package zai owns the China Coding Plan endpoint and catalog.
package zai

import (
	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/compatible"
)

const ID = "zai-coding-cn"
const BaseURL = "https://open.bigmodel.cn/api/coding/paas/v4"

func Definition() provider.ProviderDefinition {
	return compatible.Definition(compatible.Config{ID: ID, Name: "ZAI Coding Plan China", BaseURL: BaseURL}, []provider.AuthMethod{auth.CredentialAPIKey}, []string{"ZAI_CODING_CN_API_KEY", "ZHIPU_API_KEY"}, Models())
}
