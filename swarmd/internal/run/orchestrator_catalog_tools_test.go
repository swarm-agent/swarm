package run

import (
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose:
//   - Requirement: the Orchestrator's stage 6 catalog tools manage_fleet,
//     manage_designs and manage_themes are code-owned: a stored profile from before them
//     cannot hide them, and no other agent (Swarm, Designer, a custom agent
//     a worker runs) gets them from that override.
//   - Threat/regression: the Orchestrator missing a catalog tool the brief
//     requires, or the override leaking fleet reads and linked designs to
//     other agents.
//   - Boundary/authority: resolveAgentToolStates.
//   - Narrowest layer: the resolver itself.
func TestOrchestratorHasItsCatalogToolsFromAStaleProfile(t *testing.T) {
	known := map[string]struct{}{"read": {}, tool.ManageFleetToolName: {}, tool.ManageDesignToolName: {}, tool.ManageThemeToolName: {}}
	stale := &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}}
	resolved, err := resolveAgentToolStates(pebblestore.AgentProfile{Name: agentruntime.SwarmOrchestratorAgentID, ToolContract: stale}, known)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{tool.ManageFleetToolName, tool.ManageDesignToolName, tool.ManageThemeToolName} {
		if !resolved.Tools[name].Enabled {
			t.Fatalf("the Orchestrator lacks %s", name)
		}
	}
	for _, agent := range []string{"swarm", agentruntime.DesignerAgentID, "my-custom-agent"} {
		other, err := resolveAgentToolStates(pebblestore.AgentProfile{Name: agent, ToolContract: stale}, known)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{tool.ManageFleetToolName, tool.ManageDesignToolName, tool.ManageThemeToolName} {
			if other.Tools[name].Enabled {
				t.Fatalf("%s got %s", agent, name)
			}
		}
	}
}
