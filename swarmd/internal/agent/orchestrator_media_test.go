package agent

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: compiled Orchestrator profiles explicitly opt into dynamic media
// authorization. SystemAgentRegistry.ReconcileSnapshot must upgrade old session
// snapshots without replacing resolved model preferences or widening unrelated
// tools. This registry-level test is the narrowest proof of that migration.
func TestOrchestratorMediaAuthorizationReconcilesModelPreferences(t *testing.T) {
	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		legacy := SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{})
		delete(legacy.ToolContract.Tools, "media_inspect")
		if disabled {
			legacy.ToolContract.Tools["media_inspect"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(false)}
		}
		legacy.Provider, legacy.Model, legacy.Thinking = "test-provider", "resolved-model", "high"
		legacy.AutoServiceTier = "priority"
		profile, err := registry.ReconcileSnapshot(SwarmOrchestratorAgentID, legacy)
		if err != nil {
			t.Fatal(err)
		}
		grant := profile.ToolContract.Tools["media_inspect"]
		if grant.Enabled == nil || !*grant.Enabled {
			t.Fatalf("reconciled media grant missing (disabled=%v): %+v", disabled, grant)
		}
		if profile.Provider != legacy.Provider || profile.Model != legacy.Model || profile.Thinking != legacy.Thinking || profile.AutoServiceTier != legacy.AutoServiceTier {
			t.Fatalf("resolved model preferences changed: %+v", profile)
		}
		for _, name := range []string{"plan_manage", "exit_plan_mode"} {
			grant := profile.ToolContract.Tools[name]
			if grant.Enabled == nil || *grant.Enabled {
				t.Fatalf("unrelated tool %q broadened", name)
			}
		}
	}
	grant := SwarmOrchestratorAgentToolContract().Tools["media_inspect"]
	if grant.Enabled == nil || !*grant.Enabled {
		t.Fatal("new Orchestrator contract lacks explicit media authorization")
	}
}
