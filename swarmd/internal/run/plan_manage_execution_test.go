package run

import (
	"encoding/json"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestExecutePlanManageCheckpointOutcomeUpdatesExecutionMetadata(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exec", "Execution Plan", "# Execution", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusInProgress},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save execution plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"checkpoint_outcome","checkpoint_id":"cp-1","outcome":"completed","attempt_id":"attempt-1","run_id":"run-1","run_session_id":"child-session","parent_session_id":"parent-session","report":"model complete","changed_files":["swarmd/internal/session/plan_execution.go"],"validation":["targeted test"]}`, "")
	if err != nil {
		t.Fatalf("checkpoint outcome: %v output=%s", err, raw)
	}
	var payload struct {
		Action string `json:"action"`
		Plan   struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Action != "checkpoint_outcome" {
		t.Fatalf("action = %q", payload.Action)
	}
	doc := payload.Plan.Document
	if doc == nil {
		t.Fatal("missing structured document in payload")
	}
	if doc.ActiveCheckpointID != "cp-2" {
		t.Fatalf("active checkpoint = %q, want cp-2", doc.ActiveCheckpointID)
	}
	if doc.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusCompleted || doc.Checkpoints[0].AttemptID != "attempt-1" || len(doc.Checkpoints[0].Attempts) != 1 {
		t.Fatalf("checkpoint metadata = %#v", doc.Checkpoints[0])
	}
	if doc.ExecutionState == nil || doc.ExecutionState.LastCheckpointID != "cp-1" || doc.ExecutionState.CurrentRunID != "run-1" || doc.ExecutionState.ParentSessionID != "parent-session" {
		t.Fatalf("execution state = %#v", doc.ExecutionState)
	}
}

func TestExecutePlanManageUpdateExecutionPolicy(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exec", "Execution Plan", "# Execution", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: sessionruntime.PlanCheckpointStatusPending}},
	}})
	if err != nil {
		t.Fatalf("save execution plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"update_execution_policy","execution_policy":{"mode":"automatic","shape":"checkpointed"}}`, "")
	if err != nil {
		t.Fatalf("update execution policy: %v output=%s", err, raw)
	}
	var payload struct {
		Plan struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Plan.Document == nil || payload.Plan.Document.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic || payload.Plan.Document.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed {
		t.Fatalf("execution policy = %#v", payload.Plan.Document)
	}
}

func TestExecutePlanManageStartAndContinueCheckpoint(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exec", "Execution Plan", "# Execution", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusPending},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save execution plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"start_checkpoint","attempt_id":"attempt-1","run_id":"run-1","run_session_id":"child-session","parent_session_id":"parent-session","started_at":1234}`, "")
	if err != nil {
		t.Fatalf("start checkpoint: %v output=%s", err, raw)
	}
	var payload struct {
		Action           string                              `json:"action"`
		CheckpointID     string                              `json:"checkpoint_id"`
		NextAction       string                              `json:"next_action"`
		ExecutionSummary sessionruntime.PlanExecutionSummary `json:"execution_summary"`
		Plan             struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Action != "start_checkpoint" || payload.CheckpointID != "cp-1" || payload.NextAction != "run_checkpoint_with_fresh_context" {
		t.Fatalf("start payload action=%q checkpoint=%q next=%q raw=%s", payload.Action, payload.CheckpointID, payload.NextAction, raw)
	}
	doc := payload.Plan.Document
	if doc == nil || doc.ActiveCheckpointID != "cp-1" || doc.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusInProgress || doc.Checkpoints[0].AttemptID != "attempt-1" {
		t.Fatalf("started document = %#v", doc)
	}
	if doc.ExecutionState == nil || doc.ExecutionState.Status != sessionruntime.PlanExecutionStateInProgress || doc.ExecutionState.ActiveAttemptID != "attempt-1" || doc.ExecutionState.CurrentRunID != "run-1" {
		t.Fatalf("execution state = %#v", doc.ExecutionState)
	}

	raw, err = runSvc.executePlanManageTool(sessionID, `{"action":"complete_checkpoint","report":"done"}`, "")
	if err != nil {
		t.Fatalf("complete checkpoint: %v output=%s", err, raw)
	}
	var completePayload struct {
		Action           string `json:"action"`
		NextCheckpointID string `json:"next_checkpoint_id"`
		NextAction       string `json:"next_action"`
		Plan             struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &completePayload); err != nil {
		t.Fatalf("decode complete payload: %v", err)
	}
	if completePayload.Action != "complete_checkpoint" || completePayload.NextCheckpointID != "cp-2" || completePayload.NextAction != "auto_advance_available" {
		t.Fatalf("complete payload action=%q next=%q next_action=%q raw=%s", completePayload.Action, completePayload.NextCheckpointID, completePayload.NextAction, raw)
	}
	if completePayload.Plan.Document == nil || completePayload.Plan.Document.ActiveCheckpointID != "cp-2" || completePayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusCompleted {
		t.Fatalf("completed document = %#v", completePayload.Plan.Document)
	}
}

func TestExecutePlanManageNeedsReviewAndBlockedStopAdvancement(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-exec", "Execution Plan", "# Execution", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusInProgress},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save execution plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"mark_needs_review","report":"needs audit"}`, "")
	if err != nil {
		t.Fatalf("mark needs review: %v output=%s", err, raw)
	}
	var reviewPayload struct {
		NextAction string `json:"next_action"`
		Plan       struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &reviewPayload); err != nil {
		t.Fatalf("decode review payload: %v", err)
	}
	if reviewPayload.NextAction != "await_review" || reviewPayload.Plan.Document.ActiveCheckpointID != "cp-1" || reviewPayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusNeedsReview {
		t.Fatalf("review payload=%s document=%#v", raw, reviewPayload.Plan.Document)
	}

	_, _, err = sessionSvc.SavePlanWithMetadata(sessionID, "plan-blocked", "Blocked Plan", "# Blocked", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-a", Title: "Blocked", Status: sessionruntime.PlanCheckpointStatusInProgress},
			{ID: "cp-b", Title: "Next", Status: sessionruntime.PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-a",
	}})
	if err != nil {
		t.Fatalf("save blocked plan: %v", err)
	}
	raw, err = runSvc.executePlanManageTool(sessionID, `{"action":"mark_blocked","report":"dependency missing"}`, "")
	if err != nil {
		t.Fatalf("mark blocked: %v output=%s", err, raw)
	}
	var blockedPayload struct {
		NextAction string `json:"next_action"`
		Plan       struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &blockedPayload); err != nil {
		t.Fatalf("decode blocked payload: %v", err)
	}
	if blockedPayload.NextAction != "stopped" || blockedPayload.Plan.Document.ActiveCheckpointID != "cp-a" || blockedPayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusBlocked {
		t.Fatalf("blocked payload=%s document=%#v", raw, blockedPayload.Plan.Document)
	}
}
