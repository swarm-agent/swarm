package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/identity"
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
	return "openrouter"
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return r.createResponse(ctx, req)
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	return r.createStreamingResponse(ctx, req, onEvent)
}

func (r *Runner) createResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r == nil || r.authStore == nil {
		return provideriface.Response{}, errors.New("openrouter runner auth store is not configured")
	}
	if r.client == nil {
		r.client = NewClient()
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return provideriface.Response{}, errors.New("model is required")
	}
	record, err := r.activeCredential(ctx)
	if err != nil {
		return provideriface.Response{}, err
	}
	payload := buildChatCompletionRequest(req)
	decoded, err := r.client.CreateChatCompletion(ctx, record.APIKey, payload)
	if err != nil {
		return provideriface.Response{}, err
	}
	result := parseChatCompletionResponse(decoded)
	if strings.TrimSpace(result.Model) == "" {
		result.Model = modelID
	}
	return result, nil
}

func (r *Runner) createStreamingResponse(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r == nil || r.authStore == nil {
		return provideriface.Response{}, errors.New("openrouter runner auth store is not configured")
	}
	if r.client == nil {
		r.client = NewClient()
	}
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		return provideriface.Response{}, errors.New("model is required")
	}
	record, err := r.activeCredential(ctx)
	if err != nil {
		return provideriface.Response{}, err
	}
	payload := buildChatCompletionRequest(req)
	toolState := newOpenRouterToolCallConstructionState()
	decoded, err := r.client.CreateChatCompletionStream(ctx, record.APIKey, payload, func(chunk chatCompletionChunk) error {
		for _, choice := range chunk.Choices {
			if choice.Delta != nil {
				if choice.Delta.Content != "" && onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: choice.Delta.Content})
				}
				if choice.Delta.Reasoning != "" && onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: choice.Delta.Reasoning, ReasoningKey: openRouterReasoningKey(choice.Index)})
				}
			}
		}
		emitOpenRouterToolCallConstructionEvents(toolState, chunk, onEvent)
		if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
			return errors.New(strings.TrimSpace(chunk.Error.Message))
		}
		return nil
	})
	if err != nil {
		return provideriface.Response{}, err
	}
	result := parseChatCompletionResponse(decoded)
	if strings.TrimSpace(result.Model) == "" {
		result.Model = modelID
	}
	return result, nil
}

func (r *Runner) activeCredential(ctx context.Context) (pebblestore.AuthCredentialRecord, error) {
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK {
		return pebblestore.AuthCredentialRecord{}, identity.ErrPrincipalRequired
	}
	record, ok, err := r.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "openrouter")
	if err != nil {
		return pebblestore.AuthCredentialRecord{}, fmt.Errorf("read openrouter auth: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return pebblestore.AuthCredentialRecord{}, errors.New("openrouter auth is not configured")
	}
	return record, nil
}

func buildChatCompletionRequest(req provideriface.Request) chatCompletionRequest {
	out := chatCompletionRequest{
		Model:       strings.TrimSpace(req.Model),
		Messages:    buildChatCompletionMessages(req),
		Reasoning:   openRouterReasoningForRequest(req),
		ServiceTier: openRouterServiceTierForRequest(req),
		SessionID:   openRouterSessionID(req),
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
					Parameters:  definition.Parameters,
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

func openRouterReasoningForRequest(req provideriface.Request) map[string]any {
	thinking := strings.ToLower(strings.TrimSpace(req.Thinking))
	if thinking == "" {
		return nil
	}
	catalog, ok := req.ModelCatalog.(pebblestore.ModelCatalogRecord)
	if !ok {
		return nil
	}
	for _, mapping := range catalog.ThinkingMappings {
		if !strings.EqualFold(strings.TrimSpace(mapping.SwarmSetting), thinking) {
			continue
		}
		providerValue := strings.TrimSpace(firstNonEmpty(mapping.EffectiveProviderValue, mapping.ProviderValue))
		if providerValue == "" {
			return nil
		}
		parameter := strings.ToLower(strings.TrimSpace(mapping.ProviderParameter))
		if strings.Contains(parameter, "max_tokens") {
			var tokens int
			if _, err := fmt.Sscanf(providerValue, "%d", &tokens); err == nil && tokens >= 0 {
				return map[string]any{"max_tokens": tokens}
			}
			return nil
		}
		if strings.Contains(parameter, "enabled") {
			enabled := !strings.EqualFold(providerValue, "false") && !strings.EqualFold(providerValue, "0") && !strings.EqualFold(providerValue, "none")
			return map[string]any{"enabled": enabled}
		}
		return map[string]any{"effort": providerValue}
	}
	return nil
}

func openRouterServiceTierForRequest(req provideriface.Request) string {
	requested := strings.ToLower(strings.TrimSpace(req.ServiceTier))
	if requested == "" {
		return ""
	}
	catalog, ok := req.ModelCatalog.(pebblestore.ModelCatalogRecord)
	if !ok {
		return ""
	}
	for _, mapping := range catalog.ServiceTierMappings {
		if !strings.EqualFold(strings.TrimSpace(mapping.SwarmSetting), requested) && !strings.EqualFold(strings.TrimSpace(mapping.Tier), requested) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(mapping.ProviderParameter), "service_tier") {
			continue
		}
		return strings.TrimSpace(mapping.ProviderValue)
	}
	return ""
}

func openRouterSessionID(req provideriface.Request) string {
	key := strings.TrimSpace(firstNonEmpty(req.SessionAffinityKey, req.ProviderCacheKey, req.ProviderLineageID))
	if key == "" {
		return ""
	}
	return "swarm-lineage-" + key
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
		"content":    nil,
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

func mapToolChoice(choice string) any {
	choice = strings.ToLower(strings.TrimSpace(choice))
	switch choice {
	case "", "auto":
		return "auto"
	case "none":
		return choice
	case "required":
		return "required"
	default:
		return "auto"
	}
}

type openRouterToolCallConstructionState struct {
	seenStarted   map[int]bool
	seenCompleted map[int]bool
	arguments     map[int]string
	ids           map[int]string
	names         map[int]string
}

func newOpenRouterToolCallConstructionState() *openRouterToolCallConstructionState {
	return &openRouterToolCallConstructionState{
		seenStarted:   make(map[int]bool),
		seenCompleted: make(map[int]bool),
		arguments:     make(map[int]string),
		ids:           make(map[int]string),
		names:         make(map[int]string),
	}
}

func emitOpenRouterToolCallConstructionEvents(state *openRouterToolCallConstructionState, chunk chatCompletionChunk, onEvent func(provideriface.StreamEvent)) {
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
				if name := strings.TrimSpace(openRouterToolCallDeltaName(delta)); name != "" {
					state.names[index] = name
				}
				if !state.seenStarted[index] {
					state.seenStarted[index] = true
					onEvent(provideriface.StreamEvent{
						Type:          provideriface.StreamEventToolCallStarted,
						ToolCallID:    state.ids[index],
						ToolCallIndex: openRouterIntPointer(index),
						ToolName:      state.names[index],
						Metadata:      openRouterToolCallDeltaMetadata(choice.Index, delta, "openrouter.chat.completions.chunk.delta"),
					})
				}
				if delta.Function != nil && delta.Function.Arguments != "" {
					state.arguments[index] += delta.Function.Arguments
					onEvent(provideriface.StreamEvent{
						Type:           provideriface.StreamEventToolCallArgumentsDelta,
						Delta:          delta.Function.Arguments,
						ToolCallID:     state.ids[index],
						ToolCallIndex:  openRouterIntPointer(index),
						ToolName:       state.names[index],
						ArgumentsDelta: delta.Function.Arguments,
						Metadata:       openRouterToolCallDeltaMetadata(choice.Index, delta, "openrouter.chat.completions.chunk.delta"),
					})
				}
			}
		}
		if strings.EqualFold(strings.TrimSpace(choice.FinishReason), "tool_calls") {
			emitCompletedOpenRouterToolCallConstructionEvents(state, choice.Index, onEvent)
		}
	}
}

func emitCompletedOpenRouterToolCallConstructionEvents(state *openRouterToolCallConstructionState, choiceIndex int, onEvent func(provideriface.StreamEvent)) {
	for index := range state.seenStarted {
		if state.seenCompleted[index] {
			continue
		}
		state.seenCompleted[index] = true
		onEvent(provideriface.StreamEvent{
			Type:          provideriface.StreamEventToolCallCompleted,
			ToolCallID:    state.ids[index],
			ToolCallIndex: openRouterIntPointer(index),
			ToolName:      state.names[index],
			Arguments:     strings.TrimSpace(state.arguments[index]),
			Metadata: map[string]any{
				"provider":     "openrouter",
				"source":       "openrouter.chat.completions.chunk.finish",
				"choice_index": choiceIndex,
			},
		})
	}
}

func openRouterToolCallDeltaName(delta chatCompletionToolCallDelta) string {
	if delta.Function == nil {
		return ""
	}
	return strings.TrimSpace(delta.Function.Name)
}

func openRouterToolCallDeltaMetadata(choiceIndex int, delta chatCompletionToolCallDelta, source string) map[string]any {
	metadata := map[string]any{
		"provider":     "openrouter",
		"source":       source,
		"choice_index": choiceIndex,
	}
	if typeName := strings.TrimSpace(delta.Type); typeName != "" {
		metadata["tool_call_type"] = typeName
	}
	return metadata
}

func openRouterReasoningKey(index int) string {
	if index < 0 {
		return "openrouter-reasoning"
	}
	return fmt.Sprintf("openrouter-reasoning-%d", index)
}

func openRouterIntPointer(value int) *int {
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
	reasoningSummary := strings.TrimSpace(message.Reasoning)
	if reasoningSummary == "" {
		reasoningSummary = extractOpenRouterReasoningDetails(message.ReasoningDetails)
	}
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
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			callID = fmt.Sprintf("openrouter_call_%d", i+1)
		}
		calls = append(calls, provideriface.FunctionCall{
			CallID:    callID,
			Name:      name,
			Arguments: arguments,
		})
	}
	return strings.TrimSpace(text), reasoningSummary, calls
}

func extractOpenRouterReasoningDetails(details []map[string]any) string {
	if len(details) == 0 {
		return ""
	}
	parts := make([]string, 0, len(details))
	for _, detail := range details {
		for _, key := range []string{"text", "reasoning", "summary"} {
			if value, ok := stringField(detail, key); ok && strings.TrimSpace(value) != "" {
				parts = append(parts, strings.TrimSpace(value))
				break
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func parseUsage(usage *chatCompletionUsage) provideriface.TokenUsage {
	if usage == nil {
		return provideriface.TokenUsage{}
	}
	cacheReadTokens := int64(0)
	cacheWriteTokens := int64(0)
	if usage.PromptTokensDetails != nil {
		cacheReadTokens = usage.PromptTokensDetails.CachedTokens
		cacheWriteTokens = usage.PromptTokensDetails.CacheWriteTokens
	}
	thinkingTokens := int64(0)
	if usage.CompletionTokensDetails != nil {
		thinkingTokens = usage.CompletionTokensDetails.ReasoningTokens
	}
	if cacheReadTokens < 0 {
		cacheReadTokens = 0
	}
	if cacheWriteTokens < 0 {
		cacheWriteTokens = 0
	}
	if thinkingTokens < 0 {
		thinkingTokens = 0
	}
	raw := openRouterUsageRaw(usage)
	out := provideriface.TokenUsage{
		InputTokens:      usage.PromptTokens,
		OutputTokens:     usage.CompletionTokens,
		ThinkingTokens:   thinkingTokens,
		TotalTokens:      usage.TotalTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		ServiceTier:      strings.TrimSpace(usage.ServiceTier),
		Source:           "openrouter_api_usage",
		APIUsageRaw:      cloneMap(raw),
		APIUsageRawPath:  "usage",
		APIUsageHistory:  []map[string]any{cloneMap(raw)},
		APIUsagePaths:    []string{"usage"},
	}
	return out
}

func openRouterUsageRaw(usage *chatCompletionUsage) map[string]any {
	raw := map[string]any{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}
	if usage.PromptTokensDetails != nil {
		raw["prompt_tokens_details"] = map[string]any{
			"cached_tokens":      usage.PromptTokensDetails.CachedTokens,
			"cache_write_tokens": usage.PromptTokensDetails.CacheWriteTokens,
			"audio_tokens":       usage.PromptTokensDetails.AudioTokens,
			"video_tokens":       usage.PromptTokensDetails.VideoTokens,
		}
	}
	if usage.CompletionTokensDetails != nil {
		raw["completion_tokens_details"] = map[string]any{
			"reasoning_tokens":           usage.CompletionTokensDetails.ReasoningTokens,
			"audio_tokens":               usage.CompletionTokensDetails.AudioTokens,
			"accepted_prediction_tokens": usage.CompletionTokensDetails.AcceptedPredictionTokens,
			"rejected_prediction_tokens": usage.CompletionTokensDetails.RejectedPredictionTokens,
		}
	}
	if usage.Cost != 0 {
		raw["cost"] = usage.Cost
	}
	if len(usage.CostDetails) > 0 {
		raw["cost_details"] = cloneMap(usage.CostDetails)
	}
	if strings.TrimSpace(usage.ServiceTier) != "" {
		raw["service_tier"] = strings.TrimSpace(usage.ServiceTier)
	}
	return raw
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
			if !strings.EqualFold(strings.TrimSpace(itemType), "input_text") && !strings.EqualFold(strings.TrimSpace(itemType), "output_text") && !strings.EqualFold(strings.TrimSpace(itemType), "text") {
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
			if !strings.EqualFold(strings.TrimSpace(itemType), "input_text") && !strings.EqualFold(strings.TrimSpace(itemType), "output_text") && !strings.EqualFold(strings.TrimSpace(itemType), "text") {
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
