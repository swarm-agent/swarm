package tool

import "testing"

// Requirement: a completed checkpoint can attach the exact native Artifact V3
// revision returned by direct primary-Swarm create. Threat: a legacy-only schema
// forces the model to forge collection/variant identity and makes successful
// artifact runs fail during terminal handoff.
func TestSessionPlanArtifactSchemaIncludesNativeArtifactV3Reference(t *testing.T) {
	schema := sessionPlanArtifactToolSchema()
	properties := schema["properties"].(map[string]any)
	for _, field := range []string{"session_id", "artifact_id", "revision_ref"} {
		if properties[field] == nil {
			t.Fatalf("plan artifact schema omitted %s", field)
		}
	}
	found := false
	for _, branch := range schema["anyOf"].([]any) {
		required, _ := branch.(map[string]any)["required"].([]string)
		if len(required) == 3 && required[0] == "session_id" && required[1] == "artifact_id" && required[2] == "revision_ref" {
			found = true
		}
	}
	if !found {
		t.Fatal("plan artifact schema omitted native Artifact V3 required branch")
	}
}
