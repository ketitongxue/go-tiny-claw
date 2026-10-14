// Package compatible implements only the OpenAI-compatible Chat Completions API.
package compatible

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/auth"
	"github.com/ketitongxue/go-tiny-claw/internal/provider"
	"github.com/ketitongxue/go-tiny-claw/internal/provider/wire"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type Config struct {
	ID, Name, BaseURL string
	HTTPClient        *http.Client
}
type Provider struct {
	endpoint, model, key string
	client               *http.Client
}

func New(config Config, model provider.ModelInfo, credential auth.Credential) (*Provider, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid OpenAI-compatible base URL")
	}
	p := &Provider{endpoint: strings.TrimRight(config.BaseURL, "/") + "/chat/completions", model: model.ID, client: wire.Client(config.HTTPClient)}
	if err := auth.Validate(credential); err != nil {
		return nil, err
	}
	switch c := credential.(type) {
	case auth.APIKeyCredential:
		p.key = c.Key
	case auth.NoCredential:
	default:
		return nil, errors.New("OpenAI-compatible provider requires API key or no auth")
	}
	return p, nil
}
func Definition(config Config, methods []provider.AuthMethod, env []string, models []provider.ModelInfo) provider.ProviderDefinition {
	return provider.ProviderDefinition{ID: config.ID, Name: config.Name, AuthMethods: methods, EnvVars: env, Models: models, Config: provider.ProviderConfig{BaseURL: config.BaseURL}, Factory: func(_ context.Context, model provider.ModelInfo, c auth.Credential, p provider.ProviderConfig) (provider.LLMProvider, error) {
		instanceConfig := config
		instanceConfig.BaseURL = p.BaseURL
		return New(instanceConfig, model, c)
	}}
}

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}
type function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type toolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type response struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Usage *struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Details    struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (p *Provider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	messages := make([]message, 0, len(msgs))
	for _, m := range msgs {
		out := message{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		if m.ToolCallID != "" {
			out.Role = "tool"
		} else {
			switch m.Role {
			case schema.RoleSystem, schema.RoleUser, schema.RoleAssistant:
			default:
				return nil, errors.New("unsupported message role")
			}
		}
		for _, tc := range m.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, toolCall{ID: tc.ID, Type: "function", Function: function{Name: tc.Name, Arguments: string(tc.Arguments)}})
		}
		messages = append(messages, out)
	}
	body := map[string]any{"model": p.model, "messages": messages}
	if len(tools) > 0 {
		definitions := make([]any, 0, len(tools))
		for _, t := range tools {
			definitions = append(definitions, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
		}
		body["tools"] = definitions
	}
	headers := map[string]string{}
	if p.key != "" {
		headers["Authorization"] = "Bearer " + p.key
	}
	resp, err := wire.Post(ctx, p.client, p.endpoint, headers, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r response
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
	if err != nil || len(data) > 16<<20 || json.Unmarshal(data, &r) != nil || len(r.Choices) == 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("malformed Chat Completions response (empty/invalid choices)")
	}
	m := r.Choices[0].Message
	result := &schema.Message{Role: schema.RoleAssistant, Content: m.Content}
	for _, tc := range m.ToolCalls {
		if tc.Type != "function" || tc.ID == "" || tc.Function.Name == "" || !json.Valid([]byte(tc.Function.Arguments)) {
			return nil, errors.New("malformed tool call response")
		}
		result.ToolCalls = append(result.ToolCalls, schema.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments)})
	}
	if strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0 {
		return nil, errors.New("Chat Completions returned no text or tool calls")
	}
	if r.Usage != nil && (r.Usage.Prompt > 0 || r.Usage.Completion > 0) {
		result.Usage = &schema.Usage{PromptTokens: r.Usage.Prompt, CompletionTokens: r.Usage.Completion, CachedPromptTokens: r.Usage.Details.Cached}
	}
	return result, nil
}
