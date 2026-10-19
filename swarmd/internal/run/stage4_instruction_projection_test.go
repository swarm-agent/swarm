package run

import (
	"encoding/json"
	"strings"
	"testing"

	agent "swarm/packages/swarmd/internal/agent"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func stage4ProjectedInstructions(t *testing.T, profile store.AgentProfile, mode string, names ...string) (store.AgentProfile, string) {
	t.Helper()
	before, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	definitions := make([]provideriface.ToolDefinition, len(names))
	for i, name := range names {
		definitions[i].Name = name
	}
	projected := InstructionProfileForTools(profile, definitions)
	after, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("instruction projection changed original profile/tool authority")
	}
	if projected.ToolContract == profile.ToolContract {
		t.Fatal("instruction projection shares original contract")
	}
	scope := tool.WorkspaceScope{PrimaryPath: t.TempDir(), SessionID: "fixture-session"}
	scope.Roots = []string{scope.PrimaryPath}
	text := (&Service{tools: &tool.Runtime{}}).ComposeRuntimeInstructions(scope, mode, false, projected, "Objective: preserve fixture source. Acceptance: exact output references remain available.")
	if !strings.Contains(text, "Objective: preserve fixture source.") || !strings.Contains(text, "Acceptance: exact output references remain available.") {
		t.Fatal("projection dropped supplied objective/acceptance")
	}
	after, err = json.Marshal(profile)
	if err != nil || string(before) != string(after) {
		t.Fatal("composition mutated original profile/tool authority")
	}
	return projected, text
}

func TestStage4InstructionProjectionSwarmKeepsOnlyExposedCapabilities(t *testing.T) {
	// Requirement: final request tools govern Swarm operational instructions.
	// Threat: composing a restricted request restores commands or management
	// capability from its broader compiled profile. Owners: InstructionProfileForTools,
	// profileHasCapability and ComposeRuntimeInstructions; production composition
	// is the narrowest boundary that can expose this instruction mismatch.
	original := agent.SwarmAgentProfileForContext(store.AgentProfile{})
	projected, text := stage4ProjectedInstructions(t, original, "auto", "task", "read", "search", "list")
	if !profileHasCapability(projected, "task") {
		t.Fatal("available delegation disappeared")
	}
	for _, name := range []string{"bash", "manage-worktree", "manage_sessions", "manage-sessions", "manage-workspace", "plan_manage"} {
		if profileHasCapability(projected, name) {
			t.Fatalf("removed capability restored: %s", name)
		}
	}
	for _, absent := range []string{"manage-worktree recall", "bash committed_source", "validation_receipt.verified=true", "Execution capacity facts", "manage-sessions capacity"} {
		if strings.Contains(text, absent) {
			t.Fatalf("unavailable operational guidance: %q", absent)
		}
	}
	if !strings.Contains(text, "Use task to launch") || !strings.Contains(text, "Parent command validation is unavailable") {
		t.Fatal("delegation or pending validation guidance missing")
	}
	// Mutating an ephemeral projection must not change the original contract.
	projected.ToolContract.Tools["task"] = store.AgentToolConfig{Enabled: store.BoolPtr(false)}
	if !profileHasCapability(original, "task") {
		t.Fatal("projection aliases the stored tool contract")
	}
}

func TestStage4InstructionProjectionWorkerWithoutBash(t *testing.T) {
	// Requirement: worker instructions reflect unavailable command capability.
	// Threat: a static worker tutorial promises validation after Bash is filtered.
	// Owners: InstructionProfileForTools and harnessForProfile; full composition
	// includes both profile and harness so one cannot contradict the other.
	projected, text := stage4ProjectedInstructions(t, agent.WorkerCoderAgentProfileForParent(store.AgentProfile{}), "auto", "read", "write", "edit", "search", "list")
	if profileHasCapability(projected, "bash") {
		t.Fatal("worker Bash restored")
	}
	for _, absent := range []string{"Worker Coders have Bash", "Worker Coders run Bash", "Execute checks required by the brief", "first write the reproduction and run it", "run their own focused tests"} {
		if strings.Contains(text, absent) {
			t.Fatalf("missing Bash still receives command mandate: %q", absent)
		}
	}
	if !strings.Contains(text, "pending step") || !strings.Contains(text, "without claiming it ran") {
		t.Fatal("worker must report unavailable validation explicitly")
	}
}

func TestStage4InstructionProjectionCoderMissingCommitAndProgress(t *testing.T) {
	// Requirement: a Coder with filtered commit/progress tools reports a blocker.
	// Threat: static instructions invent commits or call removed task_progress.
	// Owners: InstructionProfileForTools and role composition; exact composed
	// guidance verifies the exposed contract without executing provider work.
	projected, text := stage4ProjectedInstructions(t, agent.CoderAgentProfileForParent(store.AgentProfile{}), "auto", "read", "write", "edit", "search", "list")
	if profileHasCapability(projected, "git_commit") || profileHasCapability(projected, "task_progress") {
		t.Fatal("Coder filtered capabilities restored")
	}
	for _, absent := range []string{"task_progress(action=", "git_commit(", "initialize your checklist"} {
		if strings.Contains(text, absent) {
			t.Fatalf("Coder instructed to call absent tool: %q", absent)
		}
	}
	for _, required := range []string{"required edit, progress or commit operation is unavailable", "actual uncommitted state", "never invent a successful commit", "not run; parent validation required"} {
		if !strings.Contains(text, required) {
			t.Fatalf("Coder lost explicit failure handoff: %q", required)
		}
	}
}

func TestStage4InstructionProjectionDesignerMissingSwarmDesign(t *testing.T) {
	// Requirement: Designer makes designs only through swarm_design; when a
	// request does not expose it, the projected prompt says no design can be
	// made instead of keeping the normal Designer instructions.
	// Threat: Designer writes HTML another way or reports an invented design
	// when its only design tool is removed. Owners: InstructionProfileForTools
	// and Designer composition; the complete instruction text is the narrowest
	// observable boundary.
	projected, text := stage4ProjectedInstructions(t, agent.DesignerAgentProfileForParent(store.AgentProfile{}), "auto", "read", "search", "list")
	if profileHasCapability(projected, tool.SwarmDesignToolName) {
		t.Fatal("Designer swarm_design restored")
	}
	if strings.Contains(text, "Call swarm_design with the brief") {
		t.Fatal("Designer instructed to call absent swarm_design")
	}
	if !strings.Contains(text, "swarm_design, your only way to make or edit designs, is unavailable") || !strings.Contains(text, "Do not write HTML any other way") {
		t.Fatal("Designer lost missing-capability failure boundary")
	}
	// With swarm_design exposed the normal Designer prompt stays in force.
	_, full := stage4ProjectedInstructions(t, agent.DesignerAgentProfileForParent(store.AgentProfile{}), "auto", "read", "search", "list", tool.SwarmDesignToolName)
	if !strings.Contains(full, agent.DesignerAgentPrompt()) || strings.Contains(full, "is unavailable in this request") {
		t.Fatal("Designer with swarm_design lost its prompt")
	}
}

func TestStage4InstructionProjectionPlanFinalizationKeepsTelemetry(t *testing.T) {
	// Requirement: finalization filters remove mutation tutorials while keeping
	// exact durable objective, acceptance and attempt facts. Threat: a filtered
	// plan_manage tool is reintroduced in instructions or telemetry is discarded.
	// Owners: final InstructionProfileForTools, ComposeRuntimeInstructions and
	// ScopeDurableRunStateInstructions; exact final-list composition proves this.
	profile := agent.WithAdvancedPlanning(agent.SwarmAgentProfileForContext(store.AgentProfile{}))
	projected, text := stage4ProjectedInstructions(t, profile, "plan", "exit_plan_mode", "compact")
	if profileHasCapability(projected, "plan_manage") || !profileHasCapability(projected, "exit_plan_mode") || !profileHasCapability(projected, "compact") {
		t.Fatal("plan-finalization tool projection differs from final tool list")
	}
	state := compactRunState{SessionMode: "plan", ActivePlanPresent: true, CurrentSessionID: "fixture-session", CurrentRunID: "fixture-run", RunKind: runKindPlanParent, ActiveCheckpoint: &compactCheckpointState{ID: "fixture-checkpoint", Session: "fixture-session", RunID: "fixture-run", Attempt: "fixture-attempt", Objective: "Exact original objective", AcceptanceCriteria: []string{"Exact acceptance and output@revision"}}}
	durable, err := renderCompactRunState(state)
	if err != nil {
		t.Fatal(err)
	}
	scoped := ScopeDurableRunStateInstructions(durable, projected, "exit_plan_mode", "compact")
	text += "\n\n" + scoped
	for _, absent := range []string{"plan_manage", "complete_checkpoint", "restart_checkpoint", "resolve_blocked_checkpoint", "start_session_checkpoint"} {
		if strings.Contains(text, absent) {
			t.Fatalf("finalization retained unavailable mutation guidance: %q", absent)
		}
	}
	_, before := stage4Telemetry(t, durable)
	actual, after := stage4Telemetry(t, scoped)
	if before != after || actual.ActiveCheckpoint.Objective != state.ActiveCheckpoint.Objective || actual.ActiveCheckpoint.Attempt != "fixture-attempt" || actual.ActiveCheckpoint.AcceptanceCriteria[0] != state.ActiveCheckpoint.AcceptanceCriteria[0] {
		t.Fatal("finalization filtering changed durable objective/attempt/acceptance")
	}
}

// Requirement: regular Coder validation guidance follows final exposed Bash.
// Threat: a filtered request promises execution, or gaining Bash restores
// delegation/environment authority. Owners: InstructionProfileForTools and
// harnessForProfile. Composition is the narrowest layer for instruction/tool
// consistency; actual execution placement is qualified separately.
func TestStage4InstructionProjectionCoderBash(t *testing.T) {
	for _, exposed := range []bool{false, true} {
		names := []string{"read", "write", "edit", "search", "list", "git_commit", "task_progress"}
		if exposed {
			names = append(names, "bash")
		}
		projected, text := stage4ProjectedInstructions(t, agent.CoderAgentProfileForParent(store.AgentProfile{}), "auto", names...)
		if profileHasCapability(projected, "bash") != exposed {
			t.Fatalf("Bash exposed=%v differs from projected authority", exposed)
		}
		for _, denied := range []string{"task", "manage_environments", "manage_workers", "manage_sessions", "manage-worktree"} {
			if profileHasCapability(projected, denied) {
				t.Fatalf("Bash exposure restored %s", denied)
			}
		}
		if exposed {
			if !strings.Contains(text, "Use Bash for the focused tests") || strings.Contains(text, "Regular Coder cannot execute commands") {
				t.Fatal("exposed Bash has stale commandless guidance")
			}
		} else if strings.Contains(text, "Use Bash for the focused tests") || !strings.Contains(text, "not run; parent validation required") {
			t.Fatal("filtered Bash lacks explicit pending-check guidance")
		}
	}
}
