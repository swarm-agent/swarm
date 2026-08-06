package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPinnedSnapshotExposesAnthropicFastOnlyForExplicitSupportedModel(t *testing.T) {
	records, _, err := decodeSwarmSnapshotRecords(pinnedSwarmSnapshotJSON, 1000, 2000, catalogSourcePinned, "")
	if err != nil {
		t.Fatalf("decode pinned Swarm snapshot: %v", err)
	}
	fastModels := make([]string, 0)
	for _, record := range records {
		if record.Provider != "anthropic" || !serviceTierListedForModel("fast", record) {
			continue
		}
		fastModels = append(fastModels, record.Model)
	}
	if !stringSlicesEqual(fastModels, []string{"claude-opus-4-8"}) {
		t.Fatalf("pinned Anthropic Fast Mode models = %#v, want only claude-opus-4-8", fastModels)
	}
}

func TestDecodeSwarmSnapshotRecordsMapsSnapshotFields(t *testing.T) {
	payload := snapshotPassthroughPayload()

	records, snapshot, err := decodeSwarmSnapshotRecords(payload, 1000, 2000, catalogSourcePinned, "etag-test")
	if err != nil {
		t.Fatalf("decodeSwarmSnapshotRecords returned error: %v", err)
	}
	if snapshot.SnapshotID != "snapshot-test" || snapshot.SnapshotVersion != "v-test" {
		t.Fatalf("snapshot metadata = %q/%q", snapshot.SnapshotID, snapshot.SnapshotVersion)
	}
	if len(records) != 6 {
		t.Fatalf("record count = %d, want exactly 6 valid snapshot records", len(records))
	}

	codex54 := records[0]
	if codex54.Provider != "codex" || codex54.Model != "gpt-5.4" || codex54.CatalogID != "codex/gpt-5.4" || codex54.DisplayName != "GPT 5.4" {
		t.Fatalf("codex gpt-5.4 identifiers not preserved: %+v", codex54)
	}
	if codex54.ContextWindow != 272000 || codex54.MaxOutputTokens != 128000 || !codex54.Reasoning {
		t.Fatalf("codex gpt-5.4 limits/reasoning = context %d max %d reasoning %v", codex54.ContextWindow, codex54.MaxOutputTokens, codex54.Reasoning)
	}
	if codex54.SourceSnapshotID != "snapshot-test" || codex54.SourceSnapshotVersion != "v-test" || codex54.Source != catalogSourcePinned {
		t.Fatalf("codex snapshot/source metadata not preserved: %+v", codex54)
	}
	var pricing map[string]any
	if err := json.Unmarshal(codex54.Pricing, &pricing); err != nil || pricing["currency"] != "USD" || pricing["input_price_per_million_tokens"].(float64) != 1.25 {
		t.Fatalf("codex pricing not preserved: pricing=%s err=%v", codex54.Pricing, err)
	}
	var thinking map[string]any
	if err := json.Unmarshal(codex54.Thinking, &thinking); err != nil || thinking["default_swarm_setting"] != "high" {
		t.Fatalf("codex thinking not preserved: thinking=%s err=%v", codex54.Thinking, err)
	}
	if !stringSlicesEqual(codex54.ThinkingOptions, []string{"off", "low", "medium", "high", "xhigh"}) || codex54.DefaultThinking != "high" || codex54.ThinkingProviderParameter != "reasoning_effort" {
		t.Fatalf("codex thinking metadata = %#v/%q/%q", codex54.ThinkingOptions, codex54.DefaultThinking, codex54.ThinkingProviderParameter)
	}
	if len(codex54.ThinkingMappings) != 2 || codex54.ThinkingMappings[0].SwarmSetting != "off" || codex54.ThinkingMappings[0].ProviderValue != "none" {
		t.Fatalf("codex thinking mappings not decoded from snapshot: %+v", codex54.ThinkingMappings)
	}
	if !stringSlicesEqual(codex54.ServiceTiers, []string{"standard", "fast"}) || codex54.DefaultServiceTier != "standard" {
		t.Fatalf("codex tiers/default = %#v/%q, want snapshot standard+fast/default", codex54.ServiceTiers, codex54.DefaultServiceTier)
	}
	if len(codex54.ServiceTierMappings) != 2 || codex54.ServiceTierMappings[1].Tier != "fast" || codex54.ServiceTierMappings[1].ProviderParameter != "service_tier" || codex54.ServiceTierMappings[1].ProviderValue != "priority" {
		t.Fatalf("codex service tier mappings not decoded from snapshot: %+v", codex54.ServiceTierMappings)
	}
	if len(codex54.ContextModes) != 1 || codex54.ContextModes[0].Mode != "1m" || codex54.ContextModes[0].ContextWindow != 333333 {
		t.Fatalf("codex context modes not decoded from snapshot metadata: %+v", codex54.ContextModes)
	}

	codex55 := records[1]
	if codex55.Provider != "codex" || codex55.Model != "gpt-5.5" {
		t.Fatalf("codex gpt-5.5 identifiers not preserved: %+v", codex55)
	}
	if codex55.ContextWindow != 444444 {
		t.Fatalf("codex gpt-5.5 context window = %d, want snapshot value 444444", codex55.ContextWindow)
	}

	openai := records[2]
	if openai.Provider != "openai" || openai.Model != "gpt-5.5" || openai.CatalogID != "openai/gpt-5.5" || openai.DisplayName != "GPT 5.5 API" {
		t.Fatalf("openai gpt-5.5 identifiers not preserved: %+v", openai)
	}
	if openai.ContextWindow != 0 || !openai.Reasoning {
		t.Fatalf("openai gpt-5.5 context/reasoning = %d/%v, want unknown context and reasoning", openai.ContextWindow, openai.Reasoning)
	}
	if !stringSlicesEqual(openai.ServiceTiers, []string{"standard", "priority", "flex"}) || openai.DefaultServiceTier != "standard" {
		t.Fatalf("openai tiers/default = %#v/%q, want synchronous tiers without batch", openai.ServiceTiers, openai.DefaultServiceTier)
	}
	if len(openai.ServiceTierMappings) != 3 || openai.ServiceTierMappings[2].Tier != "flex" || openai.ServiceTierMappings[2].ProviderParameter != "service_tier" || openai.ServiceTierMappings[2].ProviderValue != "flex" {
		t.Fatalf("openai service tier mappings not decoded without async batch: %+v", openai.ServiceTierMappings)
	}

	fireworks := records[4]
	if fireworks.Provider != "fireworks" || fireworks.Model != "glm-5p1" || fireworks.CatalogID != "fireworks/glm-5p1" || fireworks.DisplayName != "GLM 5.1" {
		t.Fatalf("fireworks base identifiers not preserved: %+v", fireworks)
	}
	if fireworks.ContextWindow != 202752 || fireworks.MaxOutputTokens != 0 {
		t.Fatalf("fireworks limits = context %d max %d", fireworks.ContextWindow, fireworks.MaxOutputTokens)
	}
	var fireworksPricing map[string]any
	if err := json.Unmarshal(fireworks.Pricing, &fireworksPricing); err != nil || fireworksPricing["input_price_per_million_tokens"].(float64) != 2.8 {
		t.Fatalf("fireworks pricing not preserved: pricing=%s err=%v", fireworks.Pricing, err)
	}
	var fireworksThinking map[string]any
	if err := json.Unmarshal(fireworks.Thinking, &fireworksThinking); err != nil || fireworksThinking["default_swarm_setting"] != "medium" {
		t.Fatalf("fireworks thinking not preserved: thinking=%s err=%v", fireworks.Thinking, err)
	}
	if !stringSlicesEqual(fireworks.ThinkingOptions, []string{"off", "medium"}) || fireworks.DefaultThinking != "medium" {
		t.Fatalf("fireworks thinking metadata = %#v/%q, want off+medium/default medium", fireworks.ThinkingOptions, fireworks.DefaultThinking)
	}
	if !stringSlicesEqual(fireworks.ServiceTiers, []string{"standard", "priority"}) || fireworks.DefaultServiceTier != "standard" {
		t.Fatalf("fireworks base tiers/default = %#v/%q, want snapshot tiers/default", fireworks.ServiceTiers, fireworks.DefaultServiceTier)
	}
	if len(fireworks.ThinkingMappings) != 2 || fireworks.ThinkingMappings[1].SwarmSetting != "medium" || fireworks.ThinkingMappings[1].ProviderParameter != "reasoning_effort" || fireworks.ThinkingMappings[1].ProviderValue != "medium" {
		t.Fatalf("fireworks thinking mappings not decoded from snapshot: %+v", fireworks.ThinkingMappings)
	}
	if len(fireworks.ServiceTierMappings) != 2 || fireworks.ServiceTierMappings[1].Tier != "priority" || fireworks.ServiceTierMappings[1].ProviderParameter != "service_tier" || fireworks.ServiceTierMappings[1].ProviderValue != "priority" || fireworks.ServiceTierMappings[1].RequestModelPath != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("fireworks service tier mappings not decoded from snapshot: %+v", fireworks.ServiceTierMappings)
	}
	var fireworksSpecific map[string]any
	if err := json.Unmarshal(fireworks.ProviderSpecific, &fireworksSpecific); err != nil {
		t.Fatalf("fireworks provider_specific not preserved: %v", err)
	}
	fireworksData := fireworksSpecific["fireworks"].(map[string]any)
	if fireworksData["resource_name"] != "accounts/fireworks/models/glm-5p1" {
		t.Fatalf("fireworks resource_name = %v", fireworksData["resource_name"])
	}

	anthropicOpus := records[3]
	if anthropicOpus.Provider != "anthropic" || anthropicOpus.Model != "claude-opus-4-8" {
		t.Fatalf("anthropic opus identifiers not preserved from snapshot: %+v", anthropicOpus)
	}
	if !stringSlicesEqual(anthropicOpus.ServiceTiers, []string{"standard", "priority"}) {
		t.Fatalf("anthropic opus visible service tiers = %#v, want standard+priority", anthropicOpus.ServiceTiers)
	}
	if len(anthropicOpus.ServiceTierMappings) != 3 || anthropicOpus.ServiceTierMappings[2].Tier != "fast" || anthropicOpus.ServiceTierMappings[2].ProviderParameter != "speed" || anthropicOpus.ServiceTierMappings[2].ProviderValue != "fast" || anthropicOpus.ServiceTierMappings[2].BetaHeader != "fast-mode-2026-02-01" {
		t.Fatalf("anthropic provider fast mode mapping not decoded distinctly from priority: %+v", anthropicOpus.ServiceTierMappings)
	}
	if !serviceTierListedForModel("fast", anthropicOpus) || !serviceTierListedForModel("priority", anthropicOpus) {
		t.Fatalf("anthropic explicit fast/priority tiers not listed from snapshot mappings: %+v", anthropicOpus.ServiceTierMappings)
	}
	priorityOnly := anthropicOpus
	priorityOnly.ServiceTierMappings = []pebblestore.ModelCatalogServiceTierMapping{{Tier: "priority", SwarmSetting: "fast", ProviderParameter: "service_tier", ProviderValue: "auto"}}
	if serviceTierListedForModel("fast", priorityOnly) {
		t.Fatalf("anthropic priority mapping swarm_setting must not infer Fast Mode support: %+v", priorityOnly.ServiceTierMappings)
	}

	fast := records[5]
	if fast.Provider != "fireworks" || fast.Model != "glm-5p1-fast" || fast.CatalogID != "fireworks/glm-5p1-fast" || fast.DisplayName != "GLM 5.1 Fast" {
		t.Fatalf("fireworks fast identifiers not preserved from snapshot: %+v", fast)
	}
	if !stringSlicesEqual(fast.ThinkingOptions, []string{"off", "medium"}) || fast.DefaultThinking != "medium" {
		t.Fatalf("fireworks fast thinking metadata = %#v/%q, want off+medium/default medium", fast.ThinkingOptions, fast.DefaultThinking)
	}
	if !stringSlicesEqual(fast.ServiceTiers, []string{"standard"}) || fast.DefaultServiceTier != "standard" {
		t.Fatalf("fireworks fast tiers/default = %#v/%q, want snapshot standard/default", fast.ServiceTiers, fast.DefaultServiceTier)
	}
	if len(fast.ServiceTierMappings) != 1 || fast.ServiceTierMappings[0].Tier != "standard" || fast.ServiceTierMappings[0].ProviderParameter != "" || fast.ServiceTierMappings[0].ProviderValue != "" || fast.ServiceTierMappings[0].RequestModelPath != "accounts/fireworks/routers/glm-5p1-fast" {
		t.Fatalf("fireworks fast service tier mappings not decoded from snapshot: %+v", fast.ServiceTierMappings)
	}
	var fastThinking map[string]any
	if err := json.Unmarshal(fast.Thinking, &fastThinking); err != nil || fastThinking["default_swarm_setting"] != "medium" {
		t.Fatalf("fireworks fast thinking not preserved: thinking=%s err=%v", fast.Thinking, err)
	}
	var fastSpecific map[string]any
	if err := json.Unmarshal(fast.ProviderSpecific, &fastSpecific); err != nil {
		t.Fatalf("fast provider_specific not preserved: %v", err)
	}
	fastData := fastSpecific["fireworks"].(map[string]any)
	if fastData["resource_name"] != "accounts/fireworks/routers/glm-5p1-fast" {
		t.Fatalf("fireworks fast resource_name = %v", fastData["resource_name"])
	}
	fastServing := fastData["serving"].(map[string]any)
	fastStandard := fastServing["standard"].(map[string]any)
	if fastStandard["request_model_path"] != "accounts/fireworks/routers/glm-5p1-fast" {
		t.Fatalf("fireworks fast request_model_path = %v", fastStandard["request_model_path"])
	}
}

func TestDecodeSwarmSnapshotRecordsPreservesReviewedMediaAndDeniesOthers(t *testing.T) {
	records, _, err := decodeSwarmSnapshotRecords(pinnedSwarmSnapshotJSON, 1000, 2000, catalogSourcePinned, "etag-media")
	if err != nil {
		t.Fatalf("decode embedded snapshot media: %v", err)
	}
	byProviderModel := make(map[string]pebblestore.ModelCatalogRecord, len(records))
	for _, record := range records {
		byProviderModel[record.Provider+"/"+record.Model] = record
	}

	openai := byProviderModel["openai/gpt-4.1"]
	if openai.Media == nil || openai.Media.ProviderSurface != "responses_api" || openai.Media.CredentialSurface != "openai_api_key" {
		t.Fatalf("OpenAI media surface not preserved: %+v", openai.Media)
	}
	assertMediaDirection(t, openai.Media.Inputs, "image", pebblestore.ModelCatalogMediaStateSupported, pebblestore.ModelCatalogMediaSemanticsNative, []string{"image/gif", "image/jpeg", "image/png", "image/webp"}, nil)
	assertMediaDirection(t, openai.Media.Inputs, "file", pebblestore.ModelCatalogMediaStateSupported, pebblestore.ModelCatalogMediaSemanticsProviderProcessed, nil, []string{"pdf", "presentation", "rich_document", "spreadsheet", "text_and_code"})
	assertMediaDirection(t, openai.Media.Inputs, "pdf", pebblestore.ModelCatalogMediaStateSupported, pebblestore.ModelCatalogMediaSemanticsProviderProcessed, []string{"application/pdf"}, []string{"pdf"})
	assertMediaDirection(t, openai.Media.Outputs, "image", pebblestore.ModelCatalogMediaStateUnsupported, pebblestore.ModelCatalogMediaSemanticsNative, nil, nil)

	codex := byProviderModel["codex/gpt-5.4"]
	if codex.Media == nil || codex.Media.ProviderSurface != "chatgpt_codex" || codex.Media.CredentialSurface != "codex_oauth" {
		t.Fatalf("Codex media surface not preserved: %+v", codex.Media)
	}
	assertMediaDirection(t, codex.Media.Inputs, "image", pebblestore.ModelCatalogMediaStateSupported, pebblestore.ModelCatalogMediaSemanticsNative, []string{"image/gif", "image/jpeg", "image/png", "image/webp"}, nil)
	assertMediaDirection(t, codex.Media.Inputs, "file", pebblestore.ModelCatalogMediaStateUnknown, pebblestore.ModelCatalogMediaSemanticsClientProcessed, nil, nil)
	assertMediaDirection(t, codex.Media.Inputs, "pdf", pebblestore.ModelCatalogMediaStateUnknown, pebblestore.ModelCatalogMediaSemanticsClientProcessed, []string{"application/pdf"}, []string{"pdf"})

	for _, record := range records {
		if !catalogMediaProviderEnabled(record.Provider) && record.Media != nil {
			t.Fatalf("unreviewed provider %q unexpectedly has media capability: %+v", record.Provider, record.Media)
		}
		if record.Provider != "openai" && record.Provider != "codex" && record.Media != nil {
			for _, input := range record.Media.Inputs {
				if input.Modality != "image" {
					t.Fatalf("reviewed provider %q hydrated non-image input: %+v", record.Provider, record.Media)
				}
			}
			if len(record.Media.Outputs) != 0 {
				t.Fatalf("reviewed provider %q hydrated media output: %+v", record.Provider, record.Media)
			}
		}
	}
}

func TestCatalogMediaSurfaceAliasesRemainProviderScoped(t *testing.T) {
	for _, test := range []struct{ provider, catalogSurface, runtimeSurface string }{
		{"google", "generate_content", provideriface.MediaProviderSurfaceGoogleGenerateContent},
		{"anthropic", "messages", provideriface.MediaProviderSurfaceAnthropicMessages},
		{"fireworks", "chat_completions", provideriface.MediaProviderSurfaceFireworksChatCompletions},
		{"fireworks", "serverless_chat_completions", provideriface.MediaProviderSurfaceFireworksChatCompletions},
		{"openrouter", "chat_completions", provideriface.MediaProviderSurfaceOpenRouterChatCompletions},
	} {
		if !catalogMediaSurfaceAliasMatches(test.provider, test.catalogSurface, test.runtimeSurface) {
			t.Fatalf("provider-scoped surface alias rejected: %+v", test)
		}
	}
	if catalogMediaSurfaceAliasMatches("google", "messages", provideriface.MediaProviderSurfaceGoogleGenerateContent) {
		t.Fatal("cross-provider surface alias matched Google")
	}
	if catalogMediaSurfaceAliasMatches("openrouter", "serverless_chat_completions", provideriface.MediaProviderSurfaceOpenRouterChatCompletions) {
		t.Fatal("Fireworks serverless surface alias leaked to OpenRouter")
	}
	var googleModel swarmSnapshotModel
	if err := json.Unmarshal([]byte(`{"provider_id":"google","model_id":"vision","capabilities":{"supports_image_input":true}}`), &googleModel); err != nil {
		t.Fatalf("decode Google model fixture: %v", err)
	}
	media, err := snapshotModelMediaCapabilities(swarmSnapshot{}, "google", googleModel)
	if err != nil || media == nil {
		t.Fatalf("hydrate Google MIME fallback: media=%+v err=%v", media, err)
	}
	assertMediaDirection(t, media.Inputs, "image", pebblestore.ModelCatalogMediaStateSupported, pebblestore.ModelCatalogMediaSemanticsNative, []string{"image/heic", "image/heif", "image/jpeg", "image/png", "image/webp"}, nil)
}

func TestSnapshotModelMediaCapabilitiesHydratesReviewedImageProvidersOnly(t *testing.T) {
	for _, test := range []struct {
		provider, surface, credential string
	}{
		{"google", "gemini_generate_content", "google_api_key"},
		{"anthropic", "anthropic_messages", "anthropic_api_key"},
		{"fireworks", "fireworks_chat_completions", "fireworks_api_key"},
		{"openrouter", "openrouter_chat_completions", "openrouter_api_key"},
	} {
		var model swarmSnapshotModel
		modelJSON := []byte(`{"provider_id":"` + test.provider + `","model_id":"vision-model","capabilities":{"supports_image_input":true},"provider_specific":{"` + test.provider + `":{"multimodal":{"api_surface":"` + test.surface + `","image":{"input":true,"supported_input_media_types":["image/png"]}}}}}`)
		if err := json.Unmarshal(modelJSON, &model); err != nil {
			t.Fatalf("decode %s fixture: %v", test.provider, err)
		}
		media, err := snapshotModelMediaCapabilities(swarmSnapshot{}, test.provider, model)
		if err != nil {
			t.Fatalf("hydrate %s media: %v", test.provider, err)
		}
		if media == nil || media.ProviderSurface != test.surface || media.CredentialSurface != test.credential {
			t.Fatalf("%s media surface = %+v", test.provider, media)
		}
		assertMediaDirection(t, media.Inputs, "image", pebblestore.ModelCatalogMediaStateSupported, pebblestore.ModelCatalogMediaSemanticsNative, []string{"image/png"}, nil)
	}
	unreviewed, err := snapshotModelMediaCapabilities(swarmSnapshot{}, "exa", swarmSnapshotModel{ProviderID: "exa", ModelID: "vision"})
	if err != nil || unreviewed != nil {
		t.Fatalf("unreviewed provider media = %+v err=%v", unreviewed, err)
	}
}

func TestSnapshotModelMediaCapabilitiesRejectsReviewedSurfaceContradiction(t *testing.T) {
	var model swarmSnapshotModel
	if err := json.Unmarshal([]byte(`{"provider_id":"google","model_id":"vision","capabilities":{"supports_image_input":true},"provider_specific":{"google":{"multimodal":{"api_surface":"messages","image":{"input":true,"supported_input_media_types":["image/png"]}}}}}`), &model); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if _, err := snapshotModelMediaCapabilities(swarmSnapshot{}, "google", model); err == nil {
		t.Fatal("contradictory Google catalog surface was accepted")
	}
}

func TestDecodeSwarmSnapshotRecordsRejectsContradictoryReviewedMedia(t *testing.T) {
	payload := []byte(`{
		"snapshot_id":"snapshot-contradictory",
		"snapshot_version":"v-contradictory",
		"generated_at":"2026-07-27T00:00:00Z",
		"definitions":{"openai":{"multimodal":{"input_modalities":["text","image"],"unsupported_native_modalities":["image_input"]}}},
		"models":[{
			"provider_id":"openai",
			"model_id":"gpt-test",
			"capabilities":{"supports_text_input":true,"supports_text_output":true,"supports_image_input":true},
			"limits":{"context_window_tokens":1000},
			"provider_specific":{"openai":{"multimodal":{"api_surface":"responses_api","image":{"input":true,"supported_input_media_types":["image/png"]}}}}
		}]
	}`)
	if _, _, err := decodeSwarmSnapshotRecords(payload, 1000, 2000, catalogSourceLive, ""); err == nil {
		t.Fatalf("expected contradictory OpenAI media data to fail hydration")
	}
}

func TestCatalogReviewedMediaRoundTripsWithMatchingProvenanceAndAliases(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))
	if _, err := catalog.replaceSnapshotLocked(pinnedSwarmSnapshotJSON, catalogSourceLive, "test://snapshot", "test://version", "etag-live", "version-etag", "test"); err != nil {
		t.Fatalf("replace snapshot: %v", err)
	}

	for _, lookupCase := range []struct {
		provider, model, wantSurface, wantCredential string
	}{
		{"openai-api", "gpt-4.1", "responses_api", "openai_api_key"},
		{"codex-oauth", "gpt-5.4", "chatgpt_codex", "codex_oauth"},
	} {
		lookup, err := catalog.Get(lookupCase.provider, lookupCase.model)
		if err != nil {
			t.Fatalf("lookup %s: %v", lookupCase.provider, err)
		}
		if !lookup.Found || lookup.Meta == nil || lookup.Record.Media == nil {
			t.Fatalf("lookup %s missing persisted media/meta: %+v", lookupCase.provider, lookup)
		}
		if lookup.Record.Media.ProviderSurface != lookupCase.wantSurface || lookup.Record.Media.CredentialSurface != lookupCase.wantCredential {
			t.Fatalf("lookup %s surface = %+v", lookupCase.provider, lookup.Record.Media)
		}
		if lookup.Meta.SnapshotID != lookup.Record.SourceSnapshotID || lookup.Meta.SnapshotVersion != lookup.Record.SourceSnapshotVersion || lookup.Meta.Source != catalogSourceLive {
			t.Fatalf("lookup %s provenance mismatch: record=%+v meta=%+v", lookupCase.provider, lookup.Record, lookup.Meta)
		}
	}
}

func assertMediaDirection(t *testing.T, values []pebblestore.ModelCatalogMediaDirection, modality, state, semantics string, mimeTypes, fileTypes []string) {
	t.Helper()
	for _, value := range values {
		if value.Modality != modality {
			continue
		}
		if value.State != state || value.Semantics != semantics || !stringSlicesEqual(value.MIMETypes, mimeTypes) || !stringSlicesEqual(value.FileTypes, fileTypes) {
			t.Fatalf("media %s = %+v, want state=%q semantics=%q MIME=%#v files=%#v", modality, value, state, semantics, mimeTypes, fileTypes)
		}
		return
	}
	t.Fatalf("media direction %q missing from %+v", modality, values)
}

func TestCatalogGetListPreserveSnapshotFireworksFastRecord(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))

	if _, err := catalog.replaceSnapshotLocked(snapshotPassthroughPayload(), catalogSourcePinned, pinnedCatalogSourceURL, pinnedCatalogVersionURL, "etag-test", "", "test"); err != nil {
		t.Fatalf("replace snapshot: %v", err)
	}

	lookup, err := catalog.Get("fireworks", "glm-5p1-fast")
	if err != nil {
		t.Fatalf("lookup fast record: %v", err)
	}
	if !lookup.Found {
		t.Fatalf("fast record not found")
	}
	if lookup.Record.Model != "glm-5p1-fast" || lookup.Record.CatalogID != "fireworks/glm-5p1-fast" || lookup.Record.DefaultServiceTier != "standard" {
		t.Fatalf("lookup fast record was rewritten instead of preserved: %+v", lookup.Record)
	}
	var providerSpecific map[string]any
	if err := json.Unmarshal(lookup.Record.ProviderSpecific, &providerSpecific); err != nil {
		t.Fatalf("decode lookup provider_specific: %v", err)
	}
	fireworksData := providerSpecific["fireworks"].(map[string]any)
	if fireworksData["resource_name"] != "accounts/fireworks/routers/glm-5p1-fast" {
		t.Fatalf("lookup provider_specific resource_name = %v", fireworksData["resource_name"])
	}

	records, err := catalog.List("fireworks", 100)
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("fireworks list count = %d, want the 2 snapshot fireworks records", len(records))
	}
	if records[0].Model != "glm-5p1" || records[1].Model != "glm-5p1-fast" {
		t.Fatalf("fireworks list models = %q/%q, want snapshot model IDs", records[0].Model, records[1].Model)
	}
}

func TestEnsureBootDefaultsSeedsPinnedSnapshotOffline(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))
	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatalf("EnsureBootDefaults returned error: %v", err)
	}

	meta, ok, err := catalog.Meta()
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if !ok {
		t.Fatalf("meta missing after pinned seed")
	}
	if meta.Source != catalogSourcePinned || meta.RecordCount == 0 || meta.SnapshotID == "" || meta.PinnedSnapshotVersion == "" {
		t.Fatalf("unexpected pinned meta: %+v", meta)
	}
	lookup, err := catalog.Get("codex", "gpt-5.4")
	if err != nil {
		t.Fatalf("catalog lookup: %v", err)
	}
	if !lookup.Found || lookup.Record.ContextWindow <= 0 || !lookup.Record.Reasoning {
		t.Fatalf("expected pinned codex record with context/reasoning, got %+v", lookup)
	}

	for _, modelID := range []string{"glm-5p2", "glm-5p2-fast"} {
		lookup, err := catalog.Get("fireworks", modelID)
		if err != nil {
			t.Fatalf("catalog lookup for %s: %v", modelID, err)
		}
		if !lookup.Found {
			t.Fatalf("expected pinned Fireworks %s record", modelID)
		}
		if !stringSlicesEqual(lookup.Record.ThinkingOptions, []string{"off", "high", "xhigh"}) || lookup.Record.DefaultThinking != "xhigh" {
			t.Fatalf("Fireworks %s thinking metadata = %#v/%q, want off/high/xhigh default xhigh", modelID, lookup.Record.ThinkingOptions, lookup.Record.DefaultThinking)
		}
		if len(lookup.Record.ThinkingMappings) != 3 || lookup.Record.ThinkingMappings[2].SwarmSetting != "xhigh" || lookup.Record.ThinkingMappings[2].ProviderValue != "max" || lookup.Record.ThinkingMappings[2].EffectiveProviderValue != "max" {
			t.Fatalf("Fireworks %s thinking mappings = %+v, want xhigh mapped to provider max", modelID, lookup.Record.ThinkingMappings)
		}
	}
	anthropic, err := catalog.Get("anthropic", "claude-sonnet-5")
	if err != nil {
		t.Fatalf("catalog lookup for claude-sonnet-5: %v", err)
	}
	if !anthropic.Found {
		t.Fatalf("expected pinned Anthropic claude-sonnet-5 record")
	}
	if !stringSlicesEqual(anthropic.Record.ServiceTiers, []string{"standard"}) {
		t.Fatalf("Anthropic service tiers = %#v, want standard without batch", anthropic.Record.ServiceTiers)
	}
	for _, mapping := range anthropic.Record.ServiceTierMappings {
		if mapping.Tier == "batch" || mapping.SwarmSetting == "batch" {
			t.Fatalf("Anthropic service tier mappings should not expose batch: %+v", anthropic.Record.ServiceTierMappings)
		}
	}
	if !stringSlicesEqual(anthropic.Record.ThinkingOptions, []string{"off", "low", "medium", "high", "xhigh"}) || anthropic.Record.DefaultThinking != "high" || anthropic.Record.ThinkingProviderParameter != "thinking.type + output_config.effort" {
		t.Fatalf("Anthropic thinking metadata = %#v/%q/%q", anthropic.Record.ThinkingOptions, anthropic.Record.DefaultThinking, anthropic.Record.ThinkingProviderParameter)
	}
	if len(anthropic.Record.ThinkingMappings) != 5 || anthropic.Record.ThinkingMappings[2].Behavior != "effort" || anthropic.Record.ThinkingMappings[2].ProviderValue != "medium" {
		t.Fatalf("Anthropic thinking mappings should preserve snapshot effort mappings: %+v", anthropic.Record.ThinkingMappings)
	}
}

func TestEnsureBootDefaultsRefreshesStalePersistedSnapshot(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))
	if err := catalog.store.SetRecord(pebblestore.ModelCatalogRecord{
		Provider:              "codex",
		Model:                 "gpt-5.4",
		ContextWindow:         200000,
		Source:                catalogSourceLive,
		SourceSnapshotID:      "old-snapshot",
		SourceSnapshotVersion: "old-version",
	}); err != nil {
		t.Fatalf("seed old record: %v", err)
	}
	if err := catalog.store.SetMeta(pebblestore.ModelCatalogMeta{
		Source:          catalogSourceLive,
		SourceURL:       "test://old-snapshot",
		SnapshotURL:     "test://old-snapshot",
		VersionURL:      "test://old-version",
		SnapshotID:      "old-snapshot",
		SnapshotVersion: "old-version",
		GeneratedAt:     "2026-01-01T00:00:00Z",
		RecordCount:     1,
		ModelCount:      1,
	}); err != nil {
		t.Fatalf("seed old meta: %v", err)
	}

	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatalf("EnsureBootDefaults returned error: %v", err)
	}
	meta, ok, err := catalog.Meta()
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if !ok || meta.Source != catalogSourcePinned || meta.SnapshotID == "old-snapshot" || meta.PinnedSnapshotVersion == "" {
		t.Fatalf("expected stale persisted meta to be replaced by pinned snapshot, got ok=%v meta=%+v", ok, meta)
	}
	lookup, err := catalog.Get("codex", "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("lookup refreshed Codex snapshot record: %v", err)
	}
	if !lookup.Found || lookup.Record.Source != catalogSourcePinned || lookup.Record.SourceSnapshotID == "old-snapshot" {
		t.Fatalf("expected embedded GPT-5.6 Codex record after stale refresh, got %+v", lookup)
	}
}

func TestEnsureBootDefaultsPreservesNewerPersistedLiveSnapshot(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))
	if err := catalog.store.SetRecord(pebblestore.ModelCatalogRecord{
		Provider:              "codex",
		Model:                 "gpt-future",
		ContextWindow:         300000,
		Source:                catalogSourceLive,
		SourceSnapshotID:      "future-snapshot",
		SourceSnapshotVersion: "future-version",
	}); err != nil {
		t.Fatalf("seed future record: %v", err)
	}
	if err := catalog.store.SetMeta(pebblestore.ModelCatalogMeta{
		Source:          catalogSourceLive,
		SourceURL:       "test://future-snapshot",
		SnapshotURL:     "test://future-snapshot",
		VersionURL:      "test://future-version",
		SnapshotID:      "future-snapshot",
		SnapshotVersion: "future-version",
		GeneratedAt:     "2027-01-01T00:00:00Z",
		RecordCount:     1,
		ModelCount:      1,
	}); err != nil {
		t.Fatalf("seed future meta: %v", err)
	}

	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatalf("EnsureBootDefaults returned error: %v", err)
	}
	meta, ok, err := catalog.Meta()
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if !ok || meta.Source != catalogSourceLive || meta.SnapshotID != "future-snapshot" || meta.PinnedSnapshotVersion == "" {
		t.Fatalf("expected newer live snapshot with current pinned metadata, got ok=%v meta=%+v", ok, meta)
	}
	lookup, err := catalog.Get("codex", "gpt-future")
	if err != nil {
		t.Fatalf("lookup future record: %v", err)
	}
	if !lookup.Found || lookup.Record.SourceSnapshotID != "future-snapshot" {
		t.Fatalf("expected future live record to remain authoritative, got %+v", lookup)
	}
}

func TestRefreshUsesSnapshotVersionAndSkipsUnchangedSnapshot(t *testing.T) {
	versionBody := []byte(`{
		"snapshot_id":"snapshot-live",
		"snapshot_version":"v-live",
		"snapshot_schema_version":"2026-07-01.1",
		"generated_at":"2026-07-01T00:00:00Z",
		"model_count":1,
		"provider_count":1,
		"hydrated_provider_count":1,
		"snapshot_url":"/v1/snapshot.json"
	}`)
	snapshotBody := []byte(`{
		"snapshot_id":"snapshot-live",
		"snapshot_version":"v-live",
		"snapshot_schema_version":"2026-07-01.1",
		"generated_at":"2026-07-01T00:00:00Z",
		"model_count":1,
		"provider_count":1,
		"hydrated_provider_count":1,
		"models":[{
			"catalog_id":"anthropic/claude-test",
			"provider_id":"anthropic",
			"model_id":"claude-test",
			"capabilities":{"supports_reasoning":true},
			"limits":{"context_window_tokens":200000,"max_output_tokens":64000}
		}]
	}`)
	versionRequests := 0
	snapshotRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/snapshot-version.json":
			versionRequests++
			w.Header().Set("ETag", "version-etag")
			_, _ = w.Write(versionBody)
		case "/v1/snapshot.json":
			snapshotRequests++
			w.Header().Set("ETag", "snapshot-etag")
			_, _ = w.Write(snapshotBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))
	catalog.sourceURL = server.URL + "/v1/snapshot.json"
	catalog.versionURL = server.URL + "/v1/snapshot-version.json"
	if err := catalog.EnsureBootDefaults(); err != nil {
		t.Fatalf("EnsureBootDefaults returned error: %v", err)
	}

	first, err := catalog.Refresh(context.Background())
	if err != nil {
		t.Fatalf("first refresh returned error: %v", err)
	}
	if first.SnapshotID != "snapshot-live" || first.RecordCount != 1 || snapshotRequests != 1 {
		t.Fatalf("unexpected first refresh result=%+v snapshotRequests=%d", first, snapshotRequests)
	}
	lookup, err := catalog.Get("anthropic", "claude-test")
	if err != nil {
		t.Fatalf("lookup live record: %v", err)
	}
	if !lookup.Found || lookup.Record.ContextWindow != 200000 || lookup.Record.MaxOutputTokens != 64000 {
		t.Fatalf("live lookup = %+v", lookup)
	}

	second, err := catalog.Refresh(context.Background())
	if err != nil {
		t.Fatalf("second refresh returned error: %v", err)
	}
	if !second.NotModified || !second.UsedCache {
		t.Fatalf("second refresh should use cached unchanged snapshot: %+v", second)
	}
	if versionRequests != 2 || snapshotRequests != 1 {
		t.Fatalf("versionRequests=%d snapshotRequests=%d, want 2/1", versionRequests, snapshotRequests)
	}
}

func TestRefreshFailureRetainsReviewedMediaAndMarksCacheFallback(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))
	if _, err := catalog.replaceSnapshotLocked(pinnedSwarmSnapshotJSON, catalogSourceLive, "test://cached", "test://version", "cached-etag", "", "test"); err != nil {
		t.Fatalf("seed valid live snapshot: %v", err)
	}
	catalog.versionURL = "http://127.0.0.1:1/unavailable"
	catalog.client = &http.Client{}

	result, err := catalog.Refresh(context.Background())
	if err == nil {
		t.Fatalf("expected refresh failure")
	}
	if !result.UsedCache || !result.UsingCacheFallback {
		t.Fatalf("refresh failure did not report cache fallback: %+v", result)
	}
	lookup, err := catalog.Get("codex", "gpt-5.4")
	if err != nil {
		t.Fatalf("lookup cached Codex media: %v", err)
	}
	if !lookup.Found || lookup.Meta == nil || !lookup.Meta.UsingCacheFallback || lookup.Record.Media == nil || lookup.Record.Media.ProviderSurface != "chatgpt_codex" {
		t.Fatalf("cached Codex media/provenance not retained: %+v", lookup)
	}
}

func TestDecodeSwarmSnapshotRecordsUnknownFieldsDoNotBroadenReviewedMedia(t *testing.T) {
	payload := []byte(`{
		"snapshot_id":"snapshot-unknown",
		"snapshot_version":"v-unknown",
		"generated_at":"2026-07-27T00:00:00Z",
		"definitions":{"openai":{"multimodal":{"future_modality":["hologram"]}}},
		"models":[{
			"provider_id":"openai",
			"model_id":"gpt-unknown",
			"capabilities":{"supports_text_input":true,"supports_text_output":true},
			"limits":{"context_window_tokens":1000},
			"provider_specific":{"openai":{"multimodal":{"api_surface":"responses_api","future_detail":{"input":true}}}}
		}]
	}`)
	records, _, err := decodeSwarmSnapshotRecords(payload, 1000, 2000, catalogSourceLive, "")
	if err != nil {
		t.Fatalf("unknown additive media fields should decode without broadening: %v", err)
	}
	if len(records) != 1 || records[0].Media == nil {
		t.Fatalf("expected one explicit unknown media record: %+v", records)
	}
	for _, input := range records[0].Media.Inputs {
		if input.Modality != "text" && input.State == pebblestore.ModelCatalogMediaStateSupported {
			t.Fatalf("unknown field broadened media capability: %+v", records[0].Media)
		}
	}
}

func TestProviderLevelRecommendationsHydrateRecommendedDefaults(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	catalog := NewCatalogService(pebblestore.NewModelCatalogStore(store))
	if _, err := catalog.replaceSnapshotLocked(providerRecommendationsPayload(), catalogSourceLive, "test://snapshot", "test://version", "", "", "test"); err != nil {
		t.Fatalf("replace snapshot: %v", err)
	}

	main, plan, utility, ok, err := catalog.RecommendedDefaults("anthropic")
	if err != nil {
		t.Fatalf("recommended defaults: %v", err)
	}
	if !ok {
		t.Fatalf("expected provider-level recommendations")
	}
	if main.Model != "claude-sonnet-5" || plan.Model != "claude-opus-4-8" || utility.Model != "claude-sonnet-5" {
		t.Fatalf("recommended models = main %q plan %q utility %q", main.Model, plan.Model, utility.Model)
	}
	mainRec := main.Recommendations[0]
	if mainRec.Role != "auto" || mainRec.Thinking != "high" {
		t.Fatalf("main recommendation = %+v, want auto/high", mainRec)
	}
	planRec := plan.Recommendations[0]
	if planRec.Role != "plan" || planRec.Thinking != "xhigh" {
		t.Fatalf("plan recommendation = %+v, want plan/xhigh", planRec)
	}
	utilityRec := utility.Recommendations[1]
	if utilityRec.Role != "utility" || utilityRec.Thinking != "medium" {
		t.Fatalf("utility recommendation = %+v, want utility/medium", utilityRec)
	}
	subagents, ok, err := catalog.RecommendedRoleDefaults("anthropic", "compact", "finder", "coder", "designer")
	if err != nil || !ok {
		t.Fatalf("subagent recommendations ok=%v err=%v", ok, err)
	}
	for role, modelID := range map[string]string{"compact": "claude-haiku-4-5", "finder": "claude-sonnet-5", "coder": "claude-opus-4-8", "designer": "claude-sonnet-5"} {
		if subagents[role].Model != modelID {
			t.Fatalf("%s recommendation model = %q, want %q", role, subagents[role].Model, modelID)
		}
	}
}

func providerRecommendationsPayload() []byte {
	return []byte(`{
		"snapshot_schema_version":"2026-07-01.1",
		"snapshot_id":"snapshot-provider-recs",
		"snapshot_version":"v-provider-recs",
		"generated_at":"2026-07-01T00:00:00Z",
		"model_count":3,
		"provider_count":1,
		"hydrated_provider_count":1,
		"recommendations":{
			"anthropic":{
				"plan":{"model":"claude-opus-4-8","thinking":"xhigh"},
				"auto":{"model":"claude-sonnet-5","thinking":"high"},
				"utility":{"model":"claude-sonnet-5","thinking":"medium"},
				"compact":{"model":"claude-haiku-4-5","thinking":"off"},
				"finder":{"model":"claude-sonnet-5","thinking":"medium"},
				"coder":{"model":"claude-opus-4-8","thinking":"high"},
				"designer":{"model":"claude-sonnet-5","thinking":"high"}
			}
		},
		"models":[
			{
				"catalog_id":"anthropic/claude-opus-4-8",
				"provider_id":"anthropic",
				"model_id":"claude-opus-4-8",
				"display_name":"Claude Opus 4.8",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":200000,"max_output_tokens":64000}
			},
			{
				"catalog_id":"anthropic/claude-haiku-4-5",
				"provider_id":"anthropic",
				"model_id":"claude-haiku-4-5",
				"display_name":"Claude Haiku 4.5",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":200000,"max_output_tokens":64000}
			},
			{
				"catalog_id":"anthropic/claude-sonnet-5",
				"provider_id":"anthropic",
				"model_id":"claude-sonnet-5",
				"display_name":"Claude Sonnet 5",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":200000,"max_output_tokens":64000}
			}
		]
	}`)
}

func snapshotPassthroughPayload() []byte {
	return []byte(`{
		"snapshot_schema_version":"2026-07-01.1",
		"snapshot_id":"snapshot-test",
		"snapshot_version":"v-test",
		"generated_at":"2026-07-01T00:00:00Z",
		"model_count":6,
		"provider_count":2,
		"hydrated_provider_count":2,
		"models":[
			{
				"catalog_id":"codex/gpt-5.4",
				"provider_id":"codex",
				"provider_display_name":"OpenAI Codex (OAuth)",
				"model_id":"gpt-5.4",
				"display_name":"GPT 5.4",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":333333,"max_output_tokens":128000},
				"pricing":{"currency":"USD","input_price_per_million_tokens":1.25},
				"thinking":{"supported":true,"supported_swarm_settings":["off","low","medium","high","xhigh"],"default_swarm_setting":"high","provider_parameter":"reasoning_effort","swarm_setting_mappings":[{"swarm_setting":"off","provider_value":"none","effective_provider_value":"none"},{"swarm_setting":"high","provider_value":"high","effective_provider_value":"high"}]},
				"provider_specific":{"codex":{"context_window":272000,"max_context_window":333333,"serving":{"supported_tiers":["standard","fast"],"default_tier":"standard","provider_parameter":"service_tier","tiers":{"standard":{"tier":"standard","swarm_setting":"off","provider_parameter":"service_tier","provider_value":null},"fast":{"tier":"fast","swarm_setting":"fast","provider_parameter":"service_tier","provider_value":"priority"}}}}}
			},
			{
				"catalog_id":"codex/gpt-5.5",
				"provider_id":"codex",
				"provider_display_name":"OpenAI Codex (OAuth)",
				"model_id":"gpt-5.5",
				"display_name":"GPT 5.5",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":444444,"max_output_tokens":128000},
				"thinking":{"supported":true,"supported_swarm_settings":["off","low","medium","high","xhigh"],"default_swarm_setting":"xhigh","provider_parameter":"reasoning_effort"}
			},
			{
				"catalog_id":"codex/no-context",
				"provider_id":"codex",
				"provider_display_name":"OpenAI Codex (OAuth)",
				"model_id":"no-context",
				"display_name":"No Context",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":null,"max_output_tokens":128000}
			},
			{
				"catalog_id":"openai/gpt-5.5",
				"provider_id":"openai",
				"provider_display_name":"OpenAI",
				"model_id":"gpt-5.5",
				"display_name":"GPT 5.5 API",
				"capabilities":{"supports_text_input":true,"supports_text_output":true,"supports_reasoning":true},
				"limits":{"context_window_tokens":null,"max_output_tokens":128000},
				"provider_specific":{"openai":{"serving":{"supported_tiers":["standard","priority","flex","batch"],"default_tier":"standard","tiers":{"standard":{"tier":"standard","swarm_setting":"off","provider_parameter":"service_tier","provider_value":null},"priority":{"tier":"priority","swarm_setting":"priority","provider_parameter":"service_tier","provider_value":"priority"},"flex":{"tier":"flex","swarm_setting":"flex","provider_parameter":"service_tier","provider_value":"flex"},"batch":{"tier":"batch","swarm_setting":"batch","provider_parameter":null,"provider_value":null}}}}}
			},
			{
				"catalog_id":"anthropic/claude-opus-4-8",
				"provider_id":"anthropic",
				"provider_display_name":"Anthropic",
				"model_id":"claude-opus-4-8",
				"display_name":"Claude Opus 4.8",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":200000,"max_output_tokens":32000},
				"provider_specific":{"anthropic":{"serving":{"supported_tiers":["standard","priority","batch"],"default_tier":"standard","tiers":{"standard":{"tier":"standard","swarm_setting":"off","provider_parameter":"service_tier","provider_value":"standard_only"},"priority":{"tier":"priority","swarm_setting":"fast","provider_parameter":"service_tier","provider_value":"auto"},"batch":{"tier":"batch","swarm_setting":"batch","provider_parameter":null,"provider_value":null}},"fast_mode":{"supported":true,"provider_parameter":"speed","provider_value":"fast","beta_header":"fast-mode-2026-02-01"}}}}
			},
			{
				"catalog_id":"fireworks/glm-5p1",
				"provider_id":"fireworks-ai",
				"provider_display_name":"Fireworks AI",
				"model_id":"glm-5p1",
				"display_name":"GLM 5.1",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":202752,"max_output_tokens":null},
				"pricing":{"currency":"USD","input_price_per_million_tokens":2.8},
				"thinking":{"supported":true,"supported_swarm_settings":["off","medium"],"default_swarm_setting":"medium","provider_parameter":"reasoning_effort","swarm_setting_mappings":[{"swarm_setting":"off","provider_value":"none","effective_provider_value":"none"},{"swarm_setting":"medium","provider_value":"medium","effective_provider_value":"medium"}]},
				"provider_specific":{"fireworks":{"resource_name":"accounts/fireworks/models/glm-5p1","serving":{"supported_tiers":["standard","priority"],"default_tier":"standard","standard":{"tier":"standard","provider_parameter":null,"provider_value":null,"request_model_path":"accounts/fireworks/models/glm-5p1"},"priority":{"tier":"priority","provider_parameter":"service_tier","provider_value":"priority","request_model_path":"accounts/fireworks/models/glm-5p1"},"fast":null}}}
			},
			{
				"catalog_id":"fireworks/glm-5p1-fast",
				"provider_id":"fireworks",
				"provider_display_name":"Fireworks AI",
				"model_id":"glm-5p1-fast",
				"display_name":"GLM 5.1 Fast",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":202752,"max_output_tokens":null},
				"pricing":{"currency":"USD","input_price_per_million_tokens":2.8},
				"thinking":{"supported":true,"supported_swarm_settings":["off","medium"],"default_swarm_setting":"medium","provider_parameter":"reasoning_effort","swarm_setting_mappings":[{"swarm_setting":"off","provider_value":"none","effective_provider_value":"none"},{"swarm_setting":"medium","provider_value":"medium","effective_provider_value":"medium"}]},
				"provider_specific":{"fireworks":{"resource_name":"accounts/fireworks/routers/glm-5p1-fast","serving":{"supported_tiers":["standard"],"default_tier":"standard","standard":{"tier":"standard","provider_parameter":null,"provider_value":null,"request_model_path":"accounts/fireworks/routers/glm-5p1-fast"},"priority":null,"fast":null}}}
			}
		]
	}`)
}

func stringSlicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
