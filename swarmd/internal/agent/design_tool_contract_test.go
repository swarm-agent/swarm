package agent

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: compiled coding profiles must not expose plan ceremonies or managed
// media mutations. Registry materialization/reconciliation is the narrowest
// boundary proving defaults and legacy opt-in without changing persistence.
func TestCodingContractsExcludePlansAndArtifacts(t *testing.T) {
	for name, profile := range map[string]pebblestore.AgentProfile{
		"swarm":  SwarmAgentProfileForContext(pebblestore.AgentProfile{}),
		"worker": WorkerCoderAgentProfileForParent(pebblestore.AgentProfile{}),
	} {
		for _, tool := range []string{"plan_manage", "exit_plan_mode", "manage_design", "manage_artifact", "manage_video", "artifact_v2_author", "artifact_v3_author"} {
			if agentToolEnabled(profile.ToolContract, tool) {
				t.Errorf("%s enables %s", name, tool)
			}
		}
	}
}

// Purpose: hiding defaults must not strip saved sessions' plan tools or explicit
// interactive plan opt-in; reconciliation owns that compatibility boundary.
func TestSwarmPreservesAdvancedPlanOptIn(t *testing.T) {
	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, context := range []pebblestore.AgentProfile{
		{DefaultSessionMode: pebblestore.AgentDefaultSessionModePlan},
		{ToolContract: &pebblestore.AgentToolContract{Tools: map[string]pebblestore.AgentToolConfig{"plan_manage": {Enabled: pebblestore.BoolPtr(true)}}}},
	} {
		profile, err := registry.ReconcileSnapshot(SwarmAgentID, context)
		if err != nil {
			t.Fatal(err)
		}
		if !agentToolEnabled(profile.ToolContract, "plan_manage") || !pebblestore.AgentExitPlanModeEnabled(profile) {
			t.Fatal("advanced plan capability lost")
		}
		if agentToolEnabled(profile.ToolContract, "manage_artifact") {
			t.Fatal("plan opt-in granted artifacts")
		}
	}
}

// Purpose: the separate orchestrator design surface remains available; removing
// coding defaults must not delete its registration.
func TestDesignToolParentContracts(t *testing.T) {
	if !agentToolEnabled(SwarmOrchestratorAgentToolContract(), "manage_design") {
		t.Fatal("orchestrator cannot delegate designs")
	}
}
