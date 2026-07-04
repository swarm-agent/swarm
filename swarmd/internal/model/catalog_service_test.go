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
	payload := []byte(`{
		"snapshot_schema_version":"2026-07-01.1",
		"snapshot_id":"snapshot-test",
		"snapshot_version":"v-test",
		"generated_at":"2026-07-01T00:00:00Z",
		"model_count":2,
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
				"limits":{"context_window_tokens":272000,"max_output_tokens":128000},
				"pricing":{"currency":"USD","input_price_per_million_tokens":1.25},
				"thinking":{"supported":true}
			},
			{
				"catalog_id":"fireworks/deepseek-v4-pro",
				"provider_id":"fireworks-ai",
				"model_id":"deepseek-v4-pro",
				"capabilities":{"supports_reasoning":true},
				"limits":{"context_window_tokens":1048576,"max_output_tokens":null},
				"provider_specific":{"fireworks":{"resource_name":"accounts/fireworks/models/deepseek-v4-pro","serving":{"supported_tiers":["standard","priority","fast"],"default_tier":"standard","fast":{"tier":"fast","provider_parameter":"model","provider_value":"accounts/fireworks/routers/deepseek-v4-pro-turbo"}}}}
			}
		]
	}`)

	records, snapshot, err := decodeSwarmSnapshotRecords(payload, 1000, 2000, catalogSourcePinned, "etag-test")
	if err != nil {
		t.Fatalf("decodeSwarmSnapshotRecords returned error: %v", err)
	}
	if snapshot.SnapshotID != "snapshot-test" || snapshot.SnapshotVersion != "v-test" {
		t.Fatalf("snapshot metadata = %q/%q", snapshot.SnapshotID, snapshot.SnapshotVersion)
	}
	if len(records) != 3 {
		t.Fatalf("record count = %d, want 3", len(records))
	}
	codex := records[0]
	if codex.Provider != "codex" || codex.Model != "gpt-5.4" {
		t.Fatalf("codex record id = %q/%q", codex.Provider, codex.Model)
	}
	if codex.ContextWindow != 272000 || codex.MaxOutputTokens != 128000 || !codex.Reasoning {
		t.Fatalf("codex limits/reasoning = context %d max %d reasoning %v", codex.ContextWindow, codex.MaxOutputTokens, codex.Reasoning)
	}
	if codex.SourceSnapshotID != "snapshot-test" || codex.SourceSnapshotVersion != "v-test" || codex.Source != catalogSourcePinned {
		t.Fatalf("codex snapshot/source metadata not preserved: %+v", codex)
	}
	var pricing map[string]any
	if err := json.Unmarshal(codex.Pricing, &pricing); err != nil || pricing["currency"] != "USD" {
		t.Fatalf("pricing not preserved: pricing=%s err=%v", codex.Pricing, err)
	}
	fireworks := records[1]
	if fireworks.Provider != "fireworks" || fireworks.Model != "deepseek-v4-pro" {
		t.Fatalf("fireworks record id = %q/%q", fireworks.Provider, fireworks.Model)
	}
	if fireworks.ContextWindow != 1048576 || fireworks.MaxOutputTokens != 0 {
		t.Fatalf("fireworks limits = context %d max %d", fireworks.ContextWindow, fireworks.MaxOutputTokens)
	}
	if len(records) != 3 {
		t.Fatalf("record count with Fireworks fast router = %d, want 3", len(records))
	}
	fast := records[2]
	if fast.Provider != "fireworks" || fast.Model != "accounts/fireworks/routers/deepseek-v4-pro-turbo" {
		t.Fatalf("fireworks fast record id = %q/%q", fast.Provider, fast.Model)
	}
	if len(fireworks.ServiceTiers) != 2 || fireworks.ServiceTiers[0] != "standard" || fireworks.ServiceTiers[1] != "priority" {
		t.Fatalf("fireworks base service tiers = %#v, want standard/priority", fireworks.ServiceTiers)
	}
	if len(fast.ServiceTiers) != 1 || fast.ServiceTiers[0] != "standard" {
		t.Fatalf("fireworks fast service tiers = %#v, want standard only", fast.ServiceTiers)
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
