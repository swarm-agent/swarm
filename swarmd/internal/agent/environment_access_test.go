package agent

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: compiled profile reconciliation must migrate missing environment tools
// without overriding saved explicit denials. The registry is the narrowest owner
// of defaults for newly created and resumed system-agent profiles.
func TestEnvironmentToolProfileDefaultsAndDenials(t *testing.T) {
	for _, reconcile := range []func(pebblestore.AgentProfile) pebblestore.AgentProfile{reconcileSwarmAgentProfile, reconcileSwarmOrchestratorAgentProfile} {
		for _, disabled := range []bool{false, true} {
			snapshot := pebblestore.AgentProfile{}
			if disabled {
				snapshot.ToolContract = &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{
					"manage_environments": {Enabled: pebblestore.BoolPtr(false)},
					"manage_connections": {Enabled: pebblestore.BoolPtr(false)},
				}}
			}
			profile := reconcile(snapshot)
			for _, name := range []string{"manage_environments", "manage_connections"} {
				if agentToolEnabled(profile.ToolContract, name) == disabled {
					t.Fatalf("%s %s disabled=%v", profile.Name, name, disabled)
				}
			}
		}
	}
}
