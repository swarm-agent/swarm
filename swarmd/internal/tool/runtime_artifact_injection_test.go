package tool

import (
	"testing"

	"swarm/packages/swarmd/internal/artifact"
)

func TestRuntimeArtifactRegistryInjection(t *testing.T) {
	runtime := NewRuntime(1)
	registry := artifact.NewRegistry(nil, artifact.Limits{})
	runtime.SetArtifactRegistry(registry)
	if runtime.ArtifactRegistry() != registry {
		t.Fatal("artifact registry was not injected")
	}
	for _, definition := range runtime.Definitions() {
		if definition.Name == "manage_artifact" {
			t.Fatal("artifact lifecycle wiring exposed manage_artifact prematurely")
		}
	}
}
