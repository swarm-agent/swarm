package tool

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// Purpose: manageWorkersV2Definition must advertise document/review objects,
// rather than unconstrained JSON values that providers may stringify. Schema
// validation is the narrowest layer proving the provider-facing contract; the
// run package separately exercises legacy string decoding and persistence.
func TestWorkerProposalJSONSchema(t *testing.T) {
	for _, definition := range []Definition{manageWorkersV2Definition(), manageAutomationV2Definition()} {
		t.Run(definition.Name, func(t *testing.T) {
			properties := definition.Parameters["properties"].(map[string]any)
			for _, key := range []string{"document", "worker_review"} {
				if properties[key].(map[string]any)["type"] != "object" {
					t.Fatalf("%s must advertise an object: %+v", key, properties[key])
				}
			}
			raw, err := json.Marshal(definition.Parameters)
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
			for _, instance := range []map[string]any{
				{"action": "propose", "document": map[string]any{"title": "Reviewer", "info": map[string]any{"goal": "Review on request"}, "worker_v2": map[string]any{"schedule": map[string]any{"kind": "trigger"}}}},
				{"action": "propose", "name": "Reviewer", "instructions": "Review on request", "local_bindings": map[string]any{"primary": "ws_fixture"}},
				{"action": "propose", "document": map[string]any{}, "worker_review": map[string]any{}},
			} {
				if err := resolved.Validate(instance); err != nil {
					t.Fatalf("supported proposal rejected: %+v: %v", instance, err)
				}
			}
			for _, key := range []string{"document", "worker_review"} {
				for _, value := range []any{`{"title":"Reviewer"}`, []any{}, true, float64(1), nil} {
					if err := resolved.Validate(map[string]any{"action": "propose", key: value}); err == nil {
						t.Fatalf("non-object %s accepted: %#v", key, value)
					}
				}
			}
		})
	}
}
