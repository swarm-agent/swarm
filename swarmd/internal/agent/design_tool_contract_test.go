package agent

import "testing"

// Purpose: single-design delegation is available to both parent system agents,
// not just multi-variant tasks. The code-owned tool contracts are the narrowest
// authority for exposure; a missing entry must not silently deny the new route.
func TestDesignToolParentContracts(t *testing.T) {
	for name, contract := range map[string]bool{
		"swarm": SwarmAgentToolContract().Tools["manage_design"].Enabled != nil && *SwarmAgentToolContract().Tools["manage_design"].Enabled,
		"orchestrator": SwarmOrchestratorAgentToolContract().Tools["manage_design"].Enabled != nil && *SwarmOrchestratorAgentToolContract().Tools["manage_design"].Enabled,
	} { if !contract { t.Fatalf("%s cannot delegate designs",name) } }
}
