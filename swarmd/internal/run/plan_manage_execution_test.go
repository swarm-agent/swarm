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
	if completePayload.Action != "complete_checkpoint" || completePayload.NextCheckpointID != "cp-2" || completePayload.NextAction != "run_checkpoint_with_fresh_context" {
		t.Fatalf("complete payload action=%q next=%q next_action=%q raw=%s", completePayload.Action, completePayload.NextCheckpointID, completePayload.NextAction, raw)
	}
	if completePayload.Plan.Document == nil || completePayload.Plan.Document.ActiveCheckpointID != "cp-2" || completePayload.Plan.Document.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusCompleted {
		t.Fatalf("completed document = %#v", completePayload.Plan.Document)
	}
}

func TestExecutePlanManageApproveAndStartAppliesExecutionPolicy(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-approve", "Approve Plan", "# Approve", "draft", "pending", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeAutomatic, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateInProgress, ActiveAttemptID: "ai-attempt", CurrentRunID: "ai-run"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusCompleted, AttemptID: "ai-attempt", RunID: "ai-run", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "ai-attempt", Status: sessionruntime.PlanCheckpointStatusCompleted}}},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusInProgress},
		},
		ActiveCheckpointID: "cp-2",
	}})
	if err != nil {
		t.Fatalf("save approve plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"approve_and_start","execution_granularity":"checkpointed","continue_automatically":true}`, "")
	if err != nil {
		t.Fatalf("approve and start: %v output=%s", err, raw)
	}
	var payload struct {
		Action       string `json:"action"`
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		RunRequest   struct {
			PlanCheckpointContext struct {
				PlanID       string `json:"plan_id"`
				CheckpointID string `json:"checkpoint_id"`
			} `json:"plan_checkpoint_context"`
		} `json:"run_request"`
		Plan struct {
			Status        string                           `json:"status"`
			ApprovalState string                           `json:"approval_state"`
			Document      *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode approve payload: %v", err)
	}
	if payload.Action != "approve_and_start" || payload.NextAction != "run_checkpoint_with_fresh_context" || payload.CheckpointID != "cp-1" {
		t.Fatalf("approve payload action=%q checkpoint=%q next=%q raw=%s", payload.Action, payload.CheckpointID, payload.NextAction, raw)
	}
	if payload.RunRequest.PlanCheckpointContext.PlanID != "plan-approve" || payload.RunRequest.PlanCheckpointContext.CheckpointID != "cp-1" {
		t.Fatalf("run request = %#v", payload.RunRequest.PlanCheckpointContext)
	}
	doc := payload.Plan.Document
	if payload.Plan.Status != "approved" || payload.Plan.ApprovalState != "approved" || doc == nil {
		t.Fatalf("approved plan = %#v doc=%#v", payload.Plan, doc)
	}
	if doc.ExecutionPolicy.Mode != sessionruntime.PlanExecutionPolicyModeAutomatic || doc.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeCheckpointed || doc.ExecutionState != nil || doc.ActiveCheckpointID != "cp-1" {
		t.Fatalf("approved document policy/state = %#v", doc)
	}
	for _, checkpoint := range doc.Checkpoints {
		if checkpoint.Status != sessionruntime.PlanCheckpointStatusPending || checkpoint.AttemptID != "" || len(checkpoint.Attempts) != 0 {
			t.Fatalf("checkpoint runtime was not reset: %#v", checkpoint)
		}
	}
}

func TestExecutePlanManageApproveAndStartRunThroughCollapsesToSingleCheckpoint(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-single", "Single Plan", "# Single", "draft", "pending", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Objective: "Build model", Tasks: []string{"Task A"}, AcceptanceCriteria: []string{"A done"}},
			{ID: "cp-2", Title: "API", Objective: "Build API", Tasks: []string{"Task B"}, AcceptanceCriteria: []string{"B done"}},
		},
	}})
	if err != nil {
		t.Fatalf("save single plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"approve_and_start","execution_granularity":"run_through"}`, "")
	if err != nil {
		t.Fatalf("approve run through: %v output=%s", err, raw)
	}
	var payload struct {
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode run-through payload: %v", err)
	}
	doc := payload.Plan.Document
	if doc == nil || doc.ExecutionPolicy.Shape != sessionruntime.PlanExecutionShapeSingleRun || len(doc.Checkpoints) != 1 || payload.CheckpointID != "plan-run" {
		t.Fatalf("run-through document = %#v raw=%s", doc, raw)
	}
	if doc.ActiveCheckpointID != "plan-run" || doc.Checkpoints[0].Status != sessionruntime.PlanCheckpointStatusPending || len(doc.Checkpoints[0].Tasks) == 0 || len(doc.Checkpoints[0].AcceptanceCriteria) != 2 {
		t.Fatalf("collapsed checkpoint = %#v", doc.Checkpoints[0])
	}
}

func TestExecutePlanManageAcceptRestartAndRewind(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-control", "Control Plan", "# Control", "approved", "approved", true, sessionruntime.PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: sessionruntime.PlanExecutionPolicyModeReviewEachCheckpoint, Shape: sessionruntime.PlanExecutionShapeCheckpointed},
		ExecutionState:  &pebblestore.SessionPlanExecutionState{Status: sessionruntime.PlanExecutionStateWaitingReview, ActiveAttemptID: "cp-1:attempt-1"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: sessionruntime.PlanCheckpointStatusCompleted, Review: &pebblestore.SessionPlanCheckpointReview{Status: sessionruntime.PlanCheckpointReviewStatusPending}, AttemptID: "cp-1:attempt-1", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-1:attempt-1", Status: sessionruntime.PlanCheckpointStatusCompleted}}},
			{ID: "cp-2", Title: "API", Status: sessionruntime.PlanCheckpointStatusFailed, AttemptID: "cp-2:attempt-1", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-2:attempt-1", Status: sessionruntime.PlanCheckpointStatusFailed}}},
			{ID: "cp-3", Title: "UI", Status: sessionruntime.PlanCheckpointStatusCompleted, AttemptID: "cp-3:attempt-1", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-3:attempt-1", Status: sessionruntime.PlanCheckpointStatusCompleted}}},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save control plan: %v", err)
	}

	raw, err := runSvc.executePlanManageTool(sessionID, `{"action":"accept_and_continue","checkpoint_id":"cp-1","notes":"approved"}`, "")
	if err != nil {
		t.Fatalf("accept and continue: %v output=%s", err, raw)
	}
	var acceptPayload struct {
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &acceptPayload); err != nil {
		t.Fatalf("decode accept payload: %v", err)
	}
	if acceptPayload.NextAction != "stopped" || acceptPayload.Plan.Document.Checkpoints[0].Review.Status != sessionruntime.PlanCheckpointReviewStatusApproved || acceptPayload.Plan.Document.ActiveCheckpointID != "cp-2" {
		t.Fatalf("accept payload raw=%s doc=%#v", raw, acceptPayload.Plan.Document)
	}

	raw, err = runSvc.executePlanManageTool(sessionID, `{"action":"restart_checkpoint","checkpoint_id":"cp-2"}`, "")
	if err != nil {
		t.Fatalf("restart checkpoint: %v output=%s", err, raw)
	}
	var restartPayload struct {
		NextAction   string `json:"next_action"`
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &restartPayload); err != nil {
		t.Fatalf("decode restart payload: %v", err)
	}
	restarted := restartPayload.Plan.Document
	if restartPayload.NextAction != "run_checkpoint_with_fresh_context" || restartPayload.CheckpointID != "cp-2" || restarted.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending || len(restarted.Checkpoints[1].Attempts) != 0 || restarted.Checkpoints[2].Status != sessionruntime.PlanCheckpointStatusCompleted {
		t.Fatalf("restart payload raw=%s doc=%#v", raw, restarted)
	}

	raw, err = runSvc.executePlanManageTool(sessionID, `{"action":"rewind_to_checkpoint","checkpoint_id":"cp-2"}`, "")
	if err != nil {
		t.Fatalf("rewind checkpoint: %v output=%s", err, raw)
	}
	var rewindPayload struct {
		CheckpointID string `json:"checkpoint_id"`
		Plan         struct {
			Document *pebblestore.SessionPlanDocument `json:"document"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(raw), &rewindPayload); err != nil {
		t.Fatalf("decode rewind payload: %v", err)
	}
	rewound := rewindPayload.Plan.Document
	if rewindPayload.CheckpointID != "cp-2" || rewound.ActiveCheckpointID != "cp-2" || rewound.Checkpoints[1].Status != sessionruntime.PlanCheckpointStatusPending || rewound.Checkpoints[2].Status != sessionruntime.PlanCheckpointStatusPending || len(rewound.Checkpoints[2].Attempts) != 0 {
		t.Fatalf("rewind payload raw=%s doc=%#v", raw, rewound)
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
