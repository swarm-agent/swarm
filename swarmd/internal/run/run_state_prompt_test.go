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
		"Durable run state (authoritative", `"session_mode":"auto"`, `"run_kind":"inline_checkpoint"`, `"context_policy":"session_history"`,
		`"execution_origin":"auto_session"`, `"plan_id":"plan-state"`, `"active_attempt_id":"attempt-1"`,
		`"next_lifecycle_action":"continue_or_start_next_checkpoint"`, "Do not trust old transcript",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("run state missing %q: %s", want, instructions)
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
