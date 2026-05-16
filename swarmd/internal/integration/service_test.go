package integration

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
