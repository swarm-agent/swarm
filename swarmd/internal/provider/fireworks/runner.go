package fireworks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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

func (r *Runner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode:                provideriface.ExecutionEpochContextStatelessFullInput,
		EpochScopedCacheKey:        true,
		EpochScopedSessionAffinity: true,
	}
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
	if err := validateFireworksMediaContractForCredential(req.MediaContract, record); err != nil {
		return provideriface.Response{}, err
	}
	payload, err := buildChatCompletionRequest(req)
	if err != nil {
		return provideriface.Response{}, err
	}
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
		"message_count":    len(payload.Messages),
		"tool_count":       len(payload.Tools),
	})
	decoded, err := r.client.CreateChatCompletion(ctx, record.APIKey, payload, requestOptions{SessionAffinity: serving.SessionAffinity, PromptCacheIsolationKey: serving.PromptCacheIsolationKey})
	if err != nil {
		return provideriface.Response{}, err
	}
	result := parseChatCompletionResponse(decoded)
	annotateUsage(&result.Usage, serving)
	fireworksDebugEvent("response", map[string]any{
		"transport":           "sync",
		"session_id":          req.SessionID,
		"model":               modelID,
		"choice_count":        len(decoded.Choices),
		"function_call_count": len(result.FunctionCalls),
		"response_text_runes": len([]rune(result.Text)),
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
	if err := validateFireworksMediaContractForCredential(req.MediaContract, record); err != nil {
		return provideriface.Response{}, err
	}
	payload, err := buildChatCompletionRequest(req)
	if err != nil {
		return provideriface.Response{}, err
	}
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
		"message_count":    len(payload.Messages),
		"tool_count":       len(payload.Tools),
	})
	reasoningByKey := make(map[string]string, 4)
	toolConstruction := newFireworksToolCallConstructionState(payload.Model)
	decoded, err := r.client.CreateChatCompletionStream(ctx, record.APIKey, payload, func(chunk chatCompletionChunk) error {
		if fireworksDebugChunkInteresting(chunk) {
			fireworksDebugEvent("stream_chunk", fireworksDebugChunkMetadata(req.SessionID, modelID, chunk))
		}
		for _, choice := range chunk.Choices {
			if choice.Delta == nil || onEvent == nil {
				continue
			}
			if choice.Delta.ReasoningContent != "" {
				reasoningKey := fireworksReasoningKey(choice.Index)
				reasoningByKey[reasoningKey] += choice.Delta.ReasoningContent
				emitFireworksReasoningSnapshot(onEvent, reasoningKey, reasoningByKey[reasoningKey])
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
		"transport":           "stream",
		"session_id":          req.SessionID,
		"model":               modelID,
		"choice_count":        len(decoded.Choices),
		"function_call_count": len(result.FunctionCalls),
		"response_text_runes": len([]rune(result.Text)),
	})
	if strings.TrimSpace(result.Model) == "" {
		result.Model = modelID
	}
	return result, nil
}

func emitFireworksReasoningSnapshot(onEvent func(provideriface.StreamEvent), reasoningKey, snapshot string) {
	if onEvent == nil || snapshot == "" {
		return
	}
	onEvent(provideriface.StreamEvent{
		Type:         provideriface.StreamEventReasoningSummaryDelta,
		Delta:        snapshot,
		DeltaMode:    provideriface.StreamEventDeltaModeReplace,
		ReasoningKey: reasoningKey,
	})
}

func validateFireworksMediaContractForCredential(contract provideriface.SessionMediaContract, record pebblestore.AuthCredentialRecord) error {
	if err := validateFireworksMediaSurface(contract); err != nil {
		return err
	}
	if strings.TrimSpace(contract.Hash) == "" {
		return nil
	}
	activeFingerprint := fireworksCredentialFingerprint(record.AccountScopeID, record.Provider, record.ID, record.Type)
	if contract.CredentialFingerprint != activeFingerprint {
		return errors.New("media contract does not match the active Fireworks credential")
	}
	return nil
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

func buildChatCompletionRequest(req provideriface.Request) (chatCompletionRequest, error) {
	messages, err := buildChatCompletionMessages(req)
	if err != nil {
		return chatCompletionRequest{}, err
	}
	out := chatCompletionRequest{
		Model:           strings.TrimSpace(req.Model),
		Messages:        messages,
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
	return out, nil
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

func buildChatCompletionMessages(req provideriface.Request) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, len(req.Input)+1)
	media := fireworksMediaRequestState{}
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
			case "message", "":
				// Continue through normal message handling.
			default:
				return nil, errors.New("fireworks input contains an unsupported item type")
			}
		}
		role, rolePresent := stringField(item, "role")
		if !rolePresent && item["role"] != nil {
			return nil, errors.New("fireworks message role is malformed")
		}
		mappedRole, err := mapFireworksMessageRole(role)
		if err != nil {
			return nil, err
		}
		content, present, err := materializeFireworksMessageContent(req, mappedRole, item["content"], &media)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		messages = append(messages, map[string]any{
			"role":    mappedRole,
			"content": content,
		})
	}
	return messages, nil
}

func mapFireworksMessageRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "user":
		return "user", nil
	case "assistant":
		return "assistant", nil
	default:
		return "", errors.New("fireworks message role is not implemented")
	}
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
	if properties, ok := input["properties"].(map[string]any); ok {
		out["properties"] = sanitizeFireworksSchemaMap(properties, true)
	}
	properties, _ := out["properties"].(map[string]any)
	for key, value := range input {
		if value == nil {
			continue
		}
		switch key {
		case "properties":
			continue
		case "required":
			if !keepRequired {
				if required := sanitizeFireworksRequired(value); len(required) > 0 {
					out[key] = required
				}
			}
		case "allOf", "anyOf", "oneOf":
			out[key] = sanitizeFireworksSchemaAlternatives(value, properties)
		default:
			out[key] = sanitizeFireworksSchemaValue(value, false)
		}
	}
	return out
}

func sanitizeFireworksSchemaAlternatives(value any, inheritedProperties map[string]any) any {
	sanitizeAlternative := func(schema map[string]any) map[string]any {
		out := sanitizeFireworksSchemaMap(schema, false)
		required := sanitizeFireworksRequired(out["required"])
		if len(required) == 0 {
			return out
		}
		properties, _ := out["properties"].(map[string]any)
		if properties == nil {
			properties = make(map[string]any, len(required))
		}
		validRequired := make([]string, 0, len(required))
		for _, name := range required {
			if _, ok := properties[name]; !ok {
				if inherited, ok := inheritedProperties[name]; ok {
					properties[name] = sanitizeFireworksSchemaValue(inherited, false)
				}
			}
			if _, ok := properties[name]; ok {
				validRequired = append(validRequired, name)
			}
		}
		if len(validRequired) == 0 {
			delete(out, "required")
			return out
		}
		out["type"] = "object"
		out["properties"] = properties
		out["required"] = validRequired
		return out
	}

	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if schema, ok := item.(map[string]any); ok {
				out = append(out, sanitizeAlternative(schema))
				continue
			}
			out = append(out, sanitizeFireworksSchemaValue(item, false))
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, schema := range typed {
			out = append(out, sanitizeAlternative(schema))
		}
		return out
	default:
		return sanitizeFireworksSchemaValue(value, false)
	}
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

type fireworksToolCallKey struct {
	choiceIndex int
	toolIndex   int
}

type fireworksToolCallConstruction struct {
	key              fireworksToolCallKey
	outputIndex      int
	id               string
	name             string
	typeName         string
	arguments        string
	pendingArguments []string
	started          bool
	completed        bool
	startedAtUnixMs  int64
	recordedAtUnixMs int64
}

type fireworksToolCallConstructionState struct {
	providerID    string
	model         string
	responseID    string
	providerModel string
	calls         map[fireworksToolCallKey]*fireworksToolCallConstruction
	callsByChoice map[int][]fireworksToolCallKey
	nextOutput    int
}

func newFireworksToolCallConstructionState(models ...string) *fireworksToolCallConstructionState {
	model := ""
	if len(models) > 0 {
		model = strings.TrimSpace(models[0])
	}
	return &fireworksToolCallConstructionState{
		providerID:    "fireworks",
		model:         model,
		calls:         make(map[fireworksToolCallKey]*fireworksToolCallConstruction),
		callsByChoice: make(map[int][]fireworksToolCallKey),
	}
}

func emitFireworksToolCallConstructionEvents(state *fireworksToolCallConstructionState, chunk chatCompletionChunk, onEvent func(provideriface.StreamEvent)) {
	if state == nil || onEvent == nil || len(chunk.Choices) == 0 {
		return
	}
	if responseID := strings.TrimSpace(chunk.ID); responseID != "" {
		state.responseID = responseID
	}
	if providerModel := strings.TrimSpace(chunk.Model); providerModel != "" {
		state.providerModel = providerModel
	}
	for _, choice := range chunk.Choices {
		if choice.Delta != nil {
			for _, delta := range choice.Delta.ToolCalls {
				call := state.call(choice.Index, delta.Index)
				if id := strings.TrimSpace(delta.ID); id != "" {
					call.id = id
				}
				if name := fireworksToolCallDeltaName(delta); name != "" {
					call.name = name
				}
				if typeName := strings.TrimSpace(delta.Type); typeName != "" {
					call.typeName = typeName
				}
				if delta.Function != nil && delta.Function.Arguments != "" {
					call.arguments += delta.Function.Arguments
					call.pendingArguments = append(call.pendingArguments, delta.Function.Arguments)
				}
				if !call.started && (call.id != "" || call.name != "") {
					state.startAndFlush(call, "fireworks.chat.completions.chunk.delta", onEvent)
				} else if call.started {
					state.flushArguments(call, "fireworks.chat.completions.chunk.delta", onEvent)
				}
			}
		}
		if strings.EqualFold(strings.TrimSpace(choice.FinishReason), "tool_calls") {
			emitCompletedFireworksToolCallConstructionEvents(state, choice.Index, choice.FinishReason, onEvent)
		}
	}
}

func (state *fireworksToolCallConstructionState) call(choiceIndex, toolIndex int) *fireworksToolCallConstruction {
	key := fireworksToolCallKey{choiceIndex: choiceIndex, toolIndex: toolIndex}
	if call := state.calls[key]; call != nil && !call.completed {
		return call
	}
	call := &fireworksToolCallConstruction{key: key, outputIndex: state.nextOutput}
	state.nextOutput++
	state.calls[key] = call
	state.callsByChoice[choiceIndex] = append(state.callsByChoice[choiceIndex], key)
	return call
}

func (state *fireworksToolCallConstructionState) startAndFlush(call *fireworksToolCallConstruction, source string, onEvent func(provideriface.StreamEvent)) {
	if call == nil || call.started || onEvent == nil {
		return
	}
	call.started = true
	onEvent(state.event(call, provideriface.StreamEventToolCallStarted, source, ""))
	state.flushArguments(call, source, onEvent)
}

func (state *fireworksToolCallConstructionState) flushArguments(call *fireworksToolCallConstruction, source string, onEvent func(provideriface.StreamEvent)) {
	if call == nil || !call.started || onEvent == nil {
		return
	}
	for _, arguments := range call.pendingArguments {
		event := state.event(call, provideriface.StreamEventToolCallArgumentsDelta, source, "")
		event.Delta = arguments
		event.ArgumentsDelta = arguments
		onEvent(event)
	}
	call.pendingArguments = call.pendingArguments[:0]
}

func (state *fireworksToolCallConstructionState) event(call *fireworksToolCallConstruction, eventType provideriface.StreamEventType, source, finishReason string) provideriface.StreamEvent {
	now := time.Now().UnixMilli()
	if now <= call.recordedAtUnixMs {
		now = call.recordedAtUnixMs + 1
	}
	call.recordedAtUnixMs = now
	if call.startedAtUnixMs == 0 {
		call.startedAtUnixMs = now
	}
	status := "building"
	if eventType == provideriface.StreamEventToolCallStarted {
		status = "started"
	} else if eventType == provideriface.StreamEventToolCallCompleted {
		status = "completed"
	}
	metadata := map[string]any{
		"provider":               state.providerID,
		"source":                 source,
		"choice_index":           call.key.choiceIndex,
		"native_tool_call_index": call.key.toolIndex,
	}
	if state.responseID != "" {
		metadata["response_id"] = state.responseID
	}
	if state.providerModel != "" {
		metadata["provider_model"] = state.providerModel
	}
	if call.typeName != "" {
		metadata["tool_call_type"] = call.typeName
	}
	if finishReason = strings.TrimSpace(finishReason); finishReason != "" {
		metadata["finish_reason"] = finishReason
	}
	return provideriface.StreamEvent{
		Type:             eventType,
		ToolCallID:       call.id,
		ToolCallIndex:    intPointer(call.outputIndex),
		ToolName:         call.name,
		ProviderID:       state.providerID,
		Model:            state.model,
		RecordedAtUnixMs: now,
		StartedAtUnixMs:  call.startedAtUnixMs,
		Status:           status,
		Metadata:         metadata,
	}
}

func emitCompletedFireworksToolCallConstructionEvents(state *fireworksToolCallConstructionState, choiceIndex int, finishReason string, onEvent func(provideriface.StreamEvent)) {
	keys := append([]fireworksToolCallKey(nil), state.callsByChoice[choiceIndex]...)
	state.callsByChoice[choiceIndex] = nil
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].toolIndex != keys[j].toolIndex {
			return keys[i].toolIndex < keys[j].toolIndex
		}
		return state.calls[keys[i]].outputIndex < state.calls[keys[j]].outputIndex
	})
	for _, key := range keys {
		call := state.calls[key]
		if call == nil || call.completed {
			continue
		}
		if !call.started {
			state.startAndFlush(call, "fireworks.chat.completions.chunk.finish", onEvent)
		} else {
			state.flushArguments(call, "fireworks.chat.completions.chunk.delta", onEvent)
		}
		call.completed = true
		event := state.event(call, provideriface.StreamEventToolCallCompleted, "fireworks.chat.completions.chunk.finish", finishReason)
		event.Arguments = call.arguments
		onEvent(event)
	}
}

func fireworksToolCallDeltaName(delta chatCompletionToolCallDelta) string {
	if delta.Function == nil {
		return ""
	}
	return strings.TrimSpace(delta.Function.Name)
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
	// Fireworks does not return an authoritative served-tier field on its
	// Chat Completions response. Keep request resolution separate from provider
	// usage so durable logs do not mislabel a requested tier as provider-confirmed.
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

func fireworksDebugChunkMetadata(sessionID, modelID string, chunk chatCompletionChunk) map[string]any {
	contentRunes := 0
	reasoningRunes := 0
	toolCallCount := 0
	finishedChoices := 0
	for _, choice := range chunk.Choices {
		if choice.Delta != nil {
			contentRunes += len([]rune(choice.Delta.Content))
			reasoningRunes += len([]rune(choice.Delta.ReasoningContent))
			toolCallCount += len(choice.Delta.ToolCalls)
		}
		toolCallCount += len(choice.Message.ToolCalls)
		if strings.TrimSpace(choice.FinishReason) != "" {
			finishedChoices++
		}
	}
	return map[string]any{
		"session_id":       sessionID,
		"model":            modelID,
		"choice_count":     len(chunk.Choices),
		"content_runes":    contentRunes,
		"reasoning_runes":  reasoningRunes,
		"tool_call_count":  toolCallCount,
		"finished_choices": finishedChoices,
	}
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
