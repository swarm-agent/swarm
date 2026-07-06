package fireworks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/privacy"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type Runner struct {
	authStore *pebblestore.AuthStore
	client    *Client
}

func NewRunner(authStore *pebblestore.AuthStore) *Runner {
	return &Runner{
		authStore: authStore,
		client:    NewClient(),
	}
}

func (r *Runner) ID() string {
	return "fireworks"
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.createResponse(ctx, req)
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.createStreamingResponse(ctx, req, onEvent)
}

func (r *Runner) createResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r == nil || r.authStore == nil {
		return provideriface.Response{}, errors.New("fireworks runner auth store is not configured")
	}
	if r.client == nil {
		r.client = NewClient()
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return provideriface.Response{}, errors.New("model is required")
	}
	serving := ResolveServingTier(req, ServingConfigFromCatalog(req.ModelCatalog))
	record, err := r.activeCredential(ctx)
	if err != nil {
		return provideriface.Response{}, err
	}
	payload := buildChatCompletionRequest(req)
	applyServingResolutionToPayload(&payload, serving)
	fireworksDebugEvent("request", map[string]any{
		"transport":        "sync",
		"session_id":       req.SessionID,
		"model":            modelID,
		"effective_model":  serving.ModelID,
		"requested_tier":   serving.RequestedTier,
		"effective_tier":   serving.EffectiveTier,
		"service_tier":     serving.ServiceTier,
		"session_affinity": serving.SessionAffinity != "",
		"payload":          fireworksDebugJSONValue(payload),
	})
	decoded, err := r.client.CreateChatCompletion(ctx, record.APIKey, payload, requestOptions{SessionAffinity: serving.SessionAffinity, PromptCacheIsolationKey: serving.PromptCacheIsolationKey})
	if err != nil {
		return provideriface.Response{}, err
	}
	result := parseChatCompletionResponse(decoded)
	annotateUsage(&result.Usage, serving)
	fireworksDebugEvent("response", map[string]any{
		"transport":  "sync",
		"session_id": req.SessionID,
		"model":      modelID,
		"decoded":    fireworksDebugJSONValue(decoded),
		"parsed":     fireworksDebugJSONValue(result),
	})
	if strings.TrimSpace(result.Model) == "" {
		result.Model = modelID
	}
	return result, nil
}

func (r *Runner) createStreamingResponse(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r == nil || r.authStore == nil {
		return provideriface.Response{}, errors.New("fireworks runner auth store is not configured")
	}
	if r.client == nil {
		r.client = NewClient()
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return provideriface.Response{}, errors.New("model is required")
	}
	serving := ResolveServingTier(req, ServingConfigFromCatalog(req.ModelCatalog))
	record, err := r.activeCredential(ctx)
	if err != nil {
		return provideriface.Response{}, err
	}
	payload := buildChatCompletionRequest(req)
	applyServingResolutionToPayload(&payload, serving)
	fireworksDebugEvent("request", map[string]any{
		"transport":        "stream",
		"session_id":       req.SessionID,
		"model":            modelID,
		"effective_model":  serving.ModelID,
		"requested_tier":   serving.RequestedTier,
		"effective_tier":   serving.EffectiveTier,
		"service_tier":     serving.ServiceTier,
		"session_affinity": serving.SessionAffinity != "",
		"payload":          fireworksDebugJSONValue(payload),
	})
	reasoningByKey := make(map[string]string, 4)
	toolConstruction := newFireworksToolCallConstructionState()
	decoded, err := r.client.CreateChatCompletionStream(ctx, record.APIKey, payload, func(chunk chatCompletionChunk) error {
		if fireworksDebugChunkInteresting(chunk) {
			fireworksDebugEvent("stream_chunk", map[string]any{
				"session_id": req.SessionID,
				"model":      modelID,
				"chunk":      fireworksDebugJSONValue(chunk),
			})
		}
		for _, choice := range chunk.Choices {
			if choice.Delta == nil || onEvent == nil {
				continue
			}
			if choice.Delta.ReasoningContent != "" {
				reasoningKey := fireworksReasoningKey(choice.Index)
				reasoningByKey[reasoningKey] += choice.Delta.ReasoningContent
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: reasoningByKey[reasoningKey], ReasoningKey: reasoningKey})
			}
			if choice.Delta.Content != "" {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: choice.Delta.Content})
			}
		}
		emitFireworksToolCallConstructionEvents(toolConstruction, chunk, onEvent)
		return nil
	}, requestOptions{SessionAffinity: serving.SessionAffinity, PromptCacheIsolationKey: serving.PromptCacheIsolationKey})
	if err != nil {
		return provideriface.Response{}, err
	}
	result := parseChatCompletionResponse(decoded)
	annotateUsage(&result.Usage, serving)
	fireworksDebugEvent("response", map[string]any{
		"transport":  "stream",
		"session_id": req.SessionID,
		"model":      modelID,
		"decoded":    fireworksDebugJSONValue(decoded),
		"parsed":     fireworksDebugJSONValue(result),
	})
	if strings.TrimSpace(result.Model) == "" {
		result.Model = modelID
	}
	return result, nil
}

func (r *Runner) activeCredential(ctx context.Context) (pebblestore.AuthCredentialRecord, error) {
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK || strings.TrimSpace(principal.AccountScopeID) == "" {
		return pebblestore.AuthCredentialRecord{}, identity.ErrPrincipalRequired
	}
	record, ok, err := r.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "fireworks")
	if err != nil {
		return pebblestore.AuthCredentialRecord{}, fmt.Errorf("read fireworks auth: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return pebblestore.AuthCredentialRecord{}, errors.New("fireworks auth is not configured")
	}
	return record, nil
}

func buildChatCompletionRequest(req provideriface.Request) chatCompletionRequest {
	out := chatCompletionRequest{
		Model:           strings.TrimSpace(req.Model),
		Messages:        buildChatCompletionMessages(req),
		ReasoningEffort: fireworksReasoningEffortForRequest(req),
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]chatCompletionTool, 0, len(req.Tools))
		for _, definition := range req.Tools {
			name := strings.TrimSpace(definition.Name)
			if name == "" {
				continue
			}
			out.Tools = append(out.Tools, chatCompletionTool{
				Type: "function",
				Function: chatCompletionToolFunction{
					Name:        name,
					Description: strings.TrimSpace(definition.Description),
					Parameters:  sanitizeFireworksToolParameters(definition.Parameters),
				},
			})
		}
		if len(out.Tools) > 0 {
			out.ToolChoice = mapToolChoice(req.ToolChoice)
			parallel := req.ParallelToolCalls
			out.ParallelToolCalls = &parallel
		}
	}
	return out
}

func applyServingResolutionToPayload(payload *chatCompletionRequest, serving requestServingResolution) {
	if payload == nil {
		return
	}
	if strings.TrimSpace(serving.ModelID) != "" {
		payload.Model = strings.TrimSpace(serving.ModelID)
	}
	if strings.TrimSpace(serving.ServiceTier) != "" {
		// Fireworks Standard omits service_tier. Priority uses service_tier=priority
		// on the normal model ID. Fast uses the provider-published router model ID
		// from the Swarm snapshot and intentionally does not also request Priority.
		payload.ServiceTier = strings.TrimSpace(serving.ServiceTier)
	}
}

func buildChatCompletionMessages(req provideriface.Request) []map[string]any {
	messages := make([]map[string]any, 0, len(req.Input)+1)
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": instructions,
		})
	}
	for _, item := range req.Input {
		if typeName, ok := stringField(item, "type"); ok {
			switch strings.ToLower(strings.TrimSpace(typeName)) {
			case "function_call":
				messages = append(messages, mapFunctionCallMessage(item))
				continue
			case "function_call_output":
				messages = append(messages, mapFunctionOutputMessage(item))
				continue
			}
		}
		role, _ := stringField(item, "role")
		content := extractMessageText(item["content"])
		if strings.TrimSpace(content) == "" {
			continue
		}
		mappedRole := "user"
		if strings.EqualFold(strings.TrimSpace(role), "assistant") {
			mappedRole = "assistant"
		}
		messages = append(messages, map[string]any{
			"role":    mappedRole,
			"content": content,
		})
	}
	return messages
}

func mapFunctionCallMessage(item map[string]any) map[string]any {
	callID, _ := stringField(item, "call_id")
	name, _ := stringField(item, "name")
	arguments, _ := stringField(item, "arguments")
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	arguments = normalizeJSONArguments(arguments)
	arguments = normalizeJSONArguments(arguments)
	toolCall := map[string]any{
		"id":   strings.TrimSpace(callID),
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
	if strings.TrimSpace(callID) == "" {
		delete(toolCall, "id")
	}
	return map[string]any{
		"role":       "assistant",
		"content":    "",
		"tool_calls": []map[string]any{toolCall},
	}
}

func mapFunctionOutputMessage(item map[string]any) map[string]any {
	callID, _ := stringField(item, "call_id")
	output, _ := stringField(item, "output")
	return map[string]any{
		"role":         "tool",
		"tool_call_id": strings.TrimSpace(callID),
		"content":      strings.TrimSpace(output),
	}
}

func sanitizeFireworksToolParameters(parameters map[string]any) map[string]any {
	out := sanitizeFireworksSchemaMap(parameters, false)
	if len(out) == 0 {
		out = map[string]any{}
	}
	if strings.TrimSpace(schemaString(out["type"])) == "" {
		out["type"] = "object"
	}
	if strings.EqualFold(strings.TrimSpace(schemaString(out["type"])), "object") {
		if _, ok := out["properties"].(map[string]any); !ok {
			out["properties"] = map[string]any{}
		}
	}
	return out
}

func sanitizeFireworksRequired(value any) []string {
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
			if text := strings.TrimSpace(schemaString(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func sanitizeFireworksSchemaMap(input map[string]any, keepRequired bool) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		if value == nil {
			continue
		}
		if key == "required" {
			if !keepRequired {
				if required := sanitizeFireworksRequired(value); len(required) > 0 {
					out[key] = required
				}
			}
			continue
		}
		out[key] = sanitizeFireworksSchemaValue(value, key == "properties")
	}
	return out
}

func sanitizeFireworksSchemaValue(value any, keepRequired bool) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeFireworksSchemaMap(typed, keepRequired)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if item != nil {
				out = append(out, sanitizeFireworksSchemaValue(item, keepRequired))
			}
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func schemaString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func fireworksReasoningEffortForRequest(req provideriface.Request) string {
	thinking := strings.ToLower(strings.TrimSpace(req.Thinking))
	if thinking == "" {
		return ""
	}
	catalog, ok := req.ModelCatalog.(pebblestore.ModelCatalogRecord)
	if !ok {
		return ""
	}
	for _, mapping := range catalog.ThinkingMappings {
		if !strings.EqualFold(strings.TrimSpace(mapping.SwarmSetting), thinking) {
			continue
		}
		return strings.TrimSpace(firstNonEmpty(mapping.EffectiveProviderValue, mapping.ProviderValue))
	}
	return ""
}

func fireworksReasoningKey(index int) string {
	if index < 0 {
		return "fireworks-reasoning"
	}
	return fmt.Sprintf("fireworks-reasoning-%d", index)
}

func mapToolChoice(choice string) any {
	choice = strings.ToLower(strings.TrimSpace(choice))
	switch choice {
	case "", "auto":
		return "auto"
	case "none", "required":
		return choice
	default:
		return "auto"
	}
}

type fireworksToolCallConstructionState struct {
	seenStarted   map[int]bool
	seenCompleted map[int]bool
	arguments     map[int]string
	ids           map[int]string
	names         map[int]string
}

func newFireworksToolCallConstructionState() *fireworksToolCallConstructionState {
	return &fireworksToolCallConstructionState{
		seenStarted:   make(map[int]bool),
		seenCompleted: make(map[int]bool),
		arguments:     make(map[int]string),
		ids:           make(map[int]string),
		names:         make(map[int]string),
	}
}

func emitFireworksToolCallConstructionEvents(state *fireworksToolCallConstructionState, chunk chatCompletionChunk, onEvent func(provideriface.StreamEvent)) {
	if state == nil || onEvent == nil || len(chunk.Choices) == 0 {
		return
	}
	for _, choice := range chunk.Choices {
		if choice.Delta != nil {
			for _, delta := range choice.Delta.ToolCalls {
				index := delta.Index
				if id := strings.TrimSpace(delta.ID); id != "" {
					state.ids[index] = id
				}
				if name := strings.TrimSpace(fireworksToolCallDeltaName(delta)); name != "" {
					state.names[index] = name
				}
				if !state.seenStarted[index] {
					state.seenStarted[index] = true
					onEvent(provideriface.StreamEvent{
						Type:          provideriface.StreamEventToolCallStarted,
						ToolCallID:    state.ids[index],
						ToolCallIndex: intPointer(index),
						ToolName:      state.names[index],
						Metadata:      fireworksToolCallDeltaMetadata(choice.Index, delta, "fireworks.chat.completions.chunk.delta"),
					})
				}
				if delta.Function != nil && delta.Function.Arguments != "" {
					state.arguments[index] += delta.Function.Arguments
					onEvent(provideriface.StreamEvent{
						Type:           provideriface.StreamEventToolCallArgumentsDelta,
						Delta:          delta.Function.Arguments,
						ToolCallID:     state.ids[index],
						ToolCallIndex:  intPointer(index),
						ToolName:       state.names[index],
						ArgumentsDelta: delta.Function.Arguments,
						Metadata:       fireworksToolCallDeltaMetadata(choice.Index, delta, "fireworks.chat.completions.chunk.delta"),
					})
				}
			}
		}
		if strings.EqualFold(strings.TrimSpace(choice.FinishReason), "tool_calls") {
			emitCompletedFireworksToolCallConstructionEvents(state, choice.Index, onEvent)
		}
	}
}

func emitCompletedFireworksToolCallConstructionEvents(state *fireworksToolCallConstructionState, choiceIndex int, onEvent func(provideriface.StreamEvent)) {
	for index := range state.seenStarted {
		if state.seenCompleted[index] {
			continue
		}
		state.seenCompleted[index] = true
		onEvent(provideriface.StreamEvent{
			Type:          provideriface.StreamEventToolCallCompleted,
			ToolCallID:    state.ids[index],
			ToolCallIndex: intPointer(index),
			ToolName:      state.names[index],
			Arguments:     strings.TrimSpace(state.arguments[index]),
			Metadata: map[string]any{
				"provider":     "fireworks",
				"source":       "fireworks.chat.completions.chunk.finish",
				"choice_index": choiceIndex,
			},
		})
	}
}

func fireworksToolCallDeltaName(delta chatCompletionToolCallDelta) string {
	if delta.Function == nil {
		return ""
	}
	return strings.TrimSpace(delta.Function.Name)
}

func fireworksToolCallDeltaMetadata(choiceIndex int, delta chatCompletionToolCallDelta, source string) map[string]any {
	metadata := map[string]any{
		"provider":     "fireworks",
		"source":       source,
		"choice_index": choiceIndex,
	}
	if typeName := strings.TrimSpace(delta.Type); typeName != "" {
		metadata["tool_call_type"] = typeName
	}
	return metadata
}

func intPointer(value int) *int {
	out := value
	return &out
}

func parseChatCompletionResponse(resp chatCompletionResponse) provideriface.Response {
	out := provideriface.Response{
		ID:    strings.TrimSpace(resp.ID),
		Model: strings.TrimSpace(resp.Model),
		Usage: parseUsage(resp.Usage),
	}
	if len(resp.Choices) == 0 {
		return out
	}
	choice := resp.Choices[0]
	out.StopReason = strings.TrimSpace(choice.FinishReason)
	text, reasoningSummary, functionCalls := parseMessage(choice.Message)
	out.Text = text
	out.ReasoningSummary = reasoningSummary
	out.FunctionCalls = functionCalls
	return out
}

func parseMessage(message chatCompletionMessage) (string, string, []provideriface.FunctionCall) {
	text := extractTextContent(message.Content)
	reasoningSummary := strings.TrimSpace(message.ReasoningContent)
	calls := make([]provideriface.FunctionCall, 0, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			name = "tool"
		}
		arguments := strings.TrimSpace(call.Function.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		arguments = normalizeJSONArguments(arguments)
		arguments = normalizeJSONArguments(arguments)
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			callID = fmt.Sprintf("fireworks_call_%d", i+1)
		}
		calls = append(calls, provideriface.FunctionCall{
			CallID:    callID,
			Name:      name,
			Arguments: arguments,
		})
	}
	return strings.TrimSpace(text), reasoningSummary, calls
}

func parseUsage(usage *chatCompletionUsage) provideriface.TokenUsage {
	if usage == nil {
		return provideriface.TokenUsage{}
	}
	inputTokens := firstPositiveInt64(usage.PromptTokens, usage.InputTokens)
	outputTokens := firstPositiveInt64(usage.CompletionTokens, usage.OutputTokens)
	totalTokens := usage.TotalTokens
	if totalTokens <= 0 && (inputTokens > 0 || outputTokens > 0) {
		totalTokens = inputTokens + outputTokens
	}
	cachedTokens := int64(0)
	if usage.PromptTokensDetails != nil {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
	}
	if cachedTokens <= 0 && usage.InputTokenDetails != nil {
		cachedTokens = usage.InputTokenDetails.CachedTokens
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > inputTokens && inputTokens > 0 {
		cachedTokens = inputTokens
	}
	raw := map[string]any{
		"prompt_tokens":          usage.PromptTokens,
		"completion_tokens":      usage.CompletionTokens,
		"total_tokens":           usage.TotalTokens,
		"input_tokens":           usage.InputTokens,
		"output_tokens":          usage.OutputTokens,
		"cached_prompt_tokens":   cachedTokens,
		"uncached_prompt_tokens": inputTokens - cachedTokens,
	}
	return provideriface.TokenUsage{
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		CacheReadTokens: cachedTokens,
		TotalTokens:     totalTokens,
		Source:          "fireworks_api_usage",
		APIUsageRaw:     cloneMap(raw),
		APIUsageRawPath: "usage",
		APIUsageHistory: []map[string]any{cloneMap(raw)},
		APIUsagePaths:   []string{"usage"},
	}
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func annotateUsage(usage *provideriface.TokenUsage, serving requestServingResolution) {
	if usage == nil || strings.TrimSpace(usage.Source) == "" {
		return
	}
	if usage.APIUsageRaw == nil {
		usage.APIUsageRaw = map[string]any{}
	}
	if strings.TrimSpace(serving.EffectiveTier) != "" {
		usage.ServiceTier = strings.TrimSpace(serving.EffectiveTier)
		usage.APIUsageRaw["service_tier"] = usage.ServiceTier
	}
	if strings.TrimSpace(serving.ModelID) != "" {
		usage.APIUsageRaw["provider_model"] = strings.TrimSpace(serving.ModelID)
	}
	if cost := EstimateCostUSD(*usage, serving.ServingTier); cost > 0 {
		usage.EstimatedCostUSD = cost
		usage.APIUsageRaw["estimated_cost_usd"] = cost
	}
	usage.APIUsageHistory = []map[string]any{cloneMap(usage.APIUsageRaw)}
}

func extractTextContent(content any) string {
	switch typed := content.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := extractTextContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n\n"))
	case map[string]any:
		if text, ok := stringField(typed, "text"); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func extractMessageText(content any) string {
	switch typed := content.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []map[string]any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			itemType, _ := stringField(item, "type")
			if !strings.EqualFold(strings.TrimSpace(itemType), "input_text") && !strings.EqualFold(strings.TrimSpace(itemType), "output_text") {
				continue
			}
			text, _ := stringField(item, "text")
			if strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n\n"))
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if !ok {
				continue
			}
			itemType, _ := stringField(mapped, "type")
			if !strings.EqualFold(strings.TrimSpace(itemType), "input_text") && !strings.EqualFold(strings.TrimSpace(itemType), "output_text") {
				continue
			}
			text, _ := stringField(mapped, "text")
			if strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n\n"))
	default:
		return ""
	}
}

func stringField(input map[string]any, key string) (string, bool) {
	value, ok := input[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return text, true
}

func fireworksDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMD_FIREWORKS_DEBUG")))
	switch value {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func fireworksDebugf(format string, args ...any) {
	if !fireworksDebugEnabled() {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[swarmd.fireworks] "+format+"\n", args...)
}

func fireworksDebugEvent(event string, data map[string]any) {
	if !fireworksDebugEnabled() {
		return
	}
	clean := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"event": strings.TrimSpace(event),
		"data":  privacy.SanitizeMap(data),
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		fireworksDebugf("event=%s encode_error=true", strings.TrimSpace(event))
		return
	}
	fireworksDebugf("%s", string(encoded))
}

func fireworksDebugJSONValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"encode_error": err.Error()}
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{"decode_error": err.Error()}
	}
	return privacy.SanitizeValue(decoded)
}

func fireworksDebugChunkInteresting(chunk chatCompletionChunk) bool {
	for _, choice := range chunk.Choices {
		if choice.Delta != nil {
			if strings.TrimSpace(choice.Delta.Content) != "" || strings.TrimSpace(choice.Delta.ReasoningContent) != "" || len(choice.Delta.ToolCalls) > 0 {
				return true
			}
		}
		if len(choice.Message.ToolCalls) > 0 || strings.TrimSpace(choice.FinishReason) != "" {
			return true
		}
	}
	return false
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func normalizeJSONArguments(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		encoded, marshalErr := json.Marshal(map[string]any{"raw": raw})
		if marshalErr != nil {
			return "{}"
		}
		return string(encoded)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
