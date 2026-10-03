package run

import (
	"slices"
	"strings"
	"testing"

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
	if err != nil || mode != session.ModePlan {
		t.Fatalf("ordinary plan mode=%q err=%v", mode, err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan_manage", "exit_plan_mode"} {
		if resolved.Tools[name].Enabled || !disabled[name] || slices.Contains(resolved.AvailableTools, name) {
			t.Fatalf("forged %s exposed: %+v", name, resolved)
		}
	}
	if !resolved.Tools["manage_projects"].Enabled || !slices.Contains(resolved.AvailableTools, "manage_projects") {
		t.Fatal("project task-card route lost")
	}
}

// Purpose: masterHarnessPromptWithScopeAndAgent plus the compiled profile must
// instruct self-authored, guarded card revisions, not automatic Plan delegation.
// Integration must preserve targeted requirement edits separately from whole-plan
// replacements so the merged guidance does not regenerate unchanged scope.
// Prompt assembly is the narrowest layer for wording; lifecycle tests separately
// prove approval/revision effects and rejection of stale inputs.
func TestOrchestratorMasterHarnessExcludesSessionPlanning(t *testing.T) {
	scope := tool.WorkspaceScope{PrimaryPath: ".", Roots: []string{"."}}
	prompt := masterHarnessPromptWithScopeAndAgent(scope, true)
	for _, forbidden := range []string{"plan_manage", "exit_plan_mode", "start_session_checkpoint", "request_new_plan", "Plan & Checkpoint Lifecycle Management:"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("Orchestrator harness contains %q", forbidden)
		}
	}
	combined := prompt + "\n" + agent.SwarmOrchestratorAgentPrompt()
	for _, forbidden := range []string{"Manual big planning", "route to the Plan agent", "dedicated Plan agent may author", "Plan-mode orchestration"} {
		if strings.Contains(combined, forbidden) {
			t.Errorf("automatic Plan delegation remains: %s", forbidden)
		}
	}
	for _, required := range []string{"refine_task", "definition_revision", "plan_document", "Never begin implementation before required user approval", "Big features route to Swarm", "For whole-plan changes", "What will change", "edit_requirements", "base_revision_id", "Never regenerate the full plan for a localized edit"} {
		if !strings.Contains(combined, required) {
			t.Errorf("missing self-authored review contract: %s", required)
		}
	}
	if !strings.Contains(prompt, "manage_projects task cards only") {
		t.Fatal("project-card planning missing")
	}
	if !strings.Contains(masterHarnessPromptWithScopeAndAgent(scope, false), "start_session_checkpoint") {
		t.Fatal("ordinary session planning removed")
	}
	legacy := pebblestore.AgentProfile{Name: agent.SwarmOrchestratorAgentID, RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true)}
	capabilities := modeCapabilityInstructions(session.ModePlan, false, legacy)
	if strings.Contains(capabilities, "plan_manage") || strings.Contains(capabilities, "exit_plan_mode") || strings.Contains(capabilities, "Current session mode: plan") {
		t.Fatalf("legacy mode instructions advertise session planning: %s", capabilities)
	}
}

// Requirement: Orchestrator's compiled session deploy/commit and worktree
// promotion tools and websearch/webfetch reach provider definitions without
// bypassing canonical permission routing. ResolveAgentToolContract and
// filterToolDefinitions own exposure. Threat: runtime filtering can hide enabled aliases or
// accidentally expose privileged tools to restricted agents. This is the
// narrowest resolved-contract/provider-inventory layer; actual Git authority
// and session mutations remain covered by their dedicated tool tests.
func TestOrchestratorSessionIntegrationProviderTools(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	profile := agent.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{})
	resolved, _, disabled, err := svc.ResolveAgentToolContract(profile)
	if err != nil {
		t.Fatal(err)
	}
	definitions := filterToolDefinitions(convertToolDefinitions(svc.ListAgentToolDefinitions()), disabled)
	for _, tc := range []struct{ canonical, provider string }{
		{canonical: "manage_sessions", provider: "manage-sessions"},
		{canonical: "manage_worktree", provider: "manage-worktree"},
		{canonical: "websearch", provider: "websearch"},
		{canonical: "webfetch", provider: "webfetch"},
	} {
		if !resolved.Tools[tc.canonical].Enabled || disabled[tc.canonical] || !slices.Contains(resolved.AvailableTools, tc.canonical) {
			t.Fatalf("%s absent from resolved contract: %+v", tc.canonical, resolved)
		}
		found := false
		for _, definition := range definitions {
			if definition.Name == tc.provider {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("provider schema for %s was filtered out", tc.provider)
		}
	}
	for _, name := range []string{"plan_manage", "exit_plan_mode"} {
		if resolved.Tools[name].Enabled || !disabled[name] || slices.Contains(resolved.AvailableTools, name) {
			t.Fatalf("Orchestrator session-plan restriction lost for %s", name)
		}
	}
	for _, tc := range []struct{ arguments, requirement string }{
		{arguments: `{"action":"deploy"}`, requirement: "session_deploy"},
		{arguments: `{"action":"commit"}`, requirement: "session_commit"},
	} {
		if requirement, ask := permissionRequirement("auto", "manage-sessions", tc.arguments); !ask || requirement != tc.requirement {
			t.Fatalf("manage-sessions %s approval requirement = %s, ask=%v", tc.arguments, requirement, ask)
		}
	}
	for _, restricted := range []pebblestore.AgentProfile{
		agent.CoderAgentProfileForParent(pebblestore.AgentProfile{}),
		agent.FinderAgentProfileForParent(pebblestore.AgentProfile{}),
	} {
		other, _, otherDisabled, err := svc.ResolveAgentToolContract(restricted)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"manage_sessions", "manage_worktree"} {
			if other.Tools[name].Enabled || !otherDisabled[name] || slices.Contains(other.AvailableTools, name) {
				t.Fatalf("restricted agent %s exposed %s", restricted.Name, name)
			}
		}
	}
}

// Purpose: generation and post-hoc soundtrack composition must be available in
// Orchestrator's actual provider inventory, not only implemented in Runtime.
// ResolveAgentToolContract/filterToolDefinitions own exposure; this is the
// narrowest layer that catches a compiled allowlist silently hiding the APIs.
func TestOrchestratorMediaProviderTools(t *testing.T) {
	svc := NewService(nil, nil, nil, tool.NewRuntime(1), nil, nil, nil, nil)
	profile := agent.SwarmOrchestratorAgentProfileForContext(pebblestore.AgentProfile{})
	resolved, _, disabled, err := svc.ResolveAgentToolContract(profile)
	if err != nil {
		t.Fatal(err)
	}
	definitions := filterToolDefinitions(convertToolDefinitions(svc.ListAgentToolDefinitions()), disabled)
	for _, name := range []string{"manage_artifact", "manage_video"} {
		if !resolved.Tools[name].Enabled || disabled[name] || !slices.Contains(resolved.AvailableTools, name) {
			t.Fatalf("media tool %s unavailable", name)
		}
		found := false
		for _, definition := range definitions {
			if definition.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("media provider definition %s filtered out", name)
		}
	}
	for _, name := range []string{"plan_manage", "exit_plan_mode"} {
		if resolved.Tools[name].Enabled || !disabled[name] {
			t.Fatalf("media exposure widened planning authority: %s", name)
		}
	}
}
