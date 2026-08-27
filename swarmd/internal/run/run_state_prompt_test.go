package run

import (
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestDurableRunStateInstructionsUsesActivePlanInsteadOfTranscript(t *testing.T) {
	svc, sessionID, cleanup := newCheckpointRunPromptTestService(t)
	defer cleanup()
	if _, _, err := svc.sessions.SavePlanWithMetadata(sessionID, "plan-state", "State Plan", "# ignored", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID:              "plan-state",
		Title:           "State Plan",
		ExecutionOrigin: sessionruntime.PlanExecutionOriginAutoSession,
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "attempt-1", ParentSessionID: "parent-1", CurrentSessionID: sessionID, CurrentRunID: "durable-run"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Current", Status: sessionruntime.PlanCheckpointStatusInProgress, AttemptID: "attempt-1", RunID: "durable-run", SessionID: sessionID,
			Tasks: []string{"Implement state", "Do not trust old transcript"},
		}},
		ActiveCheckpointID: "cp-1",
	}}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	instructions, err := svc.durableRunStateInstructions(sessionID, sessionruntime.ModeAuto, "run-now", RunOptions{})
	if err != nil {
		t.Fatalf("run state instructions: %v", err)
	}
	for _, want := range []string{
		"Durable run state (authoritative", `"session_mode":"auto"`, `"active_plan_present":true`, `"run_kind":"inline_checkpoint"`, `"context_policy":"session_history"`,
		`"execution_origin":"auto_session"`, `"plan_id":"plan-state"`, `"active_attempt_id":"attempt-1"`,
		`"next_lifecycle_action":"continue_or_start_next_checkpoint"`, "Do not trust old transcript",
		"Do not call plan_manage get-active merely to determine whether a plan exists", "An active plan exists; the injected plan and checkpoint fields are authoritative",
		"Inspect next_lifecycle_action and the active checkpoint status before acting", "When next_lifecycle_action is await_review or await_final_review", "prior checkpoint is already terminal and its handoff has already been emitted", "normal post-handoff conversation turn", "Do not continue, complete, re-complete, or otherwise mutate the terminal checkpoint", "respond conversationally without plan mutation", "plain request to continue as authority to keep working in the same checkpoint only when the checkpoint is nonterminal",
		"Classify new feedback by impact on the deliverable contract", "choose the least disruptive valid route", "regardless of whether the user used an imperative sentence",
		"backend has already reactivated the paused checkpoint", "treat it as nonterminal", "do not call resume_checkpoint", "do not wait for the user to click Resume", "Treat a plain request to continue as authority to keep working in the same checkpoint",
		"inquiry or guidance only means respond without plan mutation", "localized additive patch whose existing checklist remains valid means add_subtask", "continue the same checkpoint and attempt", `"action":"add_subtask","checkpoint_id":"cp-1","subtask":{"title":"Measure Swarm hosting capacity"}`, "subtask must be a JSON object with a non-empty title", "do not issue a partial call to discover the format", "feedback that supersedes the current checklist means replace_subtasks with the complete authoritative list",
		"redefinition that invalidates the objective or acceptance criteria means restart_checkpoint with change_request and complete replacement title/tasks/acceptance_criteria/notes", "you must restart the checkpoint with the full replacement contract", "Do not refuse or conversationally dismiss the redirection", "complete or re-complete the superseded checkpoint", "treat it as terminal post-handoff conversation", "emit a final handoff instead of restarting",
		"independently shippable work or a separate review/failure boundary from a parent provider turn means transition_checkpoint_boundary", "use request_new_plan with the current plan_id to replace the whole plan", "Why is the hero headline blue?", "Make the hero headline blue", "Also build an email template",
		"preserve checkpoint identity and attempt history", "Do not use add_subtask to clear blocked or failed state",
		"selected checkpoint's current objective governs its run", "earlier plan goals and checkpoint objectives are historical context", "self-contained objective derived only from the current request",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("run state missing %q: %s", want, instructions)
		}
	}
	for _, forbidden := range []string{"No active plan exists", "for a clear bounded task call plan_manage start_session_checkpoint", "make exactly one approval-gated plan_manage request_new_plan call"} {
		if strings.Contains(instructions, forbidden) {
			t.Fatalf("active-plan run state unexpectedly contains %q: %s", forbidden, instructions)
		}
	}
}

func TestDurableRunStateInstructionsRoutesBlockedCheckpointThroughResolution(t *testing.T) {
	svc, sessionID, cleanup := newCheckpointRunPromptTestService(t)
	defer cleanup()
	if _, _, err := svc.sessions.SavePlanWithMetadata(sessionID, "plan-blocked", "Blocked Plan", "# ignored", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ID:                 "plan-blocked",
		Title:              "Blocked Plan",
		ExecutionOrigin:    sessionruntime.PlanExecutionOriginApprovedPlan,
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateBlocked, LastCheckpointID: "cp-4", LastAttemptID: "attempt-3", LastOutcome: sessionruntime.PlanCheckpointStatusBlocked, ParentSessionID: sessionID},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-4", Title: "Live proof", Status: sessionruntime.PlanCheckpointStatusBlocked, AttemptID: "attempt-3", RunID: "old-run", SessionID: sessionID}},
		ActiveCheckpointID: "cp-4",
	}}); err != nil {
		t.Fatalf("save blocked plan: %v", err)
	}

	instructions, err := svc.durableRunStateInstructions(sessionID, sessionruntime.ModeAuto, "new-parent-run", RunOptions{})
	if err != nil {
		t.Fatalf("blocked run state: %v", err)
	}
	for _, want := range []string{
		`"blocked":true`, `"next_lifecycle_action":"remain_blocked"`,
		"this parent run does not own its terminal outcome", "Do not continue implementation, run validation", "complete_checkpoint/mark_needs_review/mark_blocked/mark_failed", "will fail run-ownership checks",
		"action=resolve_blocked_checkpoint", "checkpoint_id=cp-4", "start_next=true", "do not pass stale attempt_id or run_id values", "creates a fresh attempt and provider run owning the same checkpoint",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("blocked run state missing %q: %s", want, instructions)
		}
	}
	for _, forbidden := range []string{"localized additive patch whose existing checklist remains valid means add_subtask", "Treat a plain request to continue as authority to keep working"} {
		if strings.Contains(instructions, forbidden) {
			t.Fatalf("blocked run state unexpectedly contains runnable instruction %q: %s", forbidden, instructions)
		}
	}
}

func TestDurableRunStateInstructionsRoutesAutoModeWithoutActivePlan(t *testing.T) {
	svc, sessionID, cleanup := newCheckpointRunPromptTestService(t)
	defer cleanup()

	instructions, err := svc.durableRunStateInstructions(sessionID, sessionruntime.ModeAuto, "run-auto", RunOptions{})
	if err != nil {
		t.Fatalf("auto run state: %v", err)
	}
	for _, want := range []string{
		`"session_mode":"auto"`, `"active_plan_present":false`,
		"Do not call plan_manage get-active merely to determine whether a plan exists",
		"Auto session mode does not mean an active plan exists", "The retired request_followup_checkpoint action and aliases are always invalid",
		"for a clear bounded task call plan_manage start_session_checkpoint directly", "that single action atomically creates and starts the checkpoint in the current run", "do not call start_checkpoint afterward",
		"make exactly one approval-gated plan_manage request_new_plan call with a complete multi-checkpoint structured document",
		"Do not create a draft with new/save", "do not propose a plan and then manually start it",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("auto/no-plan run state missing %q: %s", want, instructions)
		}
	}
}

func TestDurableRunStateInstructionsRoutesPlanModeWithoutActivePlan(t *testing.T) {
	svc, sessionID, cleanup := newCheckpointRunPromptTestService(t)
	defer cleanup()

	instructions, err := svc.durableRunStateInstructions(sessionID, sessionruntime.ModePlan, "run-plan", RunOptions{})
	if err != nil {
		t.Fatalf("plan run state: %v", err)
	}
	for _, want := range []string{
		`"session_mode":"plan"`, `"active_plan_present":false`,
		"Do not call plan_manage get-active merely to determine whether a plan exists",
		"run only the targeted discovery needed to make the plan actionable",
		"call exit_plan_mode exactly once with the complete structured plan document",
		"Do not call start_session_checkpoint",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("plan/no-plan run state missing %q: %s", want, instructions)
		}
	}
	for _, forbidden := range []string{"for a clear bounded task call plan_manage start_session_checkpoint", "approval-gated plan_manage request_new_plan"} {
		if strings.Contains(instructions, forbidden) {
			t.Fatalf("plan/no-plan run state unexpectedly contains %q: %s", forbidden, instructions)
		}
	}
}

func TestDurableRunStateInstructionsDistinguishesPlanAndCheckpointRuns(t *testing.T) {
	svc, sessionID, cleanup := newCheckpointRunPromptTestService(t)
	defer cleanup()
	planInstructions, err := svc.durableRunStateInstructions(sessionID, sessionruntime.ModePlan, "run-plan", RunOptions{})
	if err != nil {
		t.Fatalf("plan run state: %v", err)
	}
	if !strings.Contains(planInstructions, `"run_kind":"plan_parent"`) || !strings.Contains(planInstructions, `"context_policy":"session_history"`) {
		t.Fatalf("plan run state = %s", planInstructions)
	}
	checkpointInstructions, err := svc.durableRunStateInstructions(sessionID, sessionruntime.ModeAuto, "run-checkpoint", RunOptions{PlanCheckpointContext: &RunPlanCheckpointContext{PlanID: "active", CheckpointID: "cp-1"}})
	if err != nil {
		t.Fatalf("checkpoint run state: %v", err)
	}
	if !strings.Contains(checkpointInstructions, `"run_kind":"checkpoint_run"`) || !strings.Contains(checkpointInstructions, `"context_policy":"same_epoch_checkpoint_context"`) {
		t.Fatalf("checkpoint run state = %s", checkpointInstructions)
	}
}

func TestCheckpointPromptCarriesOriginAndSameEpochRunMetadata(t *testing.T) {
	prompt, err := renderCheckpointRunPrompt(checkpointRunPromptPayload{
		PlanID: "plan-1", ExecutionOrigin: sessionruntime.PlanExecutionOriginApprovedPlan, RunKind: runKindCheckpoint, ContextPolicy: contextPolicyCheckpoint,
		Checkpoint: pebblestore.SessionPlanCheckpoint{ID: "cp-1"},
	})
	if err != nil {
		t.Fatalf("render checkpoint prompt: %v", err)
	}
	for _, want := range []string{`"execution_origin": "approved_plan"`, `"run_kind": "checkpoint_run"`, `"context_policy": "same_epoch_checkpoint_context"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("checkpoint prompt missing %q: %s", want, prompt)
		}
	}
}
