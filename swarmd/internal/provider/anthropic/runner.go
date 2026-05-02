package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	promptCachingBeta = anthropicapi.AnthropicBetaPromptCaching2024_07_31
	usageSource       = "anthropic_api_usage"
)

type Runner struct {
	authStore *pebblestore.AuthStore
}

func NewRunner(authStore *pebblestore.AuthStore) *Runner {
	return &Runner{authStore: authStore}
}

func (r *Runner) ID() string {
	return "anthropic"
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r == nil || r.authStore == nil {
		return provideriface.Response{}, errors.New("anthropic runner auth store is not configured")
	}
	client, modelName, params, err := r.buildRequest(req)
	if err != nil {
		return provideriface.Response{}, err
	}
	message, err := client.Messages.New(ctx, params)
	if err != nil {
		return provideriface.Response{}, err
	}
	response := anthropicMessageToResponse(*message)
	if strings.TrimSpace(response.Model) == "" {
		response.Model = modelName
	}
	return response, nil
}

func (r *Runner) CreateResponseStreaming(ctx context.Context, req provideriface.Request, onEvent func(provideriface.StreamEvent)) (provideriface.Response, error) {
	if r == nil || r.authStore == nil {
		return provideriface.Response{}, errors.New("anthropic runner auth store is not configured")
	}
	client, modelName, params, err := r.buildRequest(req)
	if err != nil {
		return provideriface.Response{}, err
	}
	stream := client.Messages.NewStreaming(ctx, params)
	message := anthropicapi.Message{}
	var textBuilder strings.Builder
	var thinkingBuilder strings.Builder
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return provideriface.Response{}, fmt.Errorf("accumulate anthropic stream: %w", err)
		}
		if onEvent != nil {
			switch variant := event.AsAny().(type) {
			case anthropicapi.ContentBlockDeltaEvent:
				switch delta := variant.Delta.AsAny().(type) {
				case anthropicapi.TextDelta:
					if strings.TrimSpace(delta.Text) != "" {
						textBuilder.WriteString(delta.Text)
						onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: delta.Text})
					}
				case anthropicapi.ThinkingDelta:
					if strings.TrimSpace(delta.Thinking) != "" {
						thinkingBuilder.WriteString(delta.Thinking)
						onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: delta.Thinking})
					}
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return provideriface.Response{}, err
	}
	response := anthropicMessageToResponse(message)
	if strings.TrimSpace(response.Model) == "" {
		response.Model = modelName
	}
	if strings.TrimSpace(response.Text) == "" {
		response.Text = strings.TrimSpace(textBuilder.String())
	}
	if strings.TrimSpace(response.ReasoningSummary) == "" {
		response.ReasoningSummary = strings.TrimSpace(thinkingBuilder.String())
	}
	return response, nil
}

func (r *Runner) buildRequest(req provideriface.Request) (anthropicapi.Client, string, anthropicapi.MessageNewParams, error) {
	record, ok, err := r.authStore.GetActiveCredential("anthropic")
	if err != nil {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, fmt.Errorf("read anthropic auth: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, errors.New("anthropic auth is not configured")
	}
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, errors.New("model is required")
	}
	messages, err := buildAnthropicMessages(req.Input)
	if err != nil {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, err
	}
	tools, enablePromptCaching, err := buildAnthropicTools(req.Tools)
	if err != nil {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, err
	}
	system := buildAnthropicSystem(req.Instructions)
	if len(system) > 0 {
		enablePromptCaching = true
	}
	params := anthropicapi.MessageNewParams{
		Model:     anthropicapi.Model(modelName),
		MaxTokens: 16384,
		Messages:  messages,
		System:    system,
		Tools:     tools,
	}
	if thinking := anthropicThinkingConfig(req.Thinking); thinking != nil {
		params.Thinking = *thinking
	}
	if toolChoice := anthropicToolChoice(req.ToolChoice, req.ParallelToolCalls); toolChoice != nil {
		params.ToolChoice = *toolChoice
	}
	if serviceTier := anthropicServiceTier(req.ServiceTier); serviceTier != "" {
		params.ServiceTier = serviceTier
	}
	if enablePromptCaching {
		applyAnthropicPromptCaching(&params, tools)
	}
	client := anthropicapi.NewClient(anthropicClientOptions(strings.TrimSpace(record.APIKey))...)
	return client, modelName, params, nil
}

func anthropicClientOptions(apiKey string) []option.RequestOption {
	return []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHeaderAdd("anthropic-beta", string(promptCachingBeta)),
	}
}

func applyAnthropicPromptCaching(params *anthropicapi.MessageNewParams, tools []anthropicapi.ToolUnionParam) {
	if params == nil {
		return
	}
	params.CacheControl = newEphemeralCacheControl()
	if len(tools) == 0 {
		return
	}
	lastTool := tools[len(tools)-1].OfTool
	if lastTool != nil {
		lastTool.CacheControl = newEphemeralCacheControl()
	}
}

func buildAnthropicSystem(instructions string) []anthropicapi.TextBlockParam {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return nil
	}
	return []anthropicapi.TextBlockParam{{Text: instructions}}
}

func buildAnthropicTools(definitions []provideriface.ToolDefinition) ([]anthropicapi.ToolUnionParam, bool, error) {
	if len(definitions) == 0 {
		return nil, false, nil
	}
	tools := make([]anthropicapi.ToolUnionParam, 0, len(definitions))
	enablePromptCaching := false
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		schema, err := sanitizeAnthropicToolSchema(definition.Parameters)
		if err != nil {
			return nil, false, fmt.Errorf("sanitize anthropic tool schema %q: %w", name, err)
		}
		tool := anthropicapi.ToolParam{
			Name:        name,
			Description: anthropicapi.String(strings.TrimSpace(definition.Description)),
			InputSchema: schema,
			Type:        anthropicapi.ToolTypeCustom,
		}
		tools = append(tools, anthropicapi.ToolUnionParam{OfTool: &tool})
		enablePromptCaching = true
	}
	return tools, enablePromptCaching, nil
}

func sanitizeAnthropicToolSchema(parameters map[string]any) (anthropicapi.ToolInputSchemaParam, error) {
	// The non-beta Messages API uses ToolInputSchemaParam, while the SDK's
	// exported compatibility transformer is BetaToolInputSchema. The beta and
	// non-beta schema params have the same JSON shape, so marshal the official
	// SDK-transformed schema back to a map and attach it as non-beta ExtraFields.
	transformed := anthropicapi.BetaToolInputSchema(parameters)
	encoded, err := json.Marshal(transformed)
	if err != nil {
		return anthropicapi.ToolInputSchemaParam{}, err
	}
	var fullSchema map[string]any
	if err := json.Unmarshal(encoded, &fullSchema); err != nil {
		return anthropicapi.ToolInputSchemaParam{}, err
	}
	return anthropicapi.ToolInputSchemaParam{ExtraFields: fullSchema}, nil
}

func buildAnthropicMessages(input []map[string]any) ([]anthropicapi.MessageParam, error) {
	messages := make([]anthropicapi.MessageParam, 0, len(input))
	for i := 0; i < len(input); i++ {
		item := input[i]
		if typeName, ok := stringField(item, "type"); ok {
			switch strings.ToLower(strings.TrimSpace(typeName)) {
			case "function_call":
				blocks := make([]anthropicapi.ContentBlockParamUnion, 0, 4)
				for ; i < len(input); i++ {
					current := input[i]
					currentType, _ := stringField(current, "type")
					if !strings.EqualFold(strings.TrimSpace(currentType), "function_call") {
						i--
						break
					}
					callID, _ := stringField(current, "call_id")
					name, _ := stringField(current, "name")
					arguments, _ := stringField(current, "arguments")
					block := anthropicapi.ToolUseBlockParam{
						ID:    strings.TrimSpace(callID),
						Name:  firstNonEmpty(strings.TrimSpace(name), "tool"),
						Input: parseJSONValue(arguments),
					}
					blocks = append(blocks, anthropicapi.ContentBlockParamUnion{OfToolUse: &block})
				}
				if len(blocks) > 0 {
					messages = append(messages, anthropicapi.NewAssistantMessage(blocks...))
				}
			case "function_call_output":
				blocks := make([]anthropicapi.ContentBlockParamUnion, 0, 4)
				for ; i < len(input); i++ {
					current := input[i]
					currentType, _ := stringField(current, "type")
					if !strings.EqualFold(strings.TrimSpace(currentType), "function_call_output") {
						i--
						break
					}
					callID, _ := stringField(current, "call_id")
					output, _ := stringField(current, "output")
					block := anthropicapi.ToolResultBlockParam{
						ToolUseID: strings.TrimSpace(callID),
						Content:   []anthropicapi.ToolResultBlockParamContentUnion{{OfText: &anthropicapi.TextBlockParam{Text: strings.TrimSpace(output)}}},
					}
					if payload := decodeJSONMap(output); payload != nil {
						if errText := strings.TrimSpace(stringFieldDefault(payload, "error")); errText != "" {
							block.IsError = anthropicapi.Bool(true)
						}
					}
					blocks = append(blocks, anthropicapi.ContentBlockParamUnion{OfToolResult: &block})
				}
				if len(blocks) > 0 {
					messages = append(messages, anthropicapi.NewUserMessage(blocks...))
				}
			}
			continue
		}
		role, _ := stringField(item, "role")
		blocks, err := buildAnthropicContentBlocks(item["content"])
		if err != nil {
			return nil, err
		}
		if len(blocks) == 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(role), "assistant") {
			messages = append(messages, anthropicapi.NewAssistantMessage(blocks...))
		} else {
			messages = append(messages, anthropicapi.NewUserMessage(blocks...))
		}
	}
	return messages, nil
}

func buildAnthropicContentBlocks(content any) ([]anthropicapi.ContentBlockParamUnion, error) {
	switch typed := content.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, nil
		}
		return []anthropicapi.ContentBlockParamUnion{{OfText: &anthropicapi.TextBlockParam{Text: text}}}, nil
	case []map[string]any:
		return buildAnthropicContentBlocksFromMaps(typed)
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if !ok {
				continue
			}
			items = append(items, mapped)
		}
		return buildAnthropicContentBlocksFromMaps(items)
	default:
		return nil, nil
	}
}

func buildAnthropicContentBlocksFromMaps(items []map[string]any) ([]anthropicapi.ContentBlockParamUnion, error) {
	blocks := make([]anthropicapi.ContentBlockParamUnion, 0, len(items))
	for _, item := range items {
		itemType, _ := stringField(item, "type")
		switch strings.ToLower(strings.TrimSpace(itemType)) {
		case "input_text", "output_text", "text":
			text, _ := stringField(item, "text")
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			blocks = append(blocks, anthropicapi.ContentBlockParamUnion{OfText: &anthropicapi.TextBlockParam{Text: text}})
		default:
			if text, _ := stringField(item, "text"); strings.TrimSpace(text) != "" {
				blocks = append(blocks, anthropicapi.ContentBlockParamUnion{OfText: &anthropicapi.TextBlockParam{Text: strings.TrimSpace(text)}})
			}
		}
	}
	return blocks, nil
}

func anthropicThinkingConfig(level string) *anthropicapi.ThinkingConfigParamUnion {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "off":
		cfg := anthropicapi.ThinkingConfigParamUnion{OfDisabled: &anthropicapi.ThinkingConfigDisabledParam{}}
		return &cfg
	case "low":
		cfg := anthropicapi.ThinkingConfigParamOfEnabled(1024)
		return &cfg
	case "medium":
		cfg := anthropicapi.ThinkingConfigParamOfEnabled(4096)
		return &cfg
	case "high":
		cfg := anthropicapi.ThinkingConfigParamOfEnabled(8192)
		return &cfg
	case "xhigh":
		cfg := anthropicapi.ThinkingConfigParamOfEnabled(16384)
		return &cfg
	default:
		return nil
	}
}

func anthropicToolChoice(choice string, parallel bool) *anthropicapi.ToolChoiceUnionParam {
	disableParallel := anthropicapi.Bool(!parallel)
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "", "auto":
		cfg := anthropicapi.ToolChoiceUnionParam{OfAuto: &anthropicapi.ToolChoiceAutoParam{DisableParallelToolUse: disableParallel}}
		return &cfg
	case "required":
		cfg := anthropicapi.ToolChoiceUnionParam{OfAny: &anthropicapi.ToolChoiceAnyParam{DisableParallelToolUse: disableParallel}}
		return &cfg
	case "none":
		cfg := anthropicapi.ToolChoiceUnionParam{OfNone: &anthropicapi.ToolChoiceNoneParam{}}
		return &cfg
	default:
		cfg := anthropicapi.ToolChoiceUnionParam{OfAuto: &anthropicapi.ToolChoiceAutoParam{DisableParallelToolUse: disableParallel}}
		return &cfg
	}
}

func anthropicServiceTier(serviceTier string) anthropicapi.MessageNewParamsServiceTier {
	switch strings.ToLower(strings.TrimSpace(serviceTier)) {
	case "auto":
		return anthropicapi.MessageNewParamsServiceTierAuto
	case "standard_only":
		return anthropicapi.MessageNewParamsServiceTierStandardOnly
	default:
		return ""
	}
}

func anthropicMessageToResponse(message anthropicapi.Message) provideriface.Response {
	response := provideriface.Response{
		ID:         strings.TrimSpace(message.ID),
		Model:      strings.TrimSpace(string(message.Model)),
		StopReason: strings.TrimSpace(string(message.StopReason)),
		Usage:      anthropicUsageToTokenUsage(message.Usage),
	}
	var textParts []string
	var thinkingParts []string
	functionCalls := make([]provideriface.FunctionCall, 0)
	for _, block := range message.Content {
		switch variant := block.AsAny().(type) {
		case anthropicapi.TextBlock:
			if text := strings.TrimSpace(variant.Text); text != "" {
				textParts = append(textParts, text)
			}
		case anthropicapi.ThinkingBlock:
			if thinking := strings.TrimSpace(variant.Thinking); thinking != "" {
				thinkingParts = append(thinkingParts, thinking)
			}
		case anthropicapi.ToolUseBlock:
			arguments := "{}"
			if encoded, err := json.Marshal(variant.Input); err == nil && len(encoded) > 0 {
				arguments = string(encoded)
			}
			functionCalls = append(functionCalls, provideriface.FunctionCall{
				CallID:    strings.TrimSpace(variant.ID),
				Name:      firstNonEmpty(strings.TrimSpace(variant.Name), "tool"),
				Arguments: arguments,
			})
		}
	}
	response.Text = strings.TrimSpace(strings.Join(textParts, "\n\n"))
	response.ReasoningSummary = strings.TrimSpace(strings.Join(thinkingParts, "\n\n"))
	response.FunctionCalls = functionCalls
	return response
}

func anthropicUsageToTokenUsage(usage anthropicapi.Usage) provideriface.TokenUsage {
	usageRaw := map[string]any{
		"input_tokens":                usage.InputTokens,
		"output_tokens":               usage.OutputTokens,
		"cache_creation_input_tokens": usage.CacheCreationInputTokens,
		"cache_read_input_tokens":     usage.CacheReadInputTokens,
		"service_tier":                strings.TrimSpace(string(usage.ServiceTier)),
		"inference_geo":               strings.TrimSpace(usage.InferenceGeo),
		"cache_creation":              cloneAnthropicRawJSONMap(usage.CacheCreation.RawJSON()),
		"server_tool_use":             cloneAnthropicRawJSONMap(usage.ServerToolUse.RawJSON()),
	}
	return provideriface.TokenUsage{
		InputTokens:      maxInt64(usage.InputTokens, 0),
		OutputTokens:     maxInt64(usage.OutputTokens, 0),
		ThinkingTokens:   0,
		CacheReadTokens:  maxInt64(usage.CacheReadInputTokens, 0),
		CacheWriteTokens: maxInt64(usage.CacheCreationInputTokens, 0),
		TotalTokens:      maxInt64(usage.InputTokens+usage.OutputTokens+usage.CacheCreationInputTokens+usage.CacheReadInputTokens, 0),
		Source:           usageSource,
		APIUsageRaw:      usageRaw,
		APIUsageRawPath:  "usage",
		APIUsageHistory:  []map[string]any{cloneMap(usageRaw)},
		APIUsagePaths:    []string{"usage"},
	}
}

func cloneAnthropicRawJSONMap(raw string) map[string]any {
	return decodeJSONMap(raw)
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

func decodeJSONMap(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func parseJSONValue(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return map[string]any{"raw": raw}
	}
	return decoded
}

func stringField(input map[string]any, key string) (string, bool) {
	if input == nil {
		return "", false
	}
	raw, ok := input[key]
	if !ok || raw == nil {
		return "", false
	}
	switch typed := raw.(type) {
	case string:
		return typed, true
	default:
		return fmt.Sprintf("%v", typed), true
	}
}

func stringFieldDefault(input map[string]any, key string) string {
	value, _ := stringField(input, key)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func newEphemeralCacheControl() anthropicapi.CacheControlEphemeralParam {
	return anthropicapi.NewCacheControlEphemeralParam()
}

func maxInt64(value, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}
