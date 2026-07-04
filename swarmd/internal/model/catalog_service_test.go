package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestDecodeSwarmSnapshotRecordsMapsSnapshotFields(t *testing.T) {
	payload := snapshotPassthroughPayload()

	records, snapshot, err := decodeSwarmSnapshotRecords(payload, 1000, 2000, catalogSourcePinned, "etag-test")
	if err != nil {
		t.Fatalf("decodeSwarmSnapshotRecords returned error: %v", err)
	}
	if snapshot.SnapshotID != "snapshot-test" || snapshot.SnapshotVersion != "v-test" {
		t.Fatalf("snapshot metadata = %q/%q", snapshot.SnapshotID, snapshot.SnapshotVersion)
	}
	if len(records) != 4 {
		t.Fatalf("record count = %d, want exactly 4 valid context-window snapshot records", len(records))
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

	fireworks := records[2]
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

	fast := records[3]
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

func snapshotPassthroughPayload() []byte {
	return []byte(`{
		"snapshot_schema_version":"2026-07-01.1",
		"snapshot_id":"snapshot-test",
		"snapshot_version":"v-test",
		"generated_at":"2026-07-01T00:00:00Z",
		"model_count":5,
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
