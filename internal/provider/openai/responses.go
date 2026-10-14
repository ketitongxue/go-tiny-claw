package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/provider/wire"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

type responseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type responseOutput struct {
	Type      string            `json:"type"`
	CallID    string            `json:"call_id"`
	Name      string            `json:"name"`
	Arguments string            `json:"arguments"`
	Content   []responseContent `json:"content"`
}
type response struct {
	Status string           `json:"status"`
	Error  json.RawMessage  `json:"error"`
	Output []responseOutput `json:"output"`
	Usage  *struct {
		Input   int `json:"input_tokens"`
		Output  int `json:"output_tokens"`
		Details struct {
			Cached int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

func responseInput(model string, msgs []schema.Message, tools []schema.ToolDefinition) (map[string]any, error) {
	input := make([]any, 0, len(msgs))
	var instructions []string
	for _, m := range msgs {
		if m.ToolCallID != "" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
			continue
		}
		switch m.Role {
		case schema.RoleSystem:
			instructions = append(instructions, m.Content)
		case schema.RoleUser:
			input = append(input, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": m.Content}}})
		case schema.RoleAssistant:
			if m.Content != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": m.Content}}})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": string(tc.Arguments)})
			}
		default:
			return nil, errors.New("unsupported Responses message role")
		}
	}
	body := map[string]any{"model": model, "input": input, "instructions": strings.Join(instructions, "\n\n")}
	if len(tools) > 0 {
		definitions := make([]any, 0, len(tools))
		for _, tool := range tools {
			definitions = append(definitions, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema, "strict": false})
		}
		body["tools"] = definitions
	}
	return body, nil
}
func apiKeyRequest(model string, msgs []schema.Message, tools []schema.ToolDefinition) (map[string]any, error) {
	body, err := responseInput(model, msgs, tools)
	if err == nil {
		body["store"], body["stream"] = false, false
	}
	return body, err
}
func oauthRequest(model string, msgs []schema.Message, tools []schema.ToolDefinition) (map[string]any, error) {
	// Explicit allowlist: no previous_response_id, system input, temperature,
	// max_output_tokens or other API-key-only options are inherited here.
	body, err := responseInput(model, msgs, tools)
	if err == nil {
		body["store"], body["stream"] = false, true
	}
	return body, err
}
func (p *Provider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	builder := apiKeyRequest
	if p.subscription {
		builder = oauthRequest
	}
	body, err := builder(p.model, msgs, tools)
	if err != nil {
		return nil, err
	}
	var resp *http.Response
	if p.subscription {
		resp, err = p.postOAuth(ctx, body)
	} else {
		resp, err = wire.Post(ctx, p.client, p.endpoint, map[string]string{"Authorization": "Bearer " + p.token}, body)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r response
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		r, err = readEvents(resp.Body)
	} else if p.subscription {
		// The public ChatGPT plan route can return real SSE with no Content-Type.
		// Inspect bounded framing, not just headers; JSON/HTML is never a success.
		reader := bufio.NewReader(resp.Body)
		var stream bool
		var shape string
		stream, shape, err = sniffEventStream(reader)
		if err != nil {
			err = errors.New("ChatGPT plan response read failed; partial output discarded")
		} else if stream {
			r, err = readEvents(reader)
		} else if known := readSubscriptionError(reader); known != nil {
			err = known
		} else {
			err = fmt.Errorf("ChatGPT plan Responses requires an SSE stream with response.completed (HTTP %d; content_type=%s; response_shape=%s)", resp.StatusCode, safeResponseMediaType(resp.Header.Get("Content-Type")), shape)
		}
	} else {
		var data []byte
		data, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
		if err != nil || len(data) > 16<<20 || json.Unmarshal(data, &r) != nil {
			err = errors.New("malformed Responses JSON")
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return mapResponse(r)
}
func (p *Provider) postOAuth(ctx context.Context, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("cannot encode ChatGPT plan request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid ChatGPT plan endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("ChatGPT plan request transport failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		if known := readSubscriptionError(resp.Body); known != nil {
			return nil, known
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ChatGPT plan request failed (HTTP %d); no API-key fallback was attempted", resp.StatusCode)
	}
	return resp, nil
}
func subscriptionError(code string) error {
	switch code {
	case "context_length_exceeded":
		return errors.New("model context_length_exceeded")
	case "subscription_sharing_usage_limit_exceeded":
		return errors.New("ChatGPT subscription usage limit exceeded (subscription_sharing_usage_limit_exceeded); wait for the plan quota to reset or explicitly configure an API key")
	case "subscription_sharing_usage_unavailable":
		return errors.New("ChatGPT subscription usage unavailable (subscription_sharing_usage_unavailable); check plan authorization/availability or explicitly configure an API key")
	}
	return nil
}
func errorCode(raw json.RawMessage) string {
	var e struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.Code != "" {
			return e.Code
		}
		return e.Type
	}
	return ""
}
func readSubscriptionError(reader io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(reader, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return nil
	}
	var reply struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &reply) != nil {
		return nil
	}
	// Only these fixed codes may escape the transport. Never print messages,
	// unknown codes or raw bodies, which may reflect credentials/context.
	return subscriptionError(errorCode(reply.Error))
}
func mapResponse(r response) (*schema.Message, error) {
	if known := subscriptionError(errorCode(r.Error)); known != nil {
		return nil, known
	}
	if r.Status != "" && r.Status != "completed" || len(r.Error) > 0 && string(r.Error) != "null" {
		return nil, errors.New("Responses request failed or incomplete")
	}
	result := &schema.Message{Role: schema.RoleAssistant}
	for _, output := range r.Output {
		switch output.Type {
		case "message":
			for _, content := range output.Content {
				if content.Type == "refusal" {
					return nil, errors.New("model refused the request")
				}
				if content.Type == "output_text" {
					result.Content += content.Text
				}
			}
		case "function_call":
			if output.CallID == "" || output.Name == "" || !json.Valid([]byte(output.Arguments)) {
				return nil, errors.New("malformed Responses tool call")
			}
			result.ToolCalls = append(result.ToolCalls, schema.ToolCall{ID: output.CallID, Name: output.Name, Arguments: json.RawMessage(output.Arguments)})
		}
	}
	if result.Content == "" && len(result.ToolCalls) == 0 {
		return nil, errors.New("Responses returned no text or tool calls")
	}
	if r.Usage != nil {
		result.Usage = &schema.Usage{PromptTokens: r.Usage.Input, CompletionTokens: r.Usage.Output, CachedPromptTokens: r.Usage.Details.Cached}
	}
	return result, nil
}

// sniffEventStream leaves the event fields unread. It only consumes a bounded
// whitespace/BOM preamble, and never exposes payloads or untrusted header values.
func sniffEventStream(reader *bufio.Reader) (bool, string, error) {
	for skipped := 0; skipped < 4096; skipped++ {
		first, err := reader.Peek(1)
		if errors.Is(err, io.EOF) {
			return false, "empty", nil
		}
		if err != nil {
			return false, "unreadable", err
		}
		firstByte := first[0]
		switch firstByte {
		case ' ', '\t', '\r', '\n':
			_, _ = reader.Discard(1)
			continue
		case 0xef:
			prefix, _ := reader.Peek(3)
			if bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
				_, _ = reader.Discard(3)
				continue
			}
		case ':':
			return true, "sse", nil
		case '{', '[':
			return false, "json", nil
		case '<':
			return false, "html_or_xml", nil
		}
		for _, field := range []string{"data:", "event:", "id:", "retry:"} {
			if firstByte != field[0] {
				continue
			}
			prefix, err := reader.Peek(len(field))
			if bytes.Equal(prefix, []byte(field)) {
				return true, "sse", nil
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return false, "unreadable", err
			}
		}
		return false, "other", nil
	}
	return false, "preamble_too_long", nil
}
func safeResponseMediaType(value string) string {
	if strings.TrimSpace(value) == "" {
		return "missing"
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err == nil {
		switch mediaType {
		case "text/event-stream", "application/json", "text/html", "text/plain", "application/octet-stream":
			return mediaType
		}
	}
	return "unrecognized" // never echo a header that could reflect credentials
}

// SSE is consumed inside the provider; AgentEngine still receives a single
// Message. EOF/[DONE]/partial deltas are never successful completion.
func readEvents(reader io.Reader) (response, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 32<<20+1))
	scanner.Buffer(make([]byte, 4096), 16<<20)
	items := make(map[int]*responseOutput)
	finalized := make(map[int]responseOutput)
	var data []string
	var eventName string
	item := func(index int) *responseOutput {
		if items[index] == nil {
			items[index] = &responseOutput{}
		}
		return items[index]
	}
	decode := func() (response, bool, error) {
		if len(data) == 0 {
			eventName = ""
			return response{}, false, nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		name := eventName
		eventName = ""
		if payload == "[DONE]" {
			return response{}, false, nil
		}
		var event struct {
			Type         string          `json:"type"`
			Response     response        `json:"response"`
			Error        json.RawMessage `json:"error"`
			Code         string          `json:"code"`
			OutputIndex  int             `json:"output_index"`
			ContentIndex int             `json:"content_index"`
			Item         responseOutput  `json:"item"`
			Part         responseContent `json:"part"`
			Delta        string          `json:"delta"`
			Text         string          `json:"text"`
			Arguments    string          `json:"arguments"`
		}
		if json.Unmarshal([]byte(payload), &event) != nil {
			return response{}, false, errors.New("malformed Responses event")
		}
		if event.Type == "" {
			event.Type = name
		}
		for _, code := range []string{event.Code, errorCode(event.Error), errorCode(event.Response.Error)} {
			if known := subscriptionError(code); known != nil {
				return response{}, false, known
			}
		}
		switch event.Type {
		case "response.completed":
			r := event.Response
			if r.Status != "" && r.Status != "completed" || len(r.Error) > 0 && string(r.Error) != "null" {
				return response{}, false, errors.New("Responses completion marked failed/incomplete")
			}
			r.Status = "completed"
			// The direct plan route sends output:[] in its completion summary;
			// actual final outputs arrive in output_item.done. A nonempty terminal
			// snapshot remains authoritative and must not duplicate those items.
			if len(r.Output) == 0 {
				indices := make([]int, 0, len(items))
				for i := range items {
					if _, done := finalized[i]; r.Output == nil || done {
						indices = append(indices, i)
					}
				}
				sort.Ints(indices)
				for _, i := range indices {
					if output, done := finalized[i]; done {
						r.Output = append(r.Output, output)
					} else {
						r.Output = append(r.Output, *items[i])
					}
				}
			}
			return r, true, nil
		case "response.failed", "response.incomplete", "error":
			return response{}, false, errors.New("Responses stream failed or incomplete; partial output discarded")
		case "response.output_item.added", "response.output_item.done":
			if event.OutputIndex < 0 {
				return response{}, false, errors.New("invalid Responses output index")
			}
			*item(event.OutputIndex) = event.Item
			if event.Type == "response.output_item.done" {
				output := event.Item
				output.Content = append([]responseContent(nil), output.Content...)
				finalized[event.OutputIndex] = output
			} else {
				delete(finalized, event.OutputIndex)
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			if event.OutputIndex < 0 {
				return response{}, false, errors.New("invalid Responses output index")
			}
			o := item(event.OutputIndex)
			o.Type = "function_call"
			if event.Type == "response.function_call_arguments.delta" {
				o.Arguments += event.Delta
			} else {
				o.Arguments = event.Arguments
			}
		case "response.output_text.delta", "response.output_text.done", "response.content_part.added", "response.content_part.done":
			if event.OutputIndex < 0 || event.ContentIndex < 0 || event.ContentIndex > 4096 {
				return response{}, false, errors.New("invalid Responses content index")
			}
			o := item(event.OutputIndex)
			o.Type = "message"
			for len(o.Content) <= event.ContentIndex {
				o.Content = append(o.Content, responseContent{})
			}
			content := &o.Content[event.ContentIndex]
			switch event.Type {
			case "response.output_text.delta":
				content.Type = "output_text"
				content.Text += event.Delta
			case "response.output_text.done":
				content.Type = "output_text"
				content.Text = event.Text
			default:
				*content = event.Part
			}
		}
		return response{}, false, nil
	}
	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
		if line == "" {
			r, done, err := decode()
			if err != nil || done {
				return r, err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if scanner.Err() != nil {
		return response{}, errors.New("Responses stream read failed; partial output discarded")
	}
	if r, done, err := decode(); err != nil || done {
		return r, err
	}
	return response{}, errors.New("Responses stream ended without response.completed; partial output discarded")
}
