package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	integrationruntime "swarm/packages/swarmd/internal/integration"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageIntegrationsContractAndDraftCRUD(t *testing.T) {
	rt := NewRuntime(1)
	found := false
	for _, definition := range rt.Definitions() {
		if definition.Name == "manage-integrations" {
			found = true
			if !strings.Contains(definition.Description, "Execution, validation, publish") {
				t.Fatalf("description does not document inactive runtime: %s", definition.Description)
			}
		}
	}
	if !found {
		t.Fatalf("manage-integrations definition not found")
	}

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "integrations.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rt.SetManageIntegrationService(integrationruntime.NewService(pebblestore.NewIntegrationStore(store)))

	call := func(args string) map[string]any {
		t.Helper()
		output, err := rt.ExecuteForWorkspaceScopeWithRuntime(context.Background(), WorkspaceScope{PrimaryPath: t.TempDir()}, Call{CallID: "mi", Name: "manage-integrations", Arguments: args})
		if err != nil {
			t.Fatalf("manage-integrations failed: %v output=%s", err, output)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(output), &payload); err != nil {
			t.Fatalf("decode output: %v output=%s", err, output)
		}
		return payload
	}

	call(`{"action":"create","resource":"pack","content":{"pack_id":"Demo","display_name":"Demo"}}`)
	call(`{"action":"create","resource":"version","pack_id":"Demo","id":"Draft","content":{"status":"draft"}}`)
	call(`{"action":"create","resource":"adapter","pack_id":"Demo","version_id":"Draft","id":"Local","content":{"type":"cli_wrapper","credential_refs":{"token":"vault-ref-demo-token"}}}`)
	inspect := call(`{"action":"inspect"}`)
	if inspect["status"] != "ok" || inspect["path_id"] != "tool.manage-integrations.v1" {
		t.Fatalf("inspect = %#v", inspect)
	}
	caps := inspect["capabilities"].(map[string]any)
	if caps["execution_active"] != false || caps["publish_active"] != false {
		t.Fatalf("capabilities = %#v", caps)
	}
	listed := call(`{"action":"list","resource":"adapter","pack_id":"Demo","version_id":"Draft"}`)
	raw, _ := json.Marshal(listed)
	if strings.Contains(string(raw), "vault-ref-demo-token") || strings.Contains(string(raw), "credential_refs") {
		t.Fatalf("credential refs leaked: %s", raw)
	}
}
