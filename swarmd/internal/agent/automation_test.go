package agent

import "testing"

// Purpose: automations are disabled for launch. The compiled primary agent
// must have manage_automation and manage_workers disabled by default in its tool contract.
func TestAutomationPrimaryCapability(t *testing.T) {
	grant, ok := SwarmAgentToolContract().Tools["manage_automation"]
	if !ok || grant.Enabled == nil || *grant.Enabled {
		t.Fatal("expected compiled primary automation capability to be disabled by default")
	}
	grantWorkers, ok := SwarmAgentToolContract().Tools["manage_workers"]
	if !ok || grantWorkers.Enabled == nil || *grantWorkers.Enabled {
		t.Fatal("expected compiled primary workers capability to be disabled by default")
	}
}
