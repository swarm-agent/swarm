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
		"Classify new feedback by impact on the deliverable contract", "choose the least disruptive valid route", "regardless of whether the user used an imperative sentence",
		"inquiry or guidance only means respond without plan mutation", "localized additive patch that preserves the objective and acceptance criteria means add_subtask", "continue the same checkpoint and attempt",
		"redefinition that invalidates the objective or acceptance criteria means restart_checkpoint with change_request and complete replacement title/tasks/acceptance_criteria/notes",
		"independently shippable work or a separate review/failure boundary means request_followup_checkpoint", "Why is the hero headline blue?", "Make the hero headline blue", "Also build an email template",
		"preserve checkpoint identity and attempt history", "Do not use add_subtask to clear blocked or failed state",
		"selected checkpoint's current objective governs its run", "earlier plan goals and checkpoint objectives are historical context", "objective is derived only from the current request",
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
		"Auto session mode does not mean an active plan exists", "Never call request_followup_checkpoint in this state",
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

func TestDurableRunStateInstructionsDistinguishesPlanAndFreshRuns(t *testing.T) {
	svc, sessionID, cleanup := newCheckpointRunPromptTestService(t)
	defer cleanup()
	planInstructions, err := svc.durableRunStateInstructions(sessionID, sessionruntime.ModePlan, "run-plan", RunOptions{})
	if err != nil {
		t.Fatalf("plan run state: %v", err)
	}
	if !strings.Contains(planInstructions, `"run_kind":"plan_parent"`) || !strings.Contains(planInstructions, `"context_policy":"session_history"`) {
		t.Fatalf("plan run state = %s", planInstructions)
	}
	freshInstructions, err := svc.durableRunStateInstructions(sessionID, sessionruntime.ModeAuto, "run-fresh", RunOptions{PlanCheckpointContext: &RunPlanCheckpointContext{PlanID: "active", CheckpointID: "cp-1"}})
	if err != nil {
		t.Fatalf("fresh run state: %v", err)
	}
	if !strings.Contains(freshInstructions, `"run_kind":"fresh_checkpoint"`) || !strings.Contains(freshInstructions, `"context_policy":"fresh_checkpoint_context"`) {
		t.Fatalf("fresh run state = %s", freshInstructions)
	}
}

func TestCheckpointPromptCarriesOriginAndFreshRunMetadata(t *testing.T) {
	prompt, err := renderCheckpointRunPrompt(checkpointRunPromptPayload{
		PlanID: "plan-1", ExecutionOrigin: sessionruntime.PlanExecutionOriginApprovedPlan, RunKind: runKindFreshCheckpoint, ContextPolicy: contextPolicyFresh,
		Checkpoint: pebblestore.SessionPlanCheckpoint{ID: "cp-1"},
	})
	if err != nil {
		t.Fatalf("render checkpoint prompt: %v", err)
	}
	for _, want := range []string{`"execution_origin": "approved_plan"`, `"run_kind": "fresh_checkpoint"`, `"context_policy": "fresh_checkpoint_context"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("checkpoint prompt missing %q: %s", want, prompt)
		}
	}
}
