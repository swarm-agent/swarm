package agent

import "testing"

// Purpose: the compiled primary agent must receive the automation tool through
// the code-owned registry, not mutable profile state. Registry-unit inspection is
// the narrowest layer proving the default grant (not runtime approval).
func TestAutomationPrimaryCapability(t *testing.T) {
	grant, ok := SwarmAgentToolContract().Tools["manage_automation"]
	if !ok || grant.Enabled == nil || !*grant.Enabled {
		t.Fatal("missing compiled primary automation capability")
	}
}
