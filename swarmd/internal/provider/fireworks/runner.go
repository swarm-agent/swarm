package fireworks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

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
	record, ok, err := r.authStore.GetActiveCredential("fireworks")
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("read fireworks auth: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return provideriface.Response{}, errors.New("fireworks auth is not configured")
	}
	payload := buildChatCompletionRequest(req)
	fireworksDebugEvent("request", map[string]any{
		"transport":  "sync",
		"session_id": req.SessionID,
		"model":      modelID,
		"payload":    fireworksDebugJSONValue(payload),
	})
	decoded, err := r.client.CreateChatCompletion(ctx, record.APIKey, payload)
	if err != nil {
		return provideriface.Response{}, err
	}
	result := parseChatCompletionResponse(decoded)
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
	record, ok, err := r.authStore.GetActiveCredential("fireworks")
	if err != nil {
		return provideriface.Response{}, fmt.Errorf("read fireworks auth: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return provideriface.Response{}, errors.New("fireworks auth is not configured")
	}
	payload := buildChatCompletionRequest(req)
	fireworksDebugEvent("request", map[string]any{
		"transport":  "stream",
		"session_id": req.SessionID,
		"model":      modelID,
		"payload":    fireworksDebugJSONValue(payload),
	})
	decoded, err := r.client.CreateChatCompletionStream(ctx, record.APIKey, payload, func(chunk chatCompletionChunk) error {
		if fireworksDebugChunkInteresting(chunk) {
			fireworksDebugEvent("stream_chunk", map[string]any{
				"session_id": req.SessionID,
				"model":      modelID,
				"chunk":      fireworksDebugJSONValue(chunk),
			})
		}
		for _, choice := range chunk.Choices {
			if choice.Delta != nil && choice.Delta.Content != "" && onEvent != nil {
				onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: choice.Delta.Content})
			}
		}
		return nil
	})
	if err != nil {
		return provideriface.Response{}, err
	}
	result := parseChatCompletionResponse(decoded)
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

func buildChatCompletionRequest(req provideriface.Request) chatCompletionRequest {
	out := chatCompletionRequest{
		Model:    strings.TrimSpace(req.Model),
		Messages: buildChatCompletionMessages(req),
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
	text, functionCalls := parseMessage(choice.Message)
	out.Text = text
	out.FunctionCalls = functionCalls
	return out
}

func parseMessage(message chatCompletionMessage) (string, []provideriface.FunctionCall) {
	text := extractTextContent(message.Content)
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
	return strings.TrimSpace(text), calls
}

func parseUsage(usage *chatCompletionUsage) provideriface.TokenUsage {
	if usage == nil {
		return provideriface.TokenUsage{}
	}
	raw := map[string]any{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}
	return provideriface.TokenUsage{
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		TotalTokens:     usage.TotalTokens,
		Source:          "fireworks_api_usage",
		APIUsageRaw:     cloneMap(raw),
		APIUsageRawPath: "usage",
		APIUsageHistory: []map[string]any{cloneMap(raw)},
		APIUsagePaths:   []string{"usage"},
	}
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
			if strings.TrimSpace(choice.Delta.Content) != "" || len(choice.Delta.ToolCalls) > 0 {
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
