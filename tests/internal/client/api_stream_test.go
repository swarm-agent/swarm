package client_test

import (
	"testing"

	client "swarm-refactor/swarmtui/internal/client"
)

func TestClientPackageCompilesForExternalTests(t *testing.T) {
	api := client.New("http://swarm.test")
	if got := api.BaseURL(); got != "http://swarm.test" {
		t.Fatalf("BaseURL() = %q, want http://swarm.test", got)
	}
}
