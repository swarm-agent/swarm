package agent

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: compiled and reconciled Orchestrator profiles must expose workspace
// management even when an old saved contract omitted it. These registry-level
// assertions prove reconciliation, not tool execution (tested in run).
func TestOrchestratorWorkspaceContractReconciliation(t *testing.T) {
	legacy := pebblestore.AgentProfile{ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{
		"manage_workspace": {Enabled: pebblestore.BoolPtr(false)},
	}}}
	for _, profile := range []pebblestore.AgentProfile{
		SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{}),
		reconcileSwarmOrchestratorAgentProfile(legacy),
	} {
		grant := profile.ToolContract.Tools["manage_workspace"]
		if grant.Enabled == nil || !*grant.Enabled {
			t.Fatal("workspace management missing from compiled contract")
		}
		for _, want := range []string{"does not create a physical directory or initialize Git", "workspace_generation", "dedicated mutation permission", "Delete unlinks catalog data, not files", "distinct from source execution authority"} {
			if !strings.Contains(profile.Prompt, want) {
				t.Fatalf("missing workspace guidance %q", want)
			}
		}
	}
}
