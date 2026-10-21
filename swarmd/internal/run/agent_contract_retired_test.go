package run

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a saved agent profile or worker that still names a removed tool
// (manage_environments, manage_connections) keeps working: the names are
// ignored, never enabled, and never fail the agent.
func TestRetiredToolsInSavedProfilesAreIgnored(t *testing.T) {
	known := map[string]struct{}{"bash": {}, "read": {}}
	contract := &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
		"bash":                {Enabled: pebblestore.BoolPtr(true)},
		"manage_environments": {Enabled: pebblestore.BoolPtr(true)},
		"manage-connections":  {Enabled: pebblestore.BoolPtr(true)},
	}}
	if err := validateStoredV3AgentToolContractRuntime("worker", contract, known); err != nil {
		t.Fatalf("saved profile naming removed tools refused: %v", err)
	}
	resolved, err := resolveAgentToolStates(pebblestore.AgentProfile{Name: "worker", ToolContract: contract}, known)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateResolvedV3AgentToolRuntime("worker", resolved, known); err != nil {
		t.Fatalf("resolved profile refused: %v", err)
	}
	for _, name := range []string{"manage_environments", "manage-connections", "manage_connections"} {
		if resolved.Tools[name].Enabled {
			t.Fatalf("removed tool %s enabled", name)
		}
	}
	if !resolved.Tools["bash"].Enabled {
		t.Fatal("bash lost")
	}
}
