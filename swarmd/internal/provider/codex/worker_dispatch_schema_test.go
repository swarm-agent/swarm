package codex_test

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"swarm/packages/swarmd/internal/provider/codex"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: registered worker schemas must allow caller keys after the production
// Codex transform, including the compatibility alias. Threat: closed schemas
// make canonical request/test admission unreachable. Resolved schema validation
// is the narrowest provider-wire check; invoker tests prove durable forwarding.
func TestCodexWorkerDispatchSchema(t *testing.T) {
	found := 0
	for _, definition := range tool.NewRuntime(1).Definitions() {
		if definition.Name != "manage_workers" && definition.Name != "manage_automation" {
			continue
		}
		found++
		t.Run(definition.Name, func(t *testing.T) {
			for _, parameters := range []map[string]any{definition.Parameters, codex.SanitizeToolParametersForTest(definition.Parameters)} {
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
					instance["idempotency_key"] = float64(1)
					if err := resolved.Validate(instance); err == nil {
						t.Fatal("non-string key accepted")
					}
				}
				if err := resolved.Validate(map[string]any{"action": "list"}); err != nil {
					t.Fatalf("unrelated action requires a key: %v", err)
				}
			}
		})
	}
	if found != 2 {
		t.Fatalf("expected both worker definitions, got %d", found)
	}
}
