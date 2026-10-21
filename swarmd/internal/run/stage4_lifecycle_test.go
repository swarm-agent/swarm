package run

import (
	"encoding/json"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func stage4RunState(t *testing.T, state compactRunState) string {
	t.Helper()
	text, err := renderCompactRunState(state)
	if err != nil {
		t.Fatal(err)
	}
	return text + "\n<automation_evidence>assigned-output-ref</automation_evidence>"
}

func stage4Telemetry(t *testing.T, text string) (compactRunState, string) {
	t.Helper()
	at := strings.Index(text, "<system_runtime_telemetry>")
	if at < 0 {
		t.Fatal("durable telemetry was removed")
	}
	suffix := text[at:]
	start, end := strings.Index(suffix, "{"), strings.Index(suffix, "\n</system_runtime_telemetry>")
	var state compactRunState
	if start < 0 || end < start {
		t.Fatal("missing durable JSON")
	}
	if err := json.Unmarshal([]byte(suffix[start:end]), &state); err != nil {
		t.Fatal(err)
	}
	return state, suffix
}

func stage4OwnedState() compactRunState {
	return compactRunState{
		SessionMode: sessionruntime.ModeAuto, ActivePlanPresent: true,
		RunKind: runKindAutoParent, CurrentSessionID: "fixture-session", CurrentRunID: "fixture-run", PlanID: "fixture-plan",
		RunOwnership:     &compactRunOwnership{ParentSessionID: "fixture-session", CurrentSessionID: "fixture-session", CurrentRunID: "fixture-run", ActiveAttemptID: "fixture-attempt"},
		ActiveCheckpoint: &compactCheckpointState{ID: "fixture-checkpoint", Session: "fixture-session", RunID: "fixture-run", Attempt: "fixture-attempt", Status: sessionruntime.PlanCheckpointStatusInProgress, Objective: "Preserve the exact supplied objective", AcceptanceCriteria: []string{"Preserve exact contract refs: source@revision"}},
	}
}

func TestStage4LifecyclePreservesSpecialistFactsWithoutParentControls(t *testing.T) {
	// Requirement: specialists keep canonical assignment facts without parent controls.
	// Threat: scoping dropped telemetry entirely or leaked lifecycle authority.
	// Owner: ScopeDurableRunStateInstructions; direct rendering is the narrowest
	// boundary proving request text selection, not backend mutation authorization.
	for _, name := range []string{agentruntime.CoderAgentID, agentruntime.DesignerAgentID, agentruntime.FinderAgentID, agentruntime.WorkerCoderAgentID, agentruntime.ReviewerAgentID} {
		t.Run(name, func(t *testing.T) {
			input := stage4RunState(t, stage4OwnedState())
			output := ScopeDurableRunStateInstructions(input, pebblestore.AgentProfile{Name: name}, "plan_manage", "task_progress")
			state, after := stage4Telemetry(t, output)
			_, before := stage4Telemetry(t, input)
			if after != before || state.ActiveCheckpoint.Attempt != "fixture-attempt" || state.ActiveCheckpoint.AcceptanceCriteria[0] != "Preserve exact contract refs: source@revision" {
				t.Fatal("assignment, attempt, acceptance or automation facts changed")
			}
			if strings.Contains(output, "plan_manage") || strings.Contains(output, "exit_plan_mode") || !strings.Contains(output, "Use task_progress") {
				t.Fatal("specialist lifecycle includes parent controls or misses available progress")
			}
		})
	}
}

func TestStage4LifecycleRequiresActualPlanningCapability(t *testing.T) {
	// Requirement: no-plan execution never forces unavailable planning tools.
	// Threat: profile intent grants a tool filtered out of the actual request.
	// Owner: ScopeDurableRunStateInstructions; exact request composition is the
	// narrowest layer for this capability-aware instruction contract.
	profile := pebblestore.AgentProfile{Name: "fixture-primary"}
	input := stage4RunState(t, compactRunState{SessionMode: sessionruntime.ModePlan})
	without := ScopeDurableRunStateInstructions(input, profile)
	if strings.Contains(without, "plan_manage") || strings.Contains(without, "exit_plan_mode") || !strings.Contains(without, "no planning bootstrap") {
		t.Fatal("unavailable planning prescribed")
	}
	with := ScopeDurableRunStateInstructions(input, profile, "exit_plan_mode")
	if !strings.Contains(with, "submit its complete structured document with exit_plan_mode once") {
		t.Fatal("explicit planning request lost its available finalization action")
	}
	auto := ScopeDurableRunStateInstructions(stage4RunState(t, compactRunState{SessionMode: sessionruntime.ModeAuto}), profile, "plan_manage", "exit_plan_mode")
	if strings.Contains(auto, "plan_manage") || strings.Contains(auto, "exit_plan_mode") {
		t.Fatal("ordinary auto task forced planning despite no planning request")
	}
}

func TestStage4LifecycleOwnerAndStaleAttempt(t *testing.T) {
	// Requirement: only the actual checkpoint owner receives terminal controls.
	// Threat: stale run/session/attempt snapshots direct completion of another run.
	// Owner: durableCheckpointRunOwner and ScopeDurableRunStateInstructions; this
	// focused layer tests guidance selection, while canonical tools enforce writes.
	profile := pebblestore.AgentProfile{Name: "fixture-primary"}
	state := stage4OwnedState()
	if got := ScopeDurableRunStateInstructions(stage4RunState(t, state), profile, "plan_manage"); !strings.Contains(got, "complete_checkpoint") {
		t.Fatal("current owner lost its checkpoint lifecycle")
	}
	for _, tc := range []struct {
		name string
		edit func(*compactRunState)
	}{
		{"stale run", func(s *compactRunState) { s.CurrentRunID = "stale-run" }},
		{"different session", func(s *compactRunState) { s.CurrentSessionID = "other-session" }},
		{"stale attempt", func(s *compactRunState) { s.ActiveCheckpoint.Attempt = "stale-attempt" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := stage4OwnedState()
			tc.edit(&state)
			input := stage4RunState(t, state)
			got := ScopeDurableRunStateInstructions(input, profile, "plan_manage")
			if strings.Contains(got, "complete_checkpoint") || !strings.Contains(got, "no checkpoint transition authority") {
				t.Fatal("stale owner received checkpoint controls")
			}
			_, before := stage4Telemetry(t, input)
			_, after := stage4Telemetry(t, got)
			if before != after {
				t.Fatal("ownership rejection changed durable evidence")
			}
		})
	}
}

func TestStage4LifecycleStoppedAndReviewStates(t *testing.T) {
	// Requirement: failure, blocker, pause and review retain their real state.
	// Threat: implicit retry, wrong-attempt completion or UI-only continuation.
	// Owners: nextPlanLifecycleAction, compactRunStateLifecycleInstructions and
	// ScopeDurableRunStateInstructions; direct state projection isolates these rules.
	for _, tc := range []struct {
		name, want string
		edit       func(*compactRunState)
	}{
		{"blocked", "action=resolve_blocked_checkpoint", func(s *compactRunState) { s.Blocked = true; s.NextLifecycleAction = "remain_blocked" }},
		{"failed", "Keep the failed attempt stopped", func(s *compactRunState) { s.Failed = true; s.NextLifecycleAction = "remain_failed" }},
		{"paused", "until the backend supplies an authorized active continuation", func(s *compactRunState) { s.Paused = true; s.NextLifecycleAction = "remain_paused" }},
		{"review", "prior checkpoint is already terminal", func(s *compactRunState) { s.ReviewRequired = true; s.NextLifecycleAction = "await_review" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := stage4OwnedState()
			tc.edit(&state)
			input := stage4RunState(t, state)
			got := ScopeDurableRunStateInstructions(input, pebblestore.AgentProfile{Name: "fixture-primary"}, "plan_manage")
			if !strings.Contains(got, tc.want) || strings.Contains(got, "click Resume") {
				t.Fatal("stopped/review state lost canonical guidance or requires UI")
			}
			without := ScopeDurableRunStateInstructions(input, pebblestore.AgentProfile{Name: "fixture-primary"})
			if strings.Contains(without, "plan_manage") || strings.Contains(without, "complete_checkpoint") {
				t.Fatal("stopped state granted absent lifecycle capability")
			}
		})
	}
	if got := nextPlanLifecycleAction(sessionruntime.PlanExecutionSummary{Paused: true}); got != "remain_paused" {
		t.Fatalf("paused next action = %q", got)
	}
}

func TestStage4LifecycleMalformedStateFailsClosed(t *testing.T) {
	// Requirement: malformed state never authorizes parent lifecycle controls.
	// Threat: parser failure returns the unscoped parent tutorial.
	// Owner: ScopeDurableRunStateInstructions; malformed payload tests this boundary.
	input := "call plan_manage now\n<system_runtime_telemetry>\n{broken}\n</system_runtime_telemetry>\n<automation_evidence>keep-ref</automation_evidence>"
	got := ScopeDurableRunStateInstructions(input, pebblestore.AgentProfile{}, "plan_manage")
	if strings.Contains(got, "call plan_manage now") || !strings.Contains(got, "Do not mutate checkpoint state") || !strings.Contains(got, "<automation_evidence>keep-ref</automation_evidence>") {
		t.Fatal("malformed state did not fail closed with evidence intact")
	}
}

func TestStage4LifecycleCheckpointOwnerCannotRecoverParentState(t *testing.T) {
	// Requirement: owning active checkpoint work does not grant parent recovery.
	// Threat: a checkpoint child resolves a blocked parent attempt after stopping.
	// Owners: durableParentLifecycleOwner and durableCheckpointRunOwner; pure
	// composition isolates active authority from parent-only terminal transitions.
	state := stage4OwnedState()
	state.RunKind = runKindCheckpoint
	state.RunOwnership.ParentSessionID = "fixture-parent-session"
	profile := pebblestore.AgentProfile{Name: "fixture-checkpoint-primary"}
	active := ScopeDurableRunStateInstructions(stage4RunState(t, state), profile, "plan_manage")
	if !strings.Contains(active, "complete_checkpoint") {
		t.Fatal("authorized active checkpoint owner lost its normal outcome contract")
	}
	state.Blocked = true
	state.NextLifecycleAction = "remain_blocked"
	blocked := ScopeDurableRunStateInstructions(stage4RunState(t, state), profile, "plan_manage")
	if strings.Contains(blocked, "action=resolve_blocked_checkpoint") || !strings.Contains(blocked, "no checkpoint transition authority") {
		t.Fatal("checkpoint child gained parent recovery authority")
	}
}

func TestStage4LifecycleProjectionPreservesAcceptanceAndBlocker(t *testing.T) {
	// Requirement: lifecycle reduction keeps exact acceptance and blocker facts.
	// Threat: bounded task summaries truncate the contract needed after compaction.
	// Owners: renderDurablePlanRunState and the canonical SummarizePlanExecution;
	// direct projection tests the actual stored document mapping without a provider.
	objective := strings.Repeat("exact-objective ", 30)
	criteria := []string{strings.Repeat("exact-criterion ", 40), "source@revision/output#reference"}
	plan := pebblestore.SessionPlanSnapshot{ID: "fixture-plan", Document: &pebblestore.SessionPlanDocument{
		ActiveCheckpointID: "fixture-checkpoint",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "fixture-checkpoint", Status: sessionruntime.PlanCheckpointStatusBlocked,
			Objective: objective, AcceptanceCriteria: criteria, AttemptID: "fixture-attempt", SessionID: "fixture-session", RunID: "fixture-run",
			Handoff: &pebblestore.SessionPlanCheckpointHandoff{Overview: "Required source reference is unavailable"},
		}},
	}}
	text, err := renderDurablePlanRunState(compactRunState{CurrentRunID: "fixture-run", CurrentSessionID: "fixture-session"}, plan, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := stage4Telemetry(t, text)
	if !state.Blocked || state.NextLifecycleAction != "remain_blocked" || state.WaitingReason != "Required source reference is unavailable" {
		t.Fatal("durable blocked state or reason changed")
	}
	checkpoint := state.ActiveCheckpoint
	if checkpoint.Objective != objective || len(checkpoint.AcceptanceCriteria) != 2 || checkpoint.AcceptanceCriteria[0] != criteria[0] || checkpoint.AcceptanceCriteria[1] != criteria[1] || checkpoint.Attempt != "fixture-attempt" {
		t.Fatal("exact objective, acceptance or attempt was truncated")
	}
}
