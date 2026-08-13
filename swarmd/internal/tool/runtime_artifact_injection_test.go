package tool

import (
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
	found := false
	for _, definition := range runtime.Definitions() {
		if definition.Name == "manage_artifact" {
			found = true
		}
	}
	if !found {
		t.Fatal("manage_artifact definition is missing")
	}
}
