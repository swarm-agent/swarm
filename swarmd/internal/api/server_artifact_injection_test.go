package api

import (
	"testing"

	"swarm/packages/swarmd/internal/artifact"
)

func TestServerArtifactRegistryInjection(t *testing.T) {
	server := &Server{}
	registry := artifact.NewRegistry(nil, artifact.Limits{})
	server.SetArtifactRegistry(registry)
	if server.ArtifactRegistry() != registry {
		t.Fatal("artifact registry was not injected")
	}
}
