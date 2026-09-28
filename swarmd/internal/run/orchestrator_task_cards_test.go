package run

import (
	"strings"
	"testing"
	"slices"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: a stale plan-mode Orchestrator executes in auto mode without
// deleting historical plans; runtime mode resolution must not trust persisted
// plan-auto flags, while other agents retain their own mode policy.
func TestOrchestratorLegacySessionModeUsesAuto(t *testing.T) {
	svc := &Service{}
	legacy := pebblestore.AgentProfile{Name: agent.SwarmOrchestratorAgentID, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true)}
	mode, warning, err := svc.resolveExecutionMode(session.ModePlan, legacy)
	if err != nil || mode != session.ModeAuto || !strings.Contains(warning, "task cards") {
		t.Fatalf("legacy Orchestrator mode=%q warning=%q err=%v", mode, warning, err)
	}
	other := pebblestore.AgentProfile{Name: "swarm", RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true)}
	mode, _, err = svc.resolveExecutionMode(session.ModePlan, other)
	if err != nil || mode != session.ModePlan { t.Fatalf("ordinary plan mode=%q err=%v", mode, err) }
}

// Requirement: the effective Orchestrator harness only describes project-card
// planning, never session checkpoints. This assembly layer is narrower than
// provider tests and checks the final master prompt rather than the profile alone.
// Requirement: even a forged persisted tool contract cannot expose session-plan
// calls to Orchestrator. The resolved contract is the provider tool inventory
// and permission boundary; manage_projects remains available for task cards.
func TestOrchestratorResolvedToolsDenyForgedSessionPlanning(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	profile := agent.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{})
	profile.ToolContract.Tools["plan_manage"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
	profile.ToolContract.Tools["exit_plan_mode"] = pebblestore.AgentToolConfig{Enabled: pebblestore.BoolPtr(true)}
	resolved, _, disabled, err := svc.ResolveAgentToolContract(profile)
	if err != nil { t.Fatal(err) }
	for _, name := range []string{"plan_manage", "exit_plan_mode"} {
		if resolved.Tools[name].Enabled || !disabled[name] || slices.Contains(resolved.AvailableTools, name) {
			t.Fatalf("forged %s exposed: %+v", name, resolved)
		}
	}
	if !resolved.Tools["manage_projects"].Enabled || !slices.Contains(resolved.AvailableTools, "manage_projects") {
		t.Fatal("project task-card route lost")
	}
}

func TestOrchestratorMasterHarnessExcludesSessionPlanning(t *testing.T) {
	scope := tool.WorkspaceScope{PrimaryPath: ".", Roots: []string{"."}}
	prompt := masterHarnessPromptWithScopeAndAgent(scope, true)
	for _, forbidden := range []string{"plan_manage", "exit_plan_mode", "start_session_checkpoint", "request_new_plan", "Plan & Checkpoint Lifecycle Management:"} {
		if strings.Contains(prompt, forbidden) { t.Errorf("Orchestrator harness contains %q", forbidden) }
	}
	if !strings.Contains(prompt, "manage_projects task cards only") { t.Fatal("project-card planning missing") }
	if !strings.Contains(masterHarnessPromptWithScopeAndAgent(scope, false), "start_session_checkpoint") { t.Fatal("ordinary session planning removed") }
	legacy := pebblestore.AgentProfile{Name: agent.SwarmOrchestratorAgentID, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true)}
	capabilities := modeCapabilityInstructions(session.ModePlan, false, legacy)
	if strings.Contains(capabilities, "plan_manage") || strings.Contains(capabilities, "exit_plan_mode") || strings.Contains(capabilities, "Current session mode: plan") {
		t.Fatalf("legacy mode instructions advertise session planning: %s", capabilities)
	}
}
