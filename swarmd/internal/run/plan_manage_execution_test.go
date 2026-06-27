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
