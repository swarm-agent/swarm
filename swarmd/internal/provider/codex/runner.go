package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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

func (r *Runner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode:                provideriface.ExecutionEpochContextResponsesChain,
		EpochScopedCacheKey:        true,
		EpochScopedSessionAffinity: true,
		TransportReusable:          true,
	}
}

func (r *Runner) MediaCapabilityDeclaration(ctx context.Context) (provideriface.MediaAdapterDeclaration, error) {
	if r == nil || r.client == nil {
		return provideriface.MediaAdapterDeclaration{}, errors.New("codex runner client is not configured")
	}
	record, err := r.client.ensureAuth(ctx)
	if err != nil {
		return provideriface.MediaAdapterDeclaration{}, err
	}
	if record.Type != pebblestore.CodexAuthTypeOAuth || !strings.EqualFold(strings.TrimSpace(record.Provider), "codex") {
		return provideriface.MediaAdapterDeclaration{}, errors.New("codex media requires the codex OAuth credential surface")
	}
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{record.AccountScopeID, record.Provider, record.ID, record.Type, record.AccountID}, "\x00")))
	return provideriface.MediaAdapterDeclaration{
		AdapterID:             provideriface.MediaAdapterIDCodexChatGPTV1,
		ProviderID:            "codex",
		ProviderSurface:       provideriface.MediaProviderSurfaceCodexChatGPT,
		CredentialSurface:     provideriface.MediaCredentialSurfaceCodexOAuth,
		CredentialFingerprint: hex.EncodeToString(fingerprint[:16]),
		Inputs: []provideriface.MediaAdapterCapability{
			// The OAuth client surface currently implements only model-native image
			// input. This provider-surface vocabulary is the transport ceiling shared
			// by every image-capable Codex model; model catalog facts may only narrow it.
			// Catalog-declared client-processed documents stay fail-closed until a
			// bounded document conversion pipeline exists.
			{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/gif", "image/jpeg", "image/png", "image/webp"}, ContentTypes: []string{"input_image"}, MaxBytes: 20 << 20, MaxCount: 20},
		},
	}, nil
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r == nil || r.client == nil {
		return provideriface.Response{}, errors.New("codex runner client is not configured")
	}
	if err := validateRunnerMediaSurface(req.MediaContract, "codex", provideriface.MediaProviderSurfaceCodexChatGPT, provideriface.MediaCredentialSurfaceCodexOAuth, provideriface.MediaAdapterIDCodexChatGPTV1); err != nil {
		return provideriface.Response{}, err
	}
	out, err := r.client.CreateResponse(ctx, ToRequest(req))
	if err != nil {
		return provideriface.Response{}, err
	}
	return FromResponse(out), nil
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r == nil || r.client == nil {
		return provideriface.Response{}, errors.New("codex runner client is not configured")
	}
	if err := validateRunnerMediaSurface(req.MediaContract, "codex", provideriface.MediaProviderSurfaceCodexChatGPT, provideriface.MediaCredentialSurfaceCodexOAuth, provideriface.MediaAdapterIDCodexChatGPTV1); err != nil {
		return provideriface.Response{}, err
	}
	out, err := r.client.CreateResponseStreaming(ctx, ToRequest(req), ToProviderStreamEventCallbackWithContext(onEvent, "codex", req.Model))
	if err != nil {
		return provideriface.Response{}, err
	}
	return FromResponse(out), nil
}

func validateRunnerMediaSurface(contract provideriface.SessionMediaContract, providerID, providerSurface, credentialSurface, adapterID string) error {
	if strings.TrimSpace(contract.Hash) == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(contract.ProviderID), providerID) || contract.ProviderSurface != providerSurface || contract.CredentialSurface != credentialSurface || contract.AdapterID != adapterID {
		return errors.New("media contract does not match the active provider transport surface")
	}
	return nil
}

func ToProviderStreamEventCallback(onEvent func(provideriface.StreamEvent)) func(StreamEvent) {
	return ToProviderStreamEventCallbackWithContext(onEvent, "codex", "")
}

func ToProviderStreamEventCallbackWithContext(onEvent func(provideriface.StreamEvent), providerID, model string) func(StreamEvent) {
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	var mu sync.Mutex
	var startedAtByKey = make(map[string]int64, 4)
	return func(event StreamEvent) {
		if onEvent == nil {
			return
		}
		now := int64(0)
		startedAt := int64(0)
		status := ""
		switch event.Type {
		case StreamEventToolCallStarted:
			status = "started"
		case StreamEventToolCallArgumentsDelta, StreamEventToolCallArgumentsSnapshot:
			status = "building"
		case StreamEventToolCallCompleted:
			status = "completed"
		}
		if status != "" {
			now = time.Now().UnixMilli()
			key := toolCallStreamKey(event)
			mu.Lock()
			startedAt = startedAtByKey[key]
			if event.Type == StreamEventToolCallStarted || startedAt == 0 {
				startedAt = now
				if key != "" {
					startedAtByKey[key] = startedAt
				}
			}
			if event.Type == StreamEventToolCallCompleted && key != "" {
				delete(startedAtByKey, key)
			}
			mu.Unlock()
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
				DeltaMode:    provideriface.StreamEventDeltaModeReplace,
				ReasoningKey: event.ReasoningKey,
			})
		case StreamEventToolCallStarted:
			onEvent(provideriface.StreamEvent{
				Type:             provideriface.StreamEventToolCallStarted,
				ToolCallID:       event.ToolCallID,
				ToolCallIndex:    cloneIntPtr(event.ToolCallIndex),
				ToolName:         event.ToolName,
				ProviderID:       providerID,
				Model:            model,
				RecordedAtUnixMs: now,
				StartedAtUnixMs:  startedAt,
				Status:           status,
				Metadata:         cloneMapStringAny(event.Metadata),
			})
		case StreamEventToolCallArgumentsDelta:
			onEvent(provideriface.StreamEvent{
				Type:             provideriface.StreamEventToolCallArgumentsDelta,
				Delta:            event.Delta,
				ToolCallID:       event.ToolCallID,
				ToolCallIndex:    cloneIntPtr(event.ToolCallIndex),
				ToolName:         event.ToolName,
				ArgumentsDelta:   event.ArgumentsDelta,
				ProviderID:       providerID,
				Model:            model,
				RecordedAtUnixMs: now,
				StartedAtUnixMs:  startedAt,
				Status:           status,
				Metadata:         cloneMapStringAny(event.Metadata),
			})
		case StreamEventToolCallArgumentsSnapshot:
			onEvent(provideriface.StreamEvent{
				Type:              provideriface.StreamEventToolCallArgumentsSnapshot,
				ToolCallID:        event.ToolCallID,
				ToolCallIndex:     cloneIntPtr(event.ToolCallIndex),
				ToolName:          event.ToolName,
				Arguments:         event.Arguments,
				ArgumentsSnapshot: event.ArgumentsSnapshot,
				ProviderID:        providerID,
				Model:             model,
				RecordedAtUnixMs:  now,
				StartedAtUnixMs:   startedAt,
				Status:            status,
				Metadata:          cloneMapStringAny(event.Metadata),
			})
		case StreamEventToolCallCompleted:
			onEvent(provideriface.StreamEvent{
				Type:             provideriface.StreamEventToolCallCompleted,
				ToolCallID:       event.ToolCallID,
				ToolCallIndex:    cloneIntPtr(event.ToolCallIndex),
				ToolName:         event.ToolName,
				Arguments:        event.Arguments,
				ProviderID:       providerID,
				Model:            model,
				RecordedAtUnixMs: now,
				StartedAtUnixMs:  startedAt,
				Status:           status,
				Metadata:         cloneMapStringAny(event.Metadata),
			})
		}
	}
}

func toCodexRequest(req provideriface.Request) Request {
	return ToRequest(req)
}

func ToRequest(req provideriface.Request) Request {
	serviceTier := strings.ToLower(strings.TrimSpace(req.ServiceTier))
	reasoningProviderValue := ""
	if catalog, ok := req.ModelCatalog.(pebblestore.ModelCatalogRecord); ok {
		serviceTier = codexServiceTierProviderValue(catalog, serviceTier)
		reasoningProviderValue = codexThinkingProviderValue(catalog, req.Thinking)
	}
	return Request{
		SessionID:                     req.SessionID,
		ProviderLineageID:             req.ProviderLineageID,
		ContextBranchID:               req.ContextBranchID,
		ProviderConfigurationHash:     req.ProviderConfigurationHash,
		ProviderCacheKey:              req.EffectiveProviderCacheKey(),
		SessionAffinityKey:            req.EffectiveSessionAffinityKey(),
		TransportAffinityKey:          req.TransportAffinityKey,
		BoundaryReason:                req.BoundaryReason,
		PreviousProviderLineageID:     req.PreviousProviderLineageID,
		PreviousProviderID:            req.PreviousProviderID,
		PreviousModel:                 req.PreviousModel,
		NewProviderID:                 req.NewProviderID,
		NewModel:                      req.NewModel,
		HandoffSummaryMessageID:       req.HandoffSummaryMessageID,
		HandoffSummaryGlobalSeq:       req.HandoffSummaryGlobalSeq,
		ProviderLineageStartMessageID: req.ProviderLineageStartMessageID,
		ProviderLineageStartRunID:     req.ProviderLineageStartRunID,
		ProviderLineageStartGlobalSeq: req.ProviderLineageStartGlobalSeq,
		StartNewChain:                 req.StartNewChain,
		AllowContinuation:             req.AllowContinuation,
		ReuseTransport:                req.ReuseTransport,
		ResetTransport:                req.ResetTransport,
		NativeContinuationAllowed:     req.NativeContinuationAllowed,
		ForceFreshProviderContext:     req.ForceFreshProviderContext,
		Model:                         req.Model,
		Thinking:                      req.Thinking,
		ReasoningProviderValue:        reasoningProviderValue,
		Instructions:                  req.Instructions,
		Input:                         req.Input,
		Tools:                         toCodexTools(req.Tools),
		ToolChoice:                    req.ToolChoice,
		ServiceTier:                   serviceTier,
		ContextMode:                   NormalizeContextMode(req.ContextMode),
		ContextWindow:                 req.ContextWindow,
		MediaContract:                 req.MediaContract,
		ParallelToolCalls:             req.ParallelToolCalls,
	}
}

func codexServiceTierProviderValue(catalog pebblestore.ModelCatalogRecord, serviceTier string) string {
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	if serviceTier == "" {
		return ""
	}
	for _, mapping := range catalog.ServiceTierMappings {
		matchesTier := strings.EqualFold(strings.TrimSpace(mapping.Tier), serviceTier)
		matchesSwarmSetting := strings.EqualFold(strings.TrimSpace(mapping.SwarmSetting), serviceTier)
		if !matchesTier && !matchesSwarmSetting {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(mapping.ProviderParameter), "service_tier") {
			return strings.TrimSpace(mapping.ProviderValue)
		}
		return ""
	}
	return ""
}

func codexThinkingProviderValue(catalog pebblestore.ModelCatalogRecord, thinking string) string {
	thinking = strings.ToLower(strings.TrimSpace(thinking))
	for _, mapping := range catalog.ThinkingMappings {
		if strings.EqualFold(strings.TrimSpace(mapping.SwarmSetting), thinking) {
			if value := strings.TrimSpace(mapping.EffectiveProviderValue); value != "" {
				return value
			}
			return strings.TrimSpace(mapping.ProviderValue)
		}
	}
	return codexDefaultReasoningEffortProviderValue(catalog, thinking)
}

func codexDefaultReasoningEffortProviderValue(catalog pebblestore.ModelCatalogRecord, thinking string) string {
	if !catalog.Reasoning {
		return ""
	}
	if !codexUsesReasoningEffortParameter(catalog.ThinkingProviderParameter) && !strings.EqualFold(strings.TrimSpace(catalog.Provider), "openai") {
		return ""
	}
	thinking = strings.ToLower(strings.TrimSpace(thinking))
	switch thinking {
	case "low", "medium", "high":
		return thinking
	case "xhigh":
		if strings.EqualFold(strings.TrimSpace(catalog.Provider), "openai") && codexOpenAIModelSupportsXHighFallback(catalog.Model) {
			return thinking
		}
		return ""
	case "off":
		return "none"
	default:
		return ""
	}
}

func codexOpenAIModelSupportsXHighFallback(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	switch model {
	case "gpt-5.2", "gpt-5.2-pro", "gpt-5.4", "gpt-5.5":
		return true
	}
	for _, prefix := range []string{"gpt-5.2-", "gpt-5.2-pro-", "gpt-5.4-", "gpt-5.5-"} {
		if strings.HasPrefix(model, prefix) && len(model) > len(prefix) && model[len(prefix)] >= '0' && model[len(prefix)] <= '9' {
			return true
		}
	}
	return false
}

func codexUsesReasoningEffortParameter(parameter string) bool {
	switch strings.ToLower(strings.TrimSpace(parameter)) {
	case "reasoning.effort", "reasoning_effort":
		return true
	default:
		return false
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
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
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

func FromResponse(resp Response) provideriface.Response {
	out := provideriface.Response{
		ID:               resp.ID,
		Model:            resp.Model,
		StopReason:       resp.StopReason,
		Text:             resp.Text,
		ReasoningSummary: resp.ReasoningSummary,
		Raw:              resp.Raw,
		Usage: provideriface.TokenUsage{
			InputTokens:      resp.Usage.InputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
			ThinkingTokens:   resp.Usage.ThinkingTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			CacheReadTokens:  resp.Usage.CacheReadTokens,
			CacheWriteTokens: resp.Usage.CacheWriteTokens,
			ServiceTier:      resp.Usage.ServiceTier,
			EstimatedCostUSD: resp.Usage.EstimatedCostUSD,
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

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneMapStringAny(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
