package anthropic

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"swarm/packages/swarmd/internal/identity"
	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	promptCachingBeta = anthropicapi.AnthropicBetaPromptCaching2024_07_31
	usageSource       = "anthropic_api_usage"

	anthropicMediaAdapterID         = provideriface.MediaAdapterIDAnthropicMessagesV1
	anthropicMediaProviderSurface   = provideriface.MediaProviderSurfaceAnthropicMessages
	anthropicMediaCredentialSurface = provideriface.MediaCredentialSurfaceAnthropicAPIKey
	anthropicImageMaxBytes          = int64(7_864_320) // Largest raw payload whose padded base64 is at most 10 MiB.
	anthropicImageMaxCount          = 100              // Safe ceiling across 200k-context API models.
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

func (r *Runner) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{
		ContextMode: provideriface.ExecutionEpochContextStatelessFullInput,
	}
}

func (r *Runner) MediaCapabilityDeclaration(ctx context.Context) (provideriface.MediaAdapterDeclaration, error) {
	record, err := r.anthropicAuthRecord(ctx)
	if err != nil {
		return provideriface.MediaAdapterDeclaration{}, err
	}
	return anthropicMediaDeclaration(record), nil
}

func anthropicMediaDeclaration(record pebblestore.AuthCredentialRecord) provideriface.MediaAdapterDeclaration {
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{record.AccountScopeID, record.Provider, record.ID, record.Type}, "\x00")))
	return provideriface.MediaAdapterDeclaration{
		AdapterID:             anthropicMediaAdapterID,
		ProviderID:            "anthropic",
		ProviderSurface:       anthropicMediaProviderSurface,
		CredentialSurface:     anthropicMediaCredentialSurface,
		CredentialFingerprint: hex.EncodeToString(fingerprint[:16]),
		Inputs: []provideriface.MediaAdapterCapability{{
			Modality:     "image",
			Semantics:    pebblestore.ModelCatalogMediaSemanticsNative,
			MIMETypes:    []string{"image/gif", "image/jpeg", "image/png", "image/webp"},
			ContentTypes: []string{"image"},
			MaxBytes:     anthropicImageMaxBytes,
			MaxCount:     anthropicImageMaxCount,
		}},
	}
}

func (r *Runner) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if r == nil || r.authStore == nil {
		return provideriface.Response{}, errors.New("anthropic runner auth store is not configured")
	}
	client, modelName, params, requestOptions, err := r.buildRequest(ctx, req)
	if err != nil {
		return provideriface.Response{}, err
	}
	message, err := client.Messages.New(ctx, params, requestOptions...)
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
	client, modelName, params, requestOptions, err := r.buildRequest(ctx, req)
	if err != nil {
		return provideriface.Response{}, err
	}
	stream := client.Messages.NewStreaming(ctx, params, requestOptions...)
	message := anthropicapi.Message{}
	streamState := newAnthropicStreamState()
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return provideriface.Response{}, fmt.Errorf("accumulate anthropic stream: %w", err)
		}
		streamState.HandleEvent(event, onEvent)
	}
	if err := stream.Err(); err != nil {
		return provideriface.Response{}, err
	}
	response := anthropicMessageToResponse(message)
	if strings.TrimSpace(response.Model) == "" {
		response.Model = modelName
	}
	if strings.TrimSpace(response.Text) == "" {
		response.Text = strings.TrimSpace(streamState.Text())
	}
	if strings.TrimSpace(response.ReasoningSummary) == "" {
		response.ReasoningSummary = strings.TrimSpace(streamState.Thinking())
	}
	return response, nil
}

type anthropicStreamState struct {
	textBuilder   strings.Builder
	thinkingByKey map[int64]string
	thinkingOrder []int64
	activeIndex   int64
}

func newAnthropicStreamState() *anthropicStreamState {
	return &anthropicStreamState{activeIndex: -1, thinkingByKey: make(map[int64]string, 4)}
}

func (s *anthropicStreamState) HandleEvent(event anthropicapi.MessageStreamEventUnion, onEvent func(provideriface.StreamEvent)) {
	if s == nil {
		return
	}
	switch variant := event.AsAny().(type) {
	case anthropicapi.ContentBlockStartEvent:
		s.activeIndex = variant.Index
		switch block := variant.ContentBlock.AsAny().(type) {
		case anthropicapi.ThinkingBlock:
			if thinking := block.Thinking; strings.TrimSpace(thinking) != "" {
				next := s.appendThinking(variant.Index, thinking)
				if onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: next, DeltaMode: provideriface.StreamEventDeltaModeReplace, ReasoningKey: anthropicReasoningKey(variant.Index)})
				}
			}
		}
	case anthropicapi.ContentBlockDeltaEvent:
		if s.activeIndex < 0 {
			s.activeIndex = variant.Index
		}
		reasoningKey := anthropicReasoningKey(variant.Index)
		switch delta := variant.Delta.AsAny().(type) {
		case anthropicapi.TextDelta:
			if strings.TrimSpace(delta.Text) != "" {
				s.textBuilder.WriteString(delta.Text)
				if onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventOutputTextDelta, Delta: delta.Text})
				}
			}
		case anthropicapi.ThinkingDelta:
			if strings.TrimSpace(delta.Thinking) != "" {
				next := s.appendThinking(variant.Index, delta.Thinking)
				if onEvent != nil {
					onEvent(provideriface.StreamEvent{Type: provideriface.StreamEventReasoningSummaryDelta, Delta: next, DeltaMode: provideriface.StreamEventDeltaModeReplace, ReasoningKey: reasoningKey})
				}
			}
		case anthropicapi.SignatureDelta:
			// Anthropic emits a signature_delta after a thinking block. The signature is
			// transport verification metadata, not user-visible reasoning text.
		}
	case anthropicapi.ContentBlockStopEvent:
		if variant.Index == s.activeIndex {
			s.activeIndex = -1
		}
	}
}

func (s *anthropicStreamState) Text() string {
	if s == nil {
		return ""
	}
	return s.textBuilder.String()
}

func (s *anthropicStreamState) Thinking() string {
	if s == nil || len(s.thinkingOrder) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.thinkingOrder))
	for _, index := range s.thinkingOrder {
		if thinking := strings.TrimSpace(s.thinkingByKey[index]); thinking != "" {
			parts = append(parts, thinking)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (s *anthropicStreamState) appendThinking(index int64, delta string) string {
	if s == nil {
		return ""
	}
	if s.thinkingByKey == nil {
		s.thinkingByKey = make(map[int64]string, 4)
	}
	if _, ok := s.thinkingByKey[index]; !ok {
		s.thinkingOrder = append(s.thinkingOrder, index)
	}
	next := s.thinkingByKey[index] + delta
	s.thinkingByKey[index] = next
	return next
}

func anthropicReasoningKey(index int64) string {
	if index < 0 {
		return "anthropic-thinking"
	}
	return fmt.Sprintf("anthropic-thinking-%d", index)
}

func (r *Runner) buildRequest(ctx context.Context, req provideriface.Request) (anthropicapi.Client, string, anthropicapi.MessageNewParams, []option.RequestOption, error) {
	record, err := r.anthropicAuthRecord(ctx)
	if err != nil {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, nil, err
	}
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, nil, errors.New("model is required")
	}
	messages, err := buildAnthropicMessages(req.Input, req.MediaContract)
	if err != nil {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, nil, err
	}
	tools, enablePromptCaching, err := buildAnthropicTools(req.Tools)
	if err != nil {
		return anthropicapi.Client{}, "", anthropicapi.MessageNewParams{}, nil, err
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
	if thinking, effort := anthropicThinkingConfig(req.ModelCatalog, req.Thinking); thinking != nil {
		params.Thinking = *thinking
		if effort != "" {
			params.OutputConfig = anthropicapi.OutputConfigParam{Effort: effort}
		}
	}
	if toolChoice := anthropicToolChoice(req.ToolChoice, req.ParallelToolCalls); toolChoice != nil {
		params.ToolChoice = *toolChoice
	}
	requestOptions := anthropicRequestOptions(req.ModelCatalog, req.ServiceTier)
	if serviceTier := anthropicProviderServiceTier(req.ModelCatalog, req.ServiceTier); serviceTier != "" {
		params.ServiceTier = serviceTier
	}
	if enablePromptCaching {
		applyAnthropicPromptCaching(&params, tools)
	}
	client := anthropicapi.NewClient(anthropicClientOptions(strings.TrimSpace(record.APIKey))...)
	return client, modelName, params, requestOptions, nil
}

func (r *Runner) anthropicAuthRecord(ctx context.Context) (pebblestore.AuthCredentialRecord, error) {
	principal, principalOK := identity.PrincipalFromContext(ctx)
	if !principalOK || !principal.Valid() {
		return pebblestore.AuthCredentialRecord{}, identity.ErrPrincipalRequired
	}
	if r == nil || r.authStore == nil {
		return pebblestore.AuthCredentialRecord{}, errors.New("anthropic runner auth store is not configured")
	}
	record, ok, err := r.authStore.GetActiveCredentialForAccount(principal.AccountScopeID, "anthropic")
	if err != nil {
		return pebblestore.AuthCredentialRecord{}, fmt.Errorf("read anthropic auth: %w", err)
	}
	if !ok || strings.TrimSpace(record.APIKey) == "" {
		return pebblestore.AuthCredentialRecord{}, errors.New("anthropic auth is not configured")
	}
	record.Provider = "anthropic"
	record.Type = pebblestore.AuthTypeAPI
	return record, nil
}

func anthropicClientOptions(apiKey string) []option.RequestOption {
	return []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHeaderAdd("anthropic-beta", string(promptCachingBeta)),
		option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			return providerdiagnostics.RoundTrip("anthropic", "messages", next, req)
		}),
	}
}

func anthropicRequestOptions(catalog any, serviceTier string) []option.RequestOption {
	mapping, ok := anthropicServiceTierMapping(catalog, serviceTier)
	if !ok || !strings.EqualFold(strings.TrimSpace(mapping.ProviderParameter), "speed") || !strings.EqualFold(strings.TrimSpace(mapping.ProviderValue), "fast") {
		return nil
	}
	options := []option.RequestOption{option.WithJSONSet("speed", "fast")}
	if betaHeader := strings.TrimSpace(mapping.BetaHeader); betaHeader != "" {
		options = append(options, option.WithHeaderAdd("anthropic-beta", betaHeader))
	}
	return options
}

func anthropicServiceTierMapping(catalog any, serviceTier string) (pebblestore.ModelCatalogServiceTierMapping, bool) {
	record, ok := catalog.(pebblestore.ModelCatalogRecord)
	if !ok {
		return pebblestore.ModelCatalogServiceTierMapping{}, false
	}
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	if serviceTier == "" || serviceTier == "standard" || serviceTier == "off" {
		return pebblestore.ModelCatalogServiceTierMapping{}, false
	}
	for _, mapping := range record.ServiceTierMappings {
		if strings.EqualFold(strings.TrimSpace(mapping.Tier), serviceTier) {
			return mapping, true
		}
	}
	for _, mapping := range record.ServiceTierMappings {
		if strings.EqualFold(strings.TrimSpace(mapping.SwarmSetting), serviceTier) {
			return mapping, true
		}
	}
	return pebblestore.ModelCatalogServiceTierMapping{}, false
}

func anthropicProviderServiceTier(catalog any, serviceTier string) anthropicapi.MessageNewParamsServiceTier {
	if mapping, ok := anthropicServiceTierMapping(catalog, serviceTier); ok && strings.EqualFold(strings.TrimSpace(mapping.ProviderParameter), "service_tier") {
		return anthropicServiceTier(mapping.ProviderValue)
	}
	return anthropicServiceTier(serviceTier)
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

func buildAnthropicMessages(input []map[string]any, mediaContract provideriface.SessionMediaContract) ([]anthropicapi.MessageParam, error) {
	messages := make([]anthropicapi.MessageParam, 0, len(input))
	mediaCount := 0
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
		blocks, err := buildAnthropicContentBlocks(item["content"], strings.TrimSpace(role), mediaContract, &mediaCount)
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

func buildAnthropicContentBlocks(content any, role string, mediaContract provideriface.SessionMediaContract, mediaCount *int) ([]anthropicapi.ContentBlockParamUnion, error) {
	switch typed := content.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, nil
		}
		return []anthropicapi.ContentBlockParamUnion{{OfText: &anthropicapi.TextBlockParam{Text: text}}}, nil
	case []map[string]any:
		return buildAnthropicContentBlocksFromMaps(typed, role, mediaContract, mediaCount)
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if !ok {
				continue
			}
			items = append(items, mapped)
		}
		return buildAnthropicContentBlocksFromMaps(items, role, mediaContract, mediaCount)
	default:
		return nil, nil
	}
}

func buildAnthropicContentBlocksFromMaps(items []map[string]any, role string, mediaContract provideriface.SessionMediaContract, mediaCount *int) ([]anthropicapi.ContentBlockParamUnion, error) {
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
		case "image", "input_image", "input_file", "file", "audio", "input_audio", "video", "input_video":
			return nil, errors.New("raw provider media content is not accepted; authorized session media is required")
		case "session_media":
			if !strings.EqualFold(strings.TrimSpace(role), "user") {
				return nil, errors.New("anthropic image input is only valid in explicit user message content")
			}
			payload, ok := item["media"].(provideriface.SessionMediaPayload)
			if !ok {
				return nil, errors.New("anthropic session media payload is malformed")
			}
			if mediaCount == nil {
				return nil, errors.New("anthropic media count is not configured")
			}
			*mediaCount = *mediaCount + 1
			image, err := anthropicImageBlock(mediaContract, payload, *mediaCount)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, image)
		default:
			if text, _ := stringField(item, "text"); strings.TrimSpace(text) != "" {
				blocks = append(blocks, anthropicapi.ContentBlockParamUnion{OfText: &anthropicapi.TextBlockParam{Text: strings.TrimSpace(text)}})
			}
		}
	}
	return blocks, nil
}

func anthropicImageBlock(contract provideriface.SessionMediaContract, payload provideriface.SessionMediaPayload, count int) (anthropicapi.ContentBlockParamUnion, error) {
	if _, err := validateAnthropicImagePayload(contract, payload, count); err != nil {
		return anthropicapi.ContentBlockParamUnion{}, err
	}
	data := base64.StdEncoding.EncodeToString(payload.Bytes)
	return anthropicapi.NewImageBlockBase64(strings.ToLower(strings.TrimSpace(payload.MIMEType)), data), nil
}

func validateAnthropicImagePayload(contract provideriface.SessionMediaContract, payload provideriface.SessionMediaPayload, count int) (provideriface.MediaContractCapability, error) {
	if strings.TrimSpace(contract.Hash) == "" || !strings.EqualFold(strings.TrimSpace(contract.ProviderID), "anthropic") || contract.ProviderSurface != anthropicMediaProviderSurface || contract.CredentialSurface != anthropicMediaCredentialSurface || contract.AdapterID != anthropicMediaAdapterID {
		return provideriface.MediaContractCapability{}, errors.New("media contract does not match the active Anthropic API-key Messages surface")
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Modality), "image") || strings.TrimSpace(payload.FileType) != "" {
		return provideriface.MediaContractCapability{}, errors.New("anthropic media payload is not a native image input")
	}
	mimeType := strings.ToLower(strings.TrimSpace(payload.MIMEType))
	if !containsFold([]string{"image/gif", "image/jpeg", "image/png", "image/webp"}, mimeType) {
		return provideriface.MediaContractCapability{}, errors.New("anthropic image MIME type is unsupported")
	}
	if strings.TrimSpace(payload.AssetID) == "" || len(payload.Bytes) == 0 || payload.Size <= 0 || payload.Size != int64(len(payload.Bytes)) || payload.Size > anthropicImageMaxBytes {
		return provideriface.MediaContractCapability{}, errors.New("anthropic image payload identity or size is invalid")
	}
	digest := sha256.Sum256(payload.Bytes)
	if !strings.EqualFold(strings.TrimSpace(payload.DigestSHA256), hex.EncodeToString(digest[:])) {
		return provideriface.MediaContractCapability{}, errors.New("anthropic image payload digest is invalid")
	}
	for _, capability := range contract.Capabilities {
		if !strings.EqualFold(strings.TrimSpace(capability.Modality), "image") {
			continue
		}
		if capability.State != provideriface.MediaCapabilityStateAllowed || capability.Semantics != pebblestore.ModelCatalogMediaSemanticsNative || !containsFold(capability.MIMETypes, mimeType) || !containsFold(capability.ContentTypes, "image") || capability.MaxBytes <= 0 || payload.Size > capability.MaxBytes || capability.MaxCount <= 0 || count > capability.MaxCount || count > anthropicImageMaxCount {
			return provideriface.MediaContractCapability{}, errors.New("anthropic image payload is outside the authorized media capability")
		}
		return capability, nil
	}
	return provideriface.MediaContractCapability{}, errors.New("anthropic image capability is absent")
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func anthropicThinkingConfig(catalog any, level string) (*anthropicapi.ThinkingConfigParamUnion, anthropicapi.OutputConfigEffort) {
	level = strings.ToLower(strings.TrimSpace(level))
	mapping, hasMapping := anthropicThinkingMapping(catalog, level)
	if hasMapping {
		return anthropicThinkingConfigFromMapping(mapping, level)
	}
	switch level {
	case "off":
		cfg := anthropicapi.ThinkingConfigParamUnion{OfDisabled: &anthropicapi.ThinkingConfigDisabledParam{}}
		return &cfg, ""
	case "low", "medium", "high", "xhigh":
		cfg := anthropicapi.ThinkingConfigParamOfEnabled(anthropicThinkingBudgetTokens(level))
		return &cfg, ""
	default:
		return nil, ""
	}
}

func anthropicThinkingMapping(catalog any, level string) (pebblestore.ModelCatalogThinkingMapping, bool) {
	record, ok := catalog.(pebblestore.ModelCatalogRecord)
	if !ok {
		return pebblestore.ModelCatalogThinkingMapping{}, false
	}
	level = strings.ToLower(strings.TrimSpace(level))
	for _, mapping := range record.ThinkingMappings {
		if strings.EqualFold(strings.TrimSpace(mapping.SwarmSetting), level) {
			return mapping, true
		}
	}
	return pebblestore.ModelCatalogThinkingMapping{}, false
}

func anthropicThinkingConfigFromMapping(mapping pebblestore.ModelCatalogThinkingMapping, level string) (*anthropicapi.ThinkingConfigParamUnion, anthropicapi.OutputConfigEffort) {
	behavior := strings.ToLower(strings.TrimSpace(mapping.Behavior))
	providerParameter := strings.ToLower(strings.TrimSpace(mapping.ProviderParameter))
	providerValue := firstNonEmpty(strings.TrimSpace(mapping.EffectiveProviderValue), strings.TrimSpace(mapping.ProviderValue))
	if behavior == "disabled" || strings.EqualFold(level, "off") {
		cfg := anthropicapi.ThinkingConfigParamUnion{OfDisabled: &anthropicapi.ThinkingConfigDisabledParam{}}
		return &cfg, ""
	}
	if behavior == "effort" || strings.Contains(providerParameter, "output_config.effort") || strings.Contains(providerParameter, "output_config") {
		effort := anthropicOutputEffort(providerValue)
		if effort == "" {
			effort = anthropicOutputEffort(level)
		}
		if effort == "" {
			return nil, ""
		}
		cfg := anthropicapi.ThinkingConfigParamUnion{OfAdaptive: &anthropicapi.ThinkingConfigAdaptiveParam{Display: anthropicapi.ThinkingConfigAdaptiveDisplaySummarized}}
		return &cfg, effort
	}
	if strings.Contains(providerParameter, "budget_tokens") {
		budgetTokens := anthropicThinkingBudgetTokensFromMapping(providerValue, level)
		if budgetTokens <= 0 {
			return nil, ""
		}
		cfg := anthropicapi.ThinkingConfigParamOfEnabled(budgetTokens)
		return &cfg, ""
	}
	return nil, ""
}

func anthropicThinkingBudgetTokensFromMapping(providerValue, level string) int64 {
	if parsed, err := strconv.ParseInt(strings.TrimSpace(providerValue), 10, 64); err == nil && parsed > 0 {
		return parsed
	}
	return anthropicThinkingBudgetTokens(level)
}

func anthropicThinkingBudgetTokens(level string) int64 {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 8192
	case "xhigh":
		return 16384
	default:
		return 4096
	}
}

func anthropicOutputEffort(level string) anthropicapi.OutputConfigEffort {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low":
		return anthropicapi.OutputConfigEffortLow
	case "medium":
		return anthropicapi.OutputConfigEffortMedium
	case "high":
		return anthropicapi.OutputConfigEffortHigh
	case "xhigh":
		return anthropicapi.OutputConfigEffortXhigh
	default:
		return ""
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
	case "auto", "priority":
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
		ServiceTier:      strings.TrimSpace(string(usage.ServiceTier)),
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
