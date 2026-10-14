package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/observability"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

func (e *AgentEngine) buildContext(system schema.Message, history []schema.Message, tools []schema.ToolDefinition, span *observability.Span, spec ctxpkg.ModelContextSpec) (ctxpkg.BuildResult, error) {
	// Tool schemas also occupy input space, though they are not messages.
	data, err := json.Marshal(tools)
	if err != nil {
		return ctxpkg.BuildResult{}, err
	}
	overhead := e.ContextBuilder.Estimator.EstimateMessage(schema.Message{Content: string(data)})
	window, input := e.ContextBuilder.Policy.Budget(spec)
	adjusted := spec
	adjusted.ContextWindow = window
	adjusted.MaxOutput = window - (window*e.ContextBuilder.Policy.Resolved().SafetyMarginPercent+99)/100 - input + overhead
	result, err := e.ContextBuilder.Build(system, history, adjusted)
	if err != nil {
		log.Printf("[Context] 无法构建模型输入: %v", err)
		return result, err
	}
	result.EstimatedTokens += overhead
	result.BudgetTokens = input
	if span != nil {
		for k, v := range map[string]int{"context.window_tokens": window, "context.input_budget_tokens": result.BudgetTokens, "context.estimated_tokens": result.EstimatedTokens, "context.total_messages": result.TotalMessages, "context.included_messages": result.IncludedMessages, "context.omitted_messages": result.OmittedMessages, "context.truncated_tool_results": result.TruncatedToolResults} {
			span.AddAttribute(k, v)
		}
	}
	return result, nil
}
func contextLimit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context_length_exceeded") || strings.Contains(s, "context window exceeded")
}
func (e *AgentEngine) generate(ctx context.Context, system schema.Message, history, view []schema.Message, tools []schema.ToolDefinition, span *observability.Span) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := e.provider.Generate(ctx, view, tools)
	if contextLimit(err) {
		log.Printf("[Context] Provider context limit; retrying once with conservative budget")
		spec := e.ContextSpec
		window, _ := e.ContextBuilder.Policy.Budget(spec)
		spec.ContextWindow = window * 3 / 4
		built, buildErr := e.buildContext(system, history, tools, span, spec)
		if buildErr != nil {
			return nil, buildErr
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err = e.provider.Generate(ctx, built.Messages, tools)
	}
	if err == nil && resp == nil {
		return nil, fmt.Errorf("provider returned no response")
	}
	return resp, err
}
