package integration

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestServiceRejectsSecretBearingAdapterCreateAndUpdate(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "integrations-secrets.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(pebblestore.NewIntegrationStore(store))

	privateKeyFixture := strings.Join([]string{"-----BEGIN", "PRIVATE KEY-----\nsynthetic-test-data\n-----END PRIVATE KEY-----"}, " ")
	tokenFixture := strings.Join([]string{"sk", "synthetic-test-token"}, "-")
	for _, req := range []Request{
		{Action: "create", Resource: ResourceAdapter, PackID: "Demo", VersionID: "Draft", ID: "Key", Content: map[string]any{"type": "hosted_api", "settings": map[string]any{"api_key": "not-even-a-real-key"}}},
		{Action: "create", Resource: ResourceAdapter, PackID: "Demo", VersionID: "Draft", ID: "Token", Content: map[string]any{"type": "hosted_api", "settings": map[string]any{"header": "Bearer raw-token-value"}}},
		{Action: "update", Resource: ResourceAdapter, PackID: "Demo", VersionID: "Draft", ID: "PrivateKey", Content: map[string]any{"type": "hosted_api", "settings": map[string]any{"tls_material": privateKeyFixture}}},
		{Action: "update", Resource: ResourceAdapter, PackID: "Demo", VersionID: "Draft", ID: "Credential", Content: map[string]any{"type": "hosted_api", "credential_refs": map[string]any{"token": tokenFixture}}},
	} {
		if _, err := svc.Handle(req); err == nil || !strings.Contains(err.Error(), "raw secret") && !strings.Contains(err.Error(), "credential reference") {
			t.Fatalf("%s %s error = %v", req.Action, req.ID, err)
		}
	}
}

func TestServiceAllowsAdapterConfigurationAndNamedCredentialRefs(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "integrations-config.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(pebblestore.NewIntegrationStore(store))

	response, err := svc.Handle(Request{Action: "create", Resource: ResourceAdapter, PackID: "Demo", VersionID: "Draft", ID: "Hosted", Content: map[string]any{
		"type": "hosted_api", "settings": map[string]any{"base_url": "https://api.example.test", "timeout": "30s"}, "credential_refs": map[string]any{"oauth": "provider_oauth"},
	}})
	if err != nil {
		t.Fatalf("create adapter: %v", err)
	}
	item := response["item"].(map[string]any)
	if item["settings"].(map[string]string)["timeout"] != "30s" {
		t.Fatalf("settings = %#v", item["settings"])
	}
	if _, ok := item["credential_refs"]; ok {
		t.Fatalf("adapter leaked credential refs: %#v", item)
	}
	if keys := item["credential_ref_keys"].([]string); len(keys) != 1 || keys[0] != "oauth" {
		t.Fatalf("credential ref keys = %#v", keys)
	}
}

func TestAdapterMapRedactsLegacySecretSettings(t *testing.T) {
	item := adapterMap(pebblestore.IntegrationAdapterRecord{Settings: map[string]string{
		"base_url": "https://api.example.test",
		"token":    "legacy-raw-value",
		"header":   "Bearer legacy-raw-value",
	}})
	settings := item["settings"].(map[string]string)
	if len(settings) != 1 || settings["base_url"] == "" {
		t.Fatalf("safe settings = %#v", settings)
	}
}

func TestServiceInspectAndDraftCRUDRedactsCredentialRefs(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "integrations.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(pebblestore.NewIntegrationStore(store))

	if _, err := svc.Handle(Request{Action: "create", Resource: "pack", Content: map[string]any{"pack_id": "Spotify", "display_name": "Spotify"}}); err != nil {
		t.Fatalf("create pack: %v", err)
	}
	if _, err := svc.Handle(Request{Action: "create", Resource: "version", PackID: "Spotify", ID: "Draft", Content: map[string]any{"status": "draft"}}); err != nil {
		t.Fatalf("create version: %v", err)
	}
	if _, err := svc.Handle(Request{Action: "create", Resource: "adapter", PackID: "Spotify", VersionID: "Draft", ID: "Local", Content: map[string]any{
		"type":            "local_api_bridge",
		"display_name":    "Local Spotify bridge",
		"settings":        map[string]any{"base_url": "http://127.0.0.1:1234"},
		"credential_refs": map[string]any{"oauth": "vault://spotify/oauth"},
	}}); err != nil {
		t.Fatalf("create adapter: %v", err)
	}
	if _, err := svc.Handle(Request{Action: "create", Resource: "tool", PackID: "Spotify", VersionID: "Draft", ID: "Search", Content: map[string]any{
		"name": "Search tracks", "adapter_id": "Local", "permission_mode": "ask_async", "input_schema": map[string]any{"type": "object"},
	}}); err != nil {
		t.Fatalf("create tool: %v", err)
	}
	if _, err := svc.Handle(Request{Action: "create", Resource: "prompt_fragment", PackID: "Spotify", VersionID: "Draft", ID: "Context", Content: map[string]any{"content": "Use local auth."}}); err != nil {
		t.Fatalf("create prompt: %v", err)
	}

	inspect, err := svc.Handle(Request{Action: "inspect"})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspect["status"] != "ok" || inspect["pack_count"] != 1 {
		t.Fatalf("inspect response = %#v", inspect)
	}
	capabilities := inspect["capabilities"].(map[string]any)
	if capabilities["execution_active"] != false || capabilities["validation_active"] != false {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}

	listed, err := svc.Handle(Request{Action: "list", Resource: "adapter", PackID: "Spotify", VersionID: "Draft"})
	if err != nil {
		t.Fatalf("list adapters: %v", err)
	}
	items := listed["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	adapter := items[0]
	if _, ok := adapter["credential_refs"]; ok {
		t.Fatalf("adapter leaked credential_refs: %#v", adapter)
	}
	keys, ok := adapter["credential_ref_keys"].([]string)
	if !ok || len(keys) != 1 || keys[0] != "oauth" {
		t.Fatalf("credential ref keys = %#v", adapter["credential_ref_keys"])
	}

	raw, err := json.Marshal(listed)
	if err != nil {
		t.Fatalf("marshal list: %v", err)
	}
	if string(raw) == "" || strings.Contains(string(raw), "vault://spotify/oauth") {
		t.Fatalf("response leaked credential ref value: %s", raw)
	}
}
