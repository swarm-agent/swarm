package codex

import (
	"context"
	"errors"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type Runner struct {
	client *Client
}

func NewRunner(client *Client) *Runner {
	return &Runner{client: client}
}

func (r *Runner) ID() string {
	return "codex"
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r.client == nil {
		return provideriface.Response{}, errors.New("codex runner client is not configured")
	}
	out, err := r.client.CreateResponse(ctx, toCodexRequest(req))
	if err != nil {
		return provideriface.Response{}, err
	}
	return fromCodexResponse(out), nil
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r.client == nil {
		return provideriface.Response{}, errors.New("codex runner client is not configured")
	}
	out, err := r.client.CreateResponseStreaming(ctx, toCodexRequest(req), func(event StreamEvent) {
		if onEvent == nil {
			return
		}
		switch event.Type {
		case StreamEventOutputTextDelta:
			onEvent(provideriface.StreamEvent{
				Type:  provideriface.StreamEventOutputTextDelta,
				Delta: event.Delta,
				Phase: event.Phase,
			})
		case StreamEventAssistantCommentary:
			onEvent(provideriface.StreamEvent{
				Type:  provideriface.StreamEventAssistantCommentary,
				Delta: event.Delta,
				Phase: event.Phase,
			})
		case StreamEventReasoningSummaryDelta:
			onEvent(provideriface.StreamEvent{
				Type:         provideriface.StreamEventReasoningSummaryDelta,
				Delta:        event.Delta,
				ReasoningKey: event.ReasoningKey,
			})
		}
	})
	if err != nil {
		return provideriface.Response{}, err
	}
	return fromCodexResponse(out), nil
}

func toCodexRequest(req provideriface.Request) Request {
	return Request{
		SessionID:         req.SessionID,
		Model:             req.Model,
		Thinking:          req.Thinking,
		Instructions:      req.Instructions,
		Input:             req.Input,
		Tools:             toCodexTools(req.Tools),
		ToolChoice:        req.ToolChoice,
		ServiceTier:       NormalizeServiceTier(req.ServiceTier),
		ContextMode:       NormalizeContextMode(req.ContextMode),
		ContextWindow:     req.ContextWindow,
		ParallelToolCalls: req.ParallelToolCalls,
	}
}

func toCodexTools(input []provideriface.ToolDefinition) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(input))
	for _, definition := range input {
		out = append(out, ToolDefinition{
			Type:        definition.Type,
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  sanitizeCodexToolParameters(definition.Parameters),
			Strict:      false,
		})
	}
	return out
}

func sanitizeCodexToolParameters(parameters map[string]any) map[string]any {
	cleaned, ok := sanitizeCodexSchemaValue(parameters).(map[string]any)
	if !ok || len(cleaned) == 0 {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	if len(normalizedCodexSchemaTypes(cleaned["type"])) == 0 {
		cleaned["type"] = "object"
	}
	if codexSchemaHasType(cleaned["type"], "object") && cleaned["properties"] == nil {
		cleaned["properties"] = map[string]any{}
	}
	return rewriteCodexFreeformObjectSchemas(cleaned)
}

func sanitizeCodexSchemaValue(value any) any {
	switch typed := value.(type) {
	case bool:
		return map[string]any{"type": "string"}
	case map[string]any:
		return sanitizeCodexSchemaObject(typed)
	case []map[string]any:
		return sanitizeCodexSchemaArray(typed)
	case []any:
		return sanitizeCodexSchemaArray(typed)
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func sanitizeCodexSchemaObject(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	out := make(map[string]any, 8)

	if description := strings.TrimSpace(asString(raw["description"])); description != "" {
		out["description"] = description
	}

	if enumValues := sanitizeCodexLiteralArray(raw["enum"]); len(enumValues) > 0 {
		out["enum"] = enumValues
	} else if constValue, ok := raw["const"]; ok {
		out["enum"] = []any{constValue}
	}

	if properties := sanitizeCodexSchemaProperties(raw["properties"]); properties != nil {
		out["properties"] = properties
	}
	if items, ok := sanitizeCodexSchemaValue(raw["items"]).(map[string]any); ok && len(items) > 0 {
		out["items"] = items
	}
	switch typed := raw["additionalProperties"].(type) {
	case bool:
		out["additionalProperties"] = typed
	case nil:
	default:
		if schema, ok := sanitizeCodexSchemaValue(typed).(map[string]any); ok && len(schema) > 0 {
			out["additionalProperties"] = schema
		}
	}

	anyOf := sanitizeCodexSchemaArray(firstNonNil(raw["anyOf"], raw["oneOf"]))
	if len(anyOf) > 0 {
		out["anyOf"] = anyOf
	}

	schemaTypes := normalizedCodexSchemaTypes(raw["type"])
	if len(schemaTypes) == 0 && len(anyOf) == 0 {
		schemaTypes = inferCodexSchemaTypes(raw, out)
	}
	writeCodexSchemaTypes(out, schemaTypes)
	ensureCodexSchemaDefaults(out, schemaTypes)

	if required := sanitizeCodexRequired(raw["required"]); len(required) > 0 {
		out["required"] = required
	}

	return out
}

func sanitizeCodexSchemaProperties(value any) map[string]any {
	typed, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]any, len(typed))
	for key, item := range typed {
		schema, ok := sanitizeCodexSchemaValue(item).(map[string]any)
		if !ok || len(schema) == 0 {
			continue
		}
		out[key] = schema
	}
	return out
}

func sanitizeCodexSchemaArray(value any) []any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			schema, ok := sanitizeCodexSchemaValue(item).(map[string]any)
			if !ok || len(schema) == 0 {
				continue
			}
			out = append(out, schema)
		}
		return out
	default:
		return nil
	}
}

func sanitizeCodexLiteralArray(value any) []any {
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...)
	default:
		return nil
	}
}

func sanitizeCodexRequired(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(asString(item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizedCodexSchemaTypes(value any) []string {
	switch typed := value.(type) {
	case string:
		if name := normalizeCodexSchemaTypeName(typed); name != "" {
			return []string{name}
		}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if name := normalizeCodexSchemaTypeName(asString(item)); name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}

func normalizeCodexSchemaTypeName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "string", "number", "boolean", "integer", "object", "array", "null":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func inferCodexSchemaTypes(raw, sanitized map[string]any) []string {
	switch {
	case raw["properties"] != nil || raw["required"] != nil || raw["additionalProperties"] != nil:
		return []string{"object"}
	case raw["items"] != nil || raw["prefixItems"] != nil:
		return []string{"array"}
	case raw["enum"] != nil || raw["format"] != nil:
		return []string{"string"}
	case raw["minimum"] != nil || raw["maximum"] != nil || raw["exclusiveMinimum"] != nil || raw["exclusiveMaximum"] != nil || raw["multipleOf"] != nil:
		return []string{"number"}
	case sanitized["properties"] != nil:
		return []string{"object"}
	case sanitized["items"] != nil:
		return []string{"array"}
	default:
		return []string{"string"}
	}
}

func writeCodexSchemaTypes(schema map[string]any, schemaTypes []string) {
	switch len(schemaTypes) {
	case 0:
		delete(schema, "type")
	case 1:
		schema["type"] = schemaTypes[0]
	default:
		out := make([]any, 0, len(schemaTypes))
		for _, schemaType := range schemaTypes {
			out = append(out, schemaType)
		}
		schema["type"] = out
	}
}

func ensureCodexSchemaDefaults(schema map[string]any, schemaTypes []string) {
	for _, schemaType := range schemaTypes {
		switch schemaType {
		case "object":
			if _, ok := schema["properties"]; !ok {
				schema["properties"] = map[string]any{}
			}
			if _, ok := schema["additionalProperties"]; !ok {
				schema["additionalProperties"] = true
			}
		case "array":
			if _, ok := schema["items"]; !ok {
				schema["items"] = map[string]any{"type": "string"}
			}
		}
	}
}

func codexSchemaHasType(value any, want string) bool {
	for _, schemaType := range normalizedCodexSchemaTypes(value) {
		if schemaType == want {
			return true
		}
	}
	return false
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func rewriteCodexFreeformObjectSchemas(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	if rewritten, ok := rewriteCodexFreeformObjectSchemaValue(schema).(map[string]any); ok && len(rewritten) > 0 {
		return rewritten
	}
	return schema
}

func rewriteCodexFreeformObjectSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		schema := make(map[string]any, len(typed))
		for key, item := range typed {
			switch key {
			case "properties":
				properties, ok := item.(map[string]any)
				if !ok {
					continue
				}
				rewritten := make(map[string]any, len(properties))
				for name, child := range properties {
					rewrittenChild, ok := rewriteCodexFreeformObjectSchemaValue(child).(map[string]any)
					if ok && len(rewrittenChild) > 0 {
						rewritten[name] = rewrittenChild
					}
				}
				schema[key] = rewritten
			case "items", "additionalProperties":
				if child, ok := rewriteCodexFreeformObjectSchemaValue(item).(map[string]any); ok && len(child) > 0 {
					schema[key] = child
				} else if booleanValue, ok := item.(bool); ok {
					schema[key] = booleanValue
				}
			case "anyOf":
				if variants, ok := rewriteCodexFreeformObjectSchemaValue(item).([]any); ok && len(variants) > 0 {
					schema[key] = variants
				}
			default:
				schema[key] = item
			}
		}
		if shouldRewriteCodexFreeformObjectSchema(schema) {
			description := strings.TrimSpace(asString(schema["description"]))
			if description == "" {
				description = "JSON-encoded object value"
			} else {
				description += " Pass this as a JSON-encoded object string."
			}
			return map[string]any{
				"type":        "string",
				"description": description,
			}
		}
		return schema
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			rewritten, ok := rewriteCodexFreeformObjectSchemaValue(item).(map[string]any)
			if ok && len(rewritten) > 0 {
				out = append(out, rewritten)
			}
		}
		return out
	default:
		return value
	}
}

func shouldRewriteCodexFreeformObjectSchema(schema map[string]any) bool {
	if !codexSchemaHasType(schema["type"], "object") {
		return false
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 0 {
		return false
	}
	additional, exists := schema["additionalProperties"]
	if !exists {
		return false
	}
	flag, ok := additional.(bool)
	return ok && flag
}

func fromCodexResponse(resp Response) provideriface.Response {
	out := provideriface.Response{
		ID:               resp.ID,
		Model:            resp.Model,
		StopReason:       resp.StopReason,
		Text:             resp.Text,
		ReasoningSummary: resp.ReasoningSummary,
		Usage: provideriface.TokenUsage{
			InputTokens:      resp.Usage.InputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
			ThinkingTokens:   resp.Usage.ThinkingTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			CacheReadTokens:  resp.Usage.CacheReadTokens,
			CacheWriteTokens: resp.Usage.CacheWriteTokens,
			Source:           resp.Usage.Source,
			Transport:        resp.Usage.Transport,
			ConnectedViaWS:   cloneBoolPointer(resp.Usage.ConnectedViaWS),
			APIUsageRaw:      resp.Usage.APIUsageRaw,
			APIUsageRawPath:  resp.Usage.APIUsageRawPath,
			APIUsageHistory:  resp.Usage.APIUsageHistory,
			APIUsagePaths:    resp.Usage.APIUsagePaths,
		},
	}
	if len(resp.Messages) > 0 {
		out.AssistantMessages = make([]provideriface.AssistantMessage, 0, len(resp.Messages))
		for _, message := range resp.Messages {
			text := strings.TrimSpace(message.Text)
			if text == "" {
				continue
			}
			out.AssistantMessages = append(out.AssistantMessages, provideriface.AssistantMessage{Text: text, Phase: message.Phase})
		}
	}
	if len(resp.FunctionCalls) == 0 {
		return out
	}
	out.FunctionCalls = make([]provideriface.FunctionCall, 0, len(resp.FunctionCalls))
	for _, call := range resp.FunctionCalls {
		out.FunctionCalls = append(out.FunctionCalls, provideriface.FunctionCall{
			CallID:    call.CallID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
	}
	return out
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
