package agent

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: workers and automations are exclusively managed by Swarm Orchestrator,
// while regular chat sessions have worker tools disabled so automations are only
// accessible in Orchestrate mode.
func TestAutomationPrimaryCapability(t *testing.T) {
	grant, ok := SwarmOrchestratorAgentToolContract().Tools["manage_automation"]
	if !ok || grant.Enabled == nil || !*grant.Enabled {
		t.Fatal("missing compiled orchestrator automation capability")
	}
	grantWorkers, ok := SwarmOrchestratorAgentToolContract().Tools["manage_workers"]
	if !ok || grantWorkers.Enabled == nil || !*grantWorkers.Enabled {
		t.Fatal("missing compiled orchestrator workers capability")
	}

	// Verify disabled on regular chat sessions
	primaryGrant, ok := SwarmAgentToolContract().Tools["manage_workers"]
	if ok && primaryGrant.Enabled != nil && *primaryGrant.Enabled {
		t.Fatal("workers capability should be disabled on regular chat sessions")
	}
	primaryAutoGrant, ok := SwarmAgentToolContract().Tools["manage_automation"]
	if ok && primaryAutoGrant.Enabled != nil && *primaryAutoGrant.Enabled {
		t.Fatal("automation capability should be disabled on regular chat sessions")
	}

	// Invariant: all subagent profiles (Coder, Finder, Designer, Compact) must have worker tools disabled.
	subagentContracts := []*pebblestore.AgentToolContract{
		CoderAgentToolContract(),
		FinderAgentToolContract(),
		DesignerAgentToolContract(),
		CompactAgentToolContract(),
	}
	for _, contract := range subagentContracts {
		if w, ok := contract.Tools["manage_workers"]; ok && w.Enabled != nil && *w.Enabled {
			t.Fatal("manage_workers must be disabled on subagent contracts")
		}
		if a, ok := contract.Tools["manage_automation"]; ok && a.Enabled != nil && *a.Enabled {
			t.Fatal("manage_automation must be disabled on subagent contracts")
		}
	}
}
