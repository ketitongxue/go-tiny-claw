package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/wire"
)

const ModelsEndpoint = OAuthResource + "/models"

// DiscoverModels is the ChatGPT OAuth catalog, not the API-key /v1/models shape.
// A visible slug is the model ID; display_name is presentation metadata only.
func DiscoverModels(ctx context.Context, c auth.OAuthCredential, endpoint string, client *http.Client) ([]provider.ModelInfo, error) {
	if err := CheckCredential(c); err != nil {
		return nil, err
	}
	if err := CanUse(c); err != nil {
		return nil, err
	}
	if endpoint == "" {
		endpoint = ModelsEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid OpenAI OAuth model endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := wire.Client(client).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("OpenAI OAuth model discovery transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if known := readSubscriptionError(resp.Body); known != nil {
			return nil, known
		}
		return nil, fmt.Errorf("OpenAI OAuth model discovery failed (HTTP %d); run claw models after checking authorization", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	var reply struct {
		Models *[]struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			ToolCalling *bool  `json:"tool_calling"`
			Reasoning   bool   `json:"reasoning"`
		} `json:"models"`
	}
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, &reply) != nil || reply.Models == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("invalid OpenAI OAuth model catalog")
	}
	registry := provider.NewModelRegistry()
	for _, m := range *reply.Models {
		if m.Visibility != "list" {
			continue
		}
		// The plan catalog lists invokable Responses models. Honor an explicit
		// tool capability if supplied; otherwise the Responses tool contract applies.
		toolCalling := m.ToolCalling == nil || *m.ToolCalling
		model := provider.ModelInfo{ProviderID: "openai", ID: m.Slug, Name: m.DisplayName, ToolCalling: toolCalling, Reasoning: m.Reasoning, BillingMode: provider.BillingSubscription}
		if registry.Register(model) != nil {
			return nil, errors.New("invalid/duplicate slug in OpenAI OAuth model catalog")
		}
	}
	return registry.List(), nil
}
