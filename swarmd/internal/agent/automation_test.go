package agent

import "testing"

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
}
