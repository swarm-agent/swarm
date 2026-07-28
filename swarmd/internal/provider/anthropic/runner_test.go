package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestAnthropicExecutionEpochRequestContainsOnlyExplicitInput(t *testing.T) {
	lifecycle := (&Runner{}).ExecutionEpochLifecycle()
	if lifecycle.ContextMode != provideriface.ExecutionEpochContextStatelessFullInput || !lifecycle.Valid() {
		t.Fatalf("lifecycle = %+v, want valid stateless full-input mode", lifecycle)
	}
	input := []map[string]any{
		{"role": "user", "content": "epoch user"},
		{"role": "assistant", "content": "epoch assistant"},
		{"role": "user", "content": "epoch follow-up"},
	}
	messages, err := buildAnthropicMessages(input, provideriface.SessionMediaContract{})
	if err != nil {
		t.Fatalf("build messages: %v", err)
	}
	encoded := mustMarshalJSON(t, messages)
	for _, text := range []string{"epoch user", "epoch assistant", "epoch follow-up"} {
		assertContains(t, encoded, text)
	}
	for _, forbidden := range []string{"previous_response_id", "conversation", "predecessor"} {
		assertNotContains(t, encoded, forbidden)
	}
}

func TestSanitizeAnthropicToolSchemaTransformsComplexUnions(t *testing.T) {
	schema, err := sanitizeAnthropicToolSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"description": "boolean or options object",
				"anyOf": []any{
					map[string]any{"type": "boolean"},
					map[string]any{
						"type":                 "object",
						"properties":           map[string]any{},
						"required":             []string{},
						"additionalProperties": true,
					},
				},
			},
			"choice": map[string]any{
				"oneOf": []any{
					map[string]any{"type": "string"},
					map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
		},
		"required":             []string{},
		"additionalProperties": false,
	})
	if err != nil {
		t.Fatalf("sanitize schema: %v", err)
	}

	encoded := mustMarshalJSON(t, schema)
	assertNotContains(t, encoded, `"additionalProperties":true`)
	assertNotContains(t, encoded, `"oneOf"`)
	assertContains(t, encoded, `"additionalProperties":false`)
	assertContains(t, encoded, `"anyOf"`)

	var decoded map[string]any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props := decoded["properties"].(map[string]any)
	textSchema := props["text"].(map[string]any)
	variants := textSchema["anyOf"].([]any)
	objectVariant := variants[1].(map[string]any)
	if got := objectVariant["additionalProperties"]; got != false {
		t.Fatalf("nested object additionalProperties = %v, want false", got)
	}
	choiceSchema := props["choice"].(map[string]any)
	if _, ok := choiceSchema["oneOf"]; ok {
		t.Fatalf("oneOf should have been converted to anyOf: %v", choiceSchema)
	}
	if _, ok := choiceSchema["anyOf"]; !ok {
		t.Fatalf("anyOf missing after oneOf conversion: %v", choiceSchema)
	}
}

func TestSanitizeAnthropicToolSchemaMovesUnsupportedConstraintsToDescription(t *testing.T) {
	schema, err := sanitizeAnthropicToolSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"date": map[string]any{
				"type":        "string",
				"format":      "regex",
				"pattern":     "^[a-z]+$",
				"description": "A constrained string",
			},
			"items": map[string]any{
				"type":     "array",
				"minItems": 2,
				"items":    map[string]any{"type": "string"},
			},
		},
	})
	if err != nil {
		t.Fatalf("sanitize schema: %v", err)
	}

	encoded := mustMarshalJSON(t, schema)
	assertNotContains(t, encoded, `"format":"regex"`)
	assertNotContains(t, encoded, `"pattern"`)
	assertNotContains(t, encoded, `"minItems":2`)
	assertContains(t, encoded, `format: regex`)
	assertContains(t, encoded, `pattern: ^[a-z]+$`)
	assertContains(t, encoded, `minItems: 2`)
}

func TestAnthropicThinkingConfigUsesCatalogAdaptiveEffortMapping(t *testing.T) {
	catalog := pebblestore.ModelCatalogRecord{ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{
		{SwarmSetting: "off", ProviderParameter: "thinking.type + output_config.effort", Behavior: "disabled"},
		{SwarmSetting: "medium", ProviderParameter: "thinking.type + output_config.effort", ProviderValue: "medium", EffectiveProviderValue: "medium", Behavior: "effort"},
	}}
	cfg, effort := anthropicThinkingConfig(catalog, "medium")
	if cfg == nil || cfg.OfAdaptive == nil {
		t.Fatalf("expected adaptive thinking config from catalog mapping, got %#v", cfg)
	}
	if cfg.OfEnabled != nil {
		t.Fatalf("adaptive catalog mapping must not use enabled thinking: %#v", cfg.OfEnabled)
	}
	if effort != anthropicapi.OutputConfigEffortMedium {
		t.Fatalf("effort = %q, want medium", effort)
	}
}

func TestAnthropicThinkingConfigKeepsBudgetForBudgetTokenMappings(t *testing.T) {
	catalog := pebblestore.ModelCatalogRecord{ThinkingMappings: []pebblestore.ModelCatalogThinkingMapping{
		{SwarmSetting: "medium", ProviderParameter: "thinking.budget_tokens", ProviderValue: "4096"},
	}}
	cfg, effort := anthropicThinkingConfig(catalog, "medium")
	if cfg == nil || cfg.OfEnabled == nil {
		t.Fatalf("expected enabled thinking config for budget-token mapping, got %#v", cfg)
	}
	if got := cfg.OfEnabled.BudgetTokens; got != 4096 {
		t.Fatalf("budget tokens = %d, want 4096", got)
	}
	if effort != "" {
		t.Fatalf("effort = %q, want empty", effort)
	}
}

func TestAnthropicThinkingConfigFallsBackToLegacyBudgetWithoutCatalog(t *testing.T) {
	cfg, effort := anthropicThinkingConfig(nil, "medium")
	if cfg == nil || cfg.OfEnabled == nil {
		t.Fatalf("expected enabled thinking config without catalog, got %#v", cfg)
	}
	if got := cfg.OfEnabled.BudgetTokens; got != 4096 {
		t.Fatalf("budget tokens = %d, want 4096", got)
	}
	if effort != "" {
		t.Fatalf("effort = %q, want empty", effort)
	}
}

func TestAnthropicRequestOptionsApplyProviderFastModeSeparatelyFromPriority(t *testing.T) {
	catalog := pebblestore.ModelCatalogRecord{ServiceTierMappings: []pebblestore.ModelCatalogServiceTierMapping{
		{Tier: "priority", SwarmSetting: "fast", ProviderParameter: "service_tier", ProviderValue: "auto"},
		{Tier: "fast", ProviderParameter: "speed", ProviderValue: "fast", BetaHeader: "fast-mode-2026-02-01"},
	}}
	if opts := anthropicRequestOptions(catalog, "priority"); len(opts) != 0 {
		t.Fatalf("priority tier must not enable provider Fast Mode options: %d", len(opts))
	}
	if got := anthropicProviderServiceTier(catalog, "priority"); got != anthropicapi.MessageNewParamsServiceTierAuto {
		t.Fatalf("priority service tier = %q, want auto", got)
	}
	opts := anthropicRequestOptions(catalog, "fast")
	if len(opts) != 2 {
		t.Fatalf("fast mode options = %d, want speed JSON set and beta header", len(opts))
	}
}

func TestAnthropicUsageMapsCacheTokenTypesForFrontend(t *testing.T) {
	usage := anthropicUsageToTokenUsage(anthropicapi.Usage{
		InputTokens:              100,
		OutputTokens:             20,
		CacheReadInputTokens:     30,
		CacheCreationInputTokens: 40,
		ServiceTier:              anthropicapi.UsageServiceTierPriority,
	})
	if usage.Source != usageSource || usage.APIUsageRawPath != "usage" {
		t.Fatalf("usage identity = %+v", usage)
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 20 || usage.CacheReadTokens != 30 || usage.CacheWriteTokens != 40 || usage.TotalTokens != 190 {
		t.Fatalf("usage tokens = %+v", usage)
	}
	if usage.ServiceTier != "priority" {
		t.Fatalf("service tier = %q, want priority", usage.ServiceTier)
	}
	if usage.APIUsageRaw["cache_read_input_tokens"] != int64(30) || usage.APIUsageRaw["cache_creation_input_tokens"] != int64(40) {
		t.Fatalf("raw cache token fields = %#v", usage.APIUsageRaw)
	}
}

func TestBuildRequestPlacesPromptCacheControlsAtOfficialBreakpoints(t *testing.T) {
	tools, _, err := buildAnthropicTools([]provideriface.ToolDefinition{
		{Name: "read", Description: "Read", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "write", Description: "Write", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
	})
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	params := anthropicapiMessageParamsForCacheTest(tools, buildAnthropicSystem("system prompt"))
	encoded := mustMarshalJSON(t, params)
	if got := strings.Count(encoded, `"cache_control"`); got != 2 {
		t.Fatalf("cache_control count = %d, want 2: %s", got, encoded)
	}

	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	toolList := decoded["tools"].([]any)
	firstTool := toolList[0].(map[string]any)
	if _, ok := firstTool["cache_control"]; ok {
		t.Fatalf("only the last tool should carry cache_control: %v", firstTool)
	}
	lastTool := toolList[len(toolList)-1].(map[string]any)
	if _, ok := lastTool["cache_control"]; !ok {
		t.Fatalf("last tool missing cache_control: %v", lastTool)
	}
	if _, ok := decoded["cache_control"]; !ok {
		t.Fatalf("top-level cache_control missing: %v", decoded)
	}
	systemBlocks := decoded["system"].([]any)
	if _, ok := systemBlocks[0].(map[string]any)["cache_control"]; ok {
		t.Fatalf("system block should not carry explicit cache_control: %v", systemBlocks[0])
	}
}

func anthropicapiMessageParamsForCacheTest(tools []anthropicapi.ToolUnionParam, system []anthropicapi.TextBlockParam) anthropicapi.MessageNewParams {
	params := anthropicapi.MessageNewParams{
		Model:     anthropicapi.Model("claude-test"),
		MaxTokens: 1024,
		Messages:  []anthropicapi.MessageParam{anthropicapi.NewUserMessage(anthropicapi.NewTextBlock("hello"))},
		System:    system,
		Tools:     tools,
	}
	if len(system) > 0 || len(tools) > 0 {
		applyAnthropicPromptCaching(&params, tools)
	}
	return params
}

func TestAnthropicStreamStateEmitsThinkingDeltasWithStableReasoningKey(t *testing.T) {
	state := newAnthropicStreamState()
	var events []provideriface.StreamEvent
	emit := func(event provideriface.StreamEvent) {
		events = append(events, event)
	}

	for _, raw := range []string{
		`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"Plan: "}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"inspect "}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"adapter"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"transport-signature"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"redacted_thinking","data":"encrypted"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"final answer"}}`,
	} {
		event := mustAnthropicStreamEvent(t, raw)
		state.HandleEvent(event, emit)
	}

	if got, want := state.Thinking(), "Plan: inspect adapter"; got != want {
		t.Fatalf("thinking snapshot = %q, want %q", got, want)
	}
	if got, want := state.Text(), "final answer"; got != want {
		t.Fatalf("text snapshot = %q, want %q", got, want)
	}
	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4: %+v", len(events), events)
	}
	for i, event := range events[:3] {
		if event.Type != provideriface.StreamEventReasoningSummaryDelta || event.ReasoningKey != "anthropic-thinking-1" {
			t.Fatalf("thinking event %d = %+v", i, event)
		}
	}
	if events[0].Delta != "Plan: " || events[1].Delta != "Plan: inspect " || events[2].Delta != "Plan: inspect adapter" {
		t.Fatalf("unexpected thinking snapshots: %+v", events[:3])
	}
	if events[3].Type != provideriface.StreamEventOutputTextDelta || events[3].Delta != "final answer" {
		t.Fatalf("text event = %+v", events[3])
	}
}

func TestAnthropicMessageToResponseCollectsThinkingAndIgnoresRedactedThinking(t *testing.T) {
	message := anthropicapi.Message{}
	for _, raw := range []string{
		`{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"visible"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signature"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"encrypted"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"answer"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_stop"}`,
	} {
		event := mustAnthropicStreamEvent(t, raw)
		if err := message.Accumulate(event); err != nil {
			t.Fatalf("accumulate %s: %v", raw, err)
		}
	}

	response := anthropicMessageToResponse(message)
	if response.Text != "answer" {
		t.Fatalf("response text = %q", response.Text)
	}
	if response.ReasoningSummary != "visible thinking" {
		t.Fatalf("response reasoning = %q", response.ReasoningSummary)
	}
}

func mustAnthropicStreamEvent(t *testing.T, raw string) anthropicapi.MessageStreamEventUnion {
	t.Helper()
	var event anthropicapi.MessageStreamEventUnion
	if err := event.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatalf("unmarshal stream event %s: %v", raw, err)
	}
	return event
}

func TestAnthropicMediaDeclarationIsImageInputOnly(t *testing.T) {
	declaration := anthropicMediaDeclaration(pebblestore.AuthCredentialRecord{
		AccountScopeID: "account-test", Provider: "anthropic", ID: "primary", Type: pebblestore.AuthTypeAPI,
	})
	if declaration.AdapterID != anthropicMediaAdapterID || declaration.ProviderID != "anthropic" || declaration.ProviderSurface != anthropicMediaProviderSurface || declaration.CredentialSurface != anthropicMediaCredentialSurface || declaration.CredentialFingerprint == "" {
		t.Fatalf("declaration identity = %+v", declaration)
	}
	if len(declaration.Inputs) != 1 {
		t.Fatalf("inputs = %+v, want one image input", declaration.Inputs)
	}
	image := declaration.Inputs[0]
	if image.Modality != "image" || image.Semantics != pebblestore.ModelCatalogMediaSemanticsNative || image.MaxBytes != anthropicImageMaxBytes || image.MaxCount != anthropicImageMaxCount {
		t.Fatalf("image capability = %+v", image)
	}
	if got := strings.Join(image.MIMETypes, ","); got != "image/gif,image/jpeg,image/png,image/webp" {
		t.Fatalf("MIME types = %q", got)
	}
	if got := strings.Join(image.ContentTypes, ","); got != "image" {
		t.Fatalf("content types = %q", got)
	}
}

func TestBuildAnthropicMessagesMaterializesNativeImageInOriginalOrder(t *testing.T) {
	payload := anthropicTestImagePayload("image/png", []byte("image-bytes"))
	messages, err := buildAnthropicMessages([]map[string]any{{
		"role": "user",
		"content": []map[string]any{
			{"type": "input_text", "text": "before"},
			{"type": "session_media", "media": payload},
			{"type": "input_text", "text": "after"},
		},
	}}, anthropicTestMediaContract(2))
	if err != nil {
		t.Fatalf("build messages: %v", err)
	}
	encoded := mustMarshalJSON(t, messages)
	want := `[{"role":"user","content":[{"text":"before","type":"text"},{"source":{"data":"aW1hZ2UtYnl0ZXM=","media_type":"image/png","type":"base64"},"type":"image"},{"text":"after","type":"text"}]}]`
	if encoded != want {
		t.Fatalf("messages = %s, want %s", encoded, want)
	}
	for _, forbidden := range []string{payload.AssetID, payload.DigestSHA256, "session_media", "file_id", "url"} {
		assertNotContains(t, encoded, forbidden)
	}
}

func TestBuildAnthropicMessagesRejectsInvalidMedia(t *testing.T) {
	valid := anthropicTestImagePayload("image/png", []byte("image-bytes"))
	cases := []struct {
		name     string
		role     string
		contract provideriface.SessionMediaContract
		media    any
	}{
		{name: "absent contract", role: "user", contract: provideriface.SessionMediaContract{}, media: valid},
		{name: "wrong surface", role: "user", contract: func() provideriface.SessionMediaContract { c := anthropicTestMediaContract(1); c.ProviderSurface = "other"; return c }(), media: valid},
		{name: "unsupported MIME", role: "user", contract: anthropicTestMediaContract(1), media: anthropicTestImagePayload("image/svg+xml", []byte("svg"))},
		{name: "unsupported modality", role: "user", contract: anthropicTestMediaContract(1), media: func() provideriface.SessionMediaPayload { p := valid; p.Modality = "audio"; return p }()},
		{name: "forged digest", role: "user", contract: anthropicTestMediaContract(1), media: func() provideriface.SessionMediaPayload { p := valid; p.DigestSHA256 = strings.Repeat("0", 64); return p }()},
		{name: "wrong size", role: "user", contract: anthropicTestMediaContract(1), media: func() provideriface.SessionMediaPayload { p := valid; p.Size++; return p }()},
		{name: "missing asset identity", role: "user", contract: anthropicTestMediaContract(1), media: func() provideriface.SessionMediaPayload { p := valid; p.AssetID = ""; return p }()},
		{name: "provider processed semantics", role: "user", contract: func() provideriface.SessionMediaContract { c := anthropicTestMediaContract(1); c.Capabilities[0].Semantics = pebblestore.ModelCatalogMediaSemanticsProviderProcessed; return c }(), media: valid},
		{name: "undeclared content type", role: "user", contract: func() provideriface.SessionMediaContract { c := anthropicTestMediaContract(1); c.Capabilities[0].ContentTypes = []string{"input_image"}; return c }(), media: valid},
		{name: "denied capability", role: "user", contract: func() provideriface.SessionMediaContract { c := anthropicTestMediaContract(1); c.Capabilities[0].State = provideriface.MediaCapabilityStateDenied; return c }(), media: valid},
		{name: "assistant placement", role: "assistant", contract: anthropicTestMediaContract(1), media: valid},
		{name: "ambiguous placement", role: "", contract: anthropicTestMediaContract(1), media: valid},
		{name: "malformed payload", role: "user", contract: anthropicTestMediaContract(1), media: map[string]any{"url": "https://example.invalid/image.png"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildAnthropicMessages([]map[string]any{{
				"role": test.role,
				"content": []map[string]any{{"type": "session_media", "media": test.media}},
			}}, test.contract)
			if err == nil {
				t.Fatal("error = nil")
			}
			if strings.Contains(err.Error(), string(valid.Bytes)) || strings.Contains(err.Error(), valid.AssetID) || strings.Contains(err.Error(), valid.DigestSHA256) {
				t.Fatalf("error disclosed media material: %v", err)
			}
		})
	}
}

func TestBuildAnthropicMessagesRejectsRawMediaContent(t *testing.T) {
	for _, itemType := range []string{"image", "input_image", "input_file", "audio", "video"} {
		t.Run(itemType, func(t *testing.T) {
			_, err := buildAnthropicMessages([]map[string]any{{
				"role": "user",
				"content": []map[string]any{{"type": itemType, "url": "https://example.invalid/media"}},
			}}, anthropicTestMediaContract(1))
			if err == nil || !strings.Contains(err.Error(), "authorized session media") {
				t.Fatalf("error = %v, want raw-media rejection", err)
			}
		})
	}
}

func TestBuildAnthropicMessagesEnforcesImageCountAcrossMessages(t *testing.T) {
	payload := anthropicTestImagePayload("image/jpeg", []byte("image"))
	_, err := buildAnthropicMessages([]map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "session_media", "media": payload}}},
		{"role": "user", "content": []map[string]any{{"type": "session_media", "media": payload}}},
	}, anthropicTestMediaContract(1))
	if err == nil || !strings.Contains(err.Error(), "outside the authorized") {
		t.Fatalf("error = %v, want count rejection", err)
	}
}

func anthropicTestImagePayload(mimeType string, body []byte) provideriface.SessionMediaPayload {
	digest := sha256.Sum256(body)
	return provideriface.SessionMediaPayload{
		AssetID: "media-test", Modality: "image", MIMEType: mimeType,
		DigestSHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)), Bytes: body,
	}
}

func anthropicTestMediaContract(maxCount int) provideriface.SessionMediaContract {
	return provideriface.SessionMediaContract{
		ProviderID: "anthropic", ProviderSurface: anthropicMediaProviderSurface,
		CredentialSurface: anthropicMediaCredentialSurface, AdapterID: anthropicMediaAdapterID,
		Hash: "contract-hash",
		Capabilities: []provideriface.MediaContractCapability{{
			Modality: "image", State: provideriface.MediaCapabilityStateAllowed,
			Semantics: pebblestore.ModelCatalogMediaSemanticsNative,
			MIMETypes: []string{"image/gif", "image/jpeg", "image/png", "image/webp"},
			ContentTypes: []string{"image"}, MaxBytes: anthropicImageMaxBytes, MaxCount: maxCount,
		}},
	}
}

func TestBuildAnthropicContentBlocksDoNotEmitPerMessageCacheControls(t *testing.T) {
	mediaCount := 0
	blocks, err := buildAnthropicContentBlocks([]any{
		map[string]any{"type": "input_text", "text": "hello"},
		map[string]any{"type": "text", "text": "world"},
	}, "user", provideriface.SessionMediaContract{}, &mediaCount)
	if err != nil {
		t.Fatalf("build blocks: %v", err)
	}
	encoded := mustMarshalJSON(t, blocks)
	assertNotContains(t, encoded, `"cache_control"`)
}

func TestBuildAnthropicToolsSanitizesWebFetchRuntimeSchema(t *testing.T) {
	var webfetch tool.Definition
	for _, definition := range tool.NewRuntime(1).Definitions() {
		if definition.Name == "webfetch" {
			webfetch = definition
			break
		}
	}
	if webfetch.Name == "" {
		t.Fatal("webfetch definition not found")
	}

	tools, _, err := buildAnthropicTools([]provideriface.ToolDefinition{{
		Name:        webfetch.Name,
		Description: webfetch.Description,
		Parameters:  webfetch.Parameters,
	}})
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	if len(tools) != 1 || tools[0].OfTool == nil {
		t.Fatalf("expected one custom tool, got %#v", tools)
	}
	encoded := mustMarshalJSON(t, tools[0].OfTool.InputSchema)
	assertContains(t, encoded, `"type":"object"`)
	assertContains(t, encoded, `"properties"`)
	assertContains(t, encoded, `"additionalProperties":false`)
	assertNotContains(t, encoded, `"additionalProperties":true`)
	assertNotContains(t, encoded, `"oneOf"`)
}

func TestBuildAnthropicToolsUsesFullSchemaExtraFields(t *testing.T) {
	tools, _, err := buildAnthropicTools([]provideriface.ToolDefinition{{
		Name:        "webfetch",
		Description: "Fetch content",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"anyOf": []any{
						map[string]any{"type": "boolean"},
						map[string]any{
							"type":       "object",
							"properties": map[string]any{},
						},
					},
				},
			},
		},
	}})
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	if len(tools) != 1 || tools[0].OfTool == nil {
		t.Fatalf("expected one custom tool, got %#v", tools)
	}
	encoded := mustMarshalJSON(t, tools[0].OfTool.InputSchema)
	assertContains(t, encoded, `"type":"object"`)
	assertContains(t, encoded, `"properties"`)
	assertContains(t, encoded, `"additionalProperties":false`)
	assertNotContains(t, encoded, `"additionalProperties":true`)
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(encoded)
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %q to contain %q", haystack, needle)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("expected %q not to contain %q", haystack, needle)
	}
}
