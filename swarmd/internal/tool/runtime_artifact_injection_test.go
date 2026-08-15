package tool

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/artifact"
)

func TestRuntimeArtifactRegistryInjection(t *testing.T) {
	runtime := NewRuntime(1)
	registry := artifact.NewRegistry(nil, artifact.Limits{})
	authority := &fakeArtifactAuthority{}
	runtime.SetArtifactRegistry(registry)
	runtime.SetArtifactAuthority(authority)
	if runtime.ArtifactRegistry() != registry {
		t.Fatal("artifact registry was not injected")
	}
	if runtime.ArtifactAuthority() != authority {
		t.Fatal("artifact authority was not injected")
	}
	definitions := runtime.Definitions()
	var manageArtifact *Definition
	for index := range definitions {
		definition := &definitions[index]
		if definition.Name == "manage_artifact" {
			manageArtifact = definition
			break
		}
	}
	if manageArtifact == nil {
		t.Fatal("manage_artifact definition is missing")
	}
	for _, want := range []string{"reusable exact source for repeated edits", "every remix", "preview/download", "re-prompt from scratch"} {
		if !strings.Contains(manageArtifact.Description, want) {
			t.Fatalf("manage_artifact description missing %q: %s", want, manageArtifact.Description)
		}
	}
	properties := manageArtifact.Parameters["properties"].(map[string]any)
	for _, field := range []string{"source_session_id", "source_collection_id", "source_variant_id", "source_event_seq"} {
		description := properties[field].(map[string]any)["description"].(string)
		if !strings.Contains(description, "every image remix") || !strings.Contains(description, "reusable") {
			t.Fatalf("%s description does not explain repeated remixing: %s", field, description)
		}
	}
}
