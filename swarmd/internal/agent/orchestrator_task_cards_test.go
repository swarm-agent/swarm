package agent

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Orchestrator plans only on project task cards. A persisted old
// plan-auto profile or prompt must not restore session-plan tools or instructions;
// registry reconciliation is the narrowest owner of compiled agent identity.
func TestOrchestratorTaskCardProfileReconcilesLegacyPlanSnapshot(t *testing.T) {
	registry, err := BuiltinSystemAgentRegistry()
	if err != nil {
		t.Fatal(err)
	}
	legacy := pebblestore.AgentProfile{
		Name: SwarmOrchestratorAgentID, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto,
		DefaultSessionMode:  pebblestore.AgentDefaultSessionModePlan,
		ExitPlanModeEnabled: pebblestore.BoolPtr(true), Prompt: "Create session checkpoints via plan_manage and exit_plan_mode",
		Provider: "test-provider", Model: "test-model",
		ToolContract: &pebblestore.AgentToolContract{Preset: "read_write", Tools: map[string]pebblestore.AgentToolConfig{
			"plan_manage": {Enabled: pebblestore.BoolPtr(true)}, "exit_plan_mode": {Enabled: pebblestore.BoolPtr(true)},
		}},
	}
	profile, err := registry.ReconcileSnapshot(SwarmOrchestratorAgentID, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if profile.RuntimeMode != pebblestore.AgentRuntimeModeReadWrite || profile.DefaultSessionMode != pebblestore.AgentDefaultSessionModeAuto || pebblestore.AgentExitPlanModeEnabled(profile) {
		t.Fatalf("legacy plan mode survived reconciliation: %+v", profile)
	}
	if profile.Provider != legacy.Provider || profile.Model != legacy.Model || profile.Prompt != SwarmOrchestratorAgentPrompt() {
		t.Fatalf("compiled prompt or model preference lost: %+v", profile)
	}
	for _, name := range []string{"plan_manage", "exit_plan_mode"} {
		if cfg := profile.ToolContract.Tools[name]; cfg.Enabled == nil || *cfg.Enabled {
			t.Fatalf("session-plan tool %q enabled: %+v", name, cfg)
		}
	}
	if cfg := profile.ToolContract.Tools["manage_projects"]; cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatal("task-card tool unavailable")
	}
	if !strings.Contains(profile.Prompt, "plan_document") || !strings.Contains(profile.Prompt, "agent=\"coder\"") {
		t.Fatal("direct structured plans or small Coder tasks missing")
	}
	if cfg := SwarmAgentToolContract().Tools["plan_manage"]; cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatal("ordinary Swarm session planning changed")
	}
}
