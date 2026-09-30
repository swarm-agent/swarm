package google

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: the Google production schema transform must retain the caller key
// on both registered worker names. Threat: provider-specific filtering blocks
// request/test before durable admission. Resolved transformed schema validation
// is the narrowest check; this is not evidence of live provider execution.
func TestGoogleWorkerDispatchSchema(t *testing.T) {
	found := 0
	for _, definition := range tool.NewRuntime(1).Definitions() {
		if definition.Name != "manage_workers" && definition.Name != "manage_automation" {
			continue
		}
		found++
		t.Run(definition.Name, func(t *testing.T) {
			parameters := sanitizeGoogleToolParameters(definition.Parameters)
			properties := parameters["properties"].(map[string]any)
			key, ok := properties["idempotency_key"].(map[string]any)
			if !ok || key["type"] != "string" {
				t.Fatalf("provider dropped caller key: %+v", properties)
			}
			raw, err := json.Marshal(parameters)
			if err != nil {
				t.Fatal(err)
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"request", "test"} {
				instance := map[string]any{"action": action, "worker_id": "worker_fixture", "prompt": "Summarize only", "idempotency_key": "summary-job-1"}
				if err := resolved.Validate(instance); err != nil {
					t.Fatalf("keyed %s rejected: %v", action, err)
				}
			}
			if err := resolved.Validate(map[string]any{"action": "list"}); err != nil {
				t.Fatalf("unrelated action requires key: %v", err)
			}
		})
	}
	if found != 2 {
		t.Fatalf("expected both worker definitions, got %d", found)
	}
}
