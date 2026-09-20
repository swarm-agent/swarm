package codex

import (
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Requirement: provider schema transformation preserves the real registered
// retained import action and exact discriminated reference properties. Threat:
// helper-only schema tests miss dropped nested fields on the provider wire.
// Runtime.Definitions -> sanitizeCodexToolParameters is the narrowest proof;
// this does not call a provider or prove live model behavior.
func TestCodexRetainedArtifactRegisteredSchema(t *testing.T) {
	for _, definition := range tool.NewRuntime(1).Definitions() {
		if definition.Name != "manage_artifact" {
			continue
		}
		cleaned := sanitizeCodexToolParameters(definition.Parameters)
		properties := cleaned["properties"].(map[string]any)
		action := properties["action"].(map[string]any)
		found := false
		for _, value := range action["enum"].([]any) {
			if value == "import" {
				found = true
			}
		}
		if !found {
			t.Fatal("provider schema dropped import action")
		}
		for name, fields := range map[string][]string{"artifact_v3_reference": {"session_id", "artifact_id", "revision_ref"}, "artifact_reference": {"session_id", "collection_id", "variant_id", "event_seq"}} {
			reference := properties[name].(map[string]any)
			nested := reference["properties"].(map[string]any)
			for _, field := range fields {
				if nested[field] == nil {
					t.Fatalf("provider schema dropped %s.%s", name, field)
				}
			}
			if len(nested) != len(fields) {
				t.Fatalf("reference exposes extra identity fields: %#v", nested)
			}
		}
		return
	}
	t.Fatal("manage_artifact missing from registered definitions")
}
