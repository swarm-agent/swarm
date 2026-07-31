package session

import (
	"fmt"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanLifecycleCancelledRunCanContinueUnderNewOwnerAndComplete(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	lifecycle := NewPlanLifecycleService(svc)
	applyMutation := func(input SessionMutationInput) (SessionMutationResult, error) {
		input.UserID = "user-test"
		input.AccountScopeID = "account-test"
		return svc.ApplySessionMutation(input)
	}
	lifecycle.SetApplySessionMutation(applyMutation)

	started, err := lifecycle.StartSessionCheckpoint(PlanLifecycleSessionCheckpointInput{
		SessionID: sessionID, ChangeRequest: "finish durable checkpoint", Title: "Durable checkpoint",
		Tasks: []string{"finish work"}, AcceptanceCriteria: []string{"work is complete"},
		RunID: "run-a", RunSessionID: sessionID, ParentSessionID: sessionID, StartedAt: 100,
	})
	if err != nil {
		t.Fatalf("start checkpoint under run A: %v", err)
	}
	if started.Plan.Document.ExecutionState.CurrentRunID != "run-a" {
		t.Fatalf("run A did not own checkpoint: %#v", started.Plan.Document.ExecutionState)
	}

	if err := recordPlanLifecycleTestRunIntent(svc, sessionID, "run-a", RunIntentCancelled, "user stopped", 200); err != nil {
		t.Fatalf("record run A cancellation: %v", err)
	}
	stale, ok, err := svc.GetActivePlan(sessionID)
	if err != nil || !ok || stale.Document == nil || stale.Document.Checkpoints[0].Status != PlanCheckpointStatusInProgress || stale.Document.ExecutionState.CurrentRunID != "run-a" {
		t.Fatalf("broken sequence was not reproduced before recovery: ok=%v err=%v plan=%#v", ok, err, stale)
	}
	if err := recordPlanLifecycleTestRunIntent(svc, sessionID, "run-b", RunIntentRunning, "", 300); err != nil {
		t.Fatalf("record run B: %v", err)
	}

	continued, err := lifecycle.ContinueCheckpoint(PlanLifecycleExecutionInput{
		SessionID: sessionID, PlanID: started.Plan.ID, CheckpointID: "cp-1",
		RunID: "run-b", RunSessionID: sessionID, ParentSessionID: sessionID, StartedAt: 300,
	})
	if err != nil {
		t.Fatalf("continue stopped checkpoint under run B: %v", err)
	}
	checkpoint := continued.Plan.Document.Checkpoints[0]
	if continued.AttemptID != "cp-1:attempt-2" || continued.Plan.Document.ExecutionState.CurrentRunID != "run-b" || checkpoint.RunID != "run-b" {
		t.Fatalf("continued ownership = result=%#v state=%#v checkpoint=%#v", continued, continued.Plan.Document.ExecutionState, checkpoint)
	}
	if len(checkpoint.Attempts) != 2 || checkpoint.Attempts[0].Status != PlanCheckpointStatusPaused || checkpoint.Attempts[1].Status != PlanCheckpointStatusInProgress {
		t.Fatalf("continued attempt history = %#v", checkpoint.Attempts)
	}

	prepared, err := svc.PreparePlanPatch(sessionID, PlanPatchOptions{
		PlanID: started.Plan.ID,
		DocumentPatch: &PlanDocumentPatch{
			Operation: "complete_checkpoint", CheckpointID: "cp-1", Status: PlanCheckpointStatusCompleted,
			AttemptID: continued.AttemptID, RunID: "run-b", RunSessionID: sessionID, ParentSessionID: sessionID,
			Report: "completed by run B", Result: "done", CompletedAt: 400,
		},
	})
	if err != nil {
		t.Fatalf("prepare run B completion: %v", err)
	}
	completedMutation, err := svc.CommitPreparedPlanSave(prepared, applyMutation)
	if err != nil {
		t.Fatalf("commit run B completion: %v", err)
	}
	completed := completedMutation.Plan.Document.Checkpoints[0]
	if completed.Status != PlanCheckpointStatusCompleted || completed.RunID != "run-b" || completed.AttemptID != "cp-1:attempt-2" {
		t.Fatalf("completed checkpoint = %#v", completed)
	}
	if len(completed.Attempts) != 2 || completed.Attempts[0].Outcome != PlanCheckpointStatusPaused || completed.Attempts[1].Outcome != PlanCheckpointStatusCompleted {
		t.Fatalf("completed attempt history = %#v", completed.Attempts)
	}
}

func recordPlanLifecycleTestRunIntent(svc *Service, sessionID, runID, status, reason string, now int64) error {
	apply := func(nextStatus, nextReason string, at int64) error {
		requestID := fmt.Sprintf("plan-lifecycle-run-%s-%s-%d", runID, nextStatus, at)
		_, err := svc.ApplySessionMutation(SessionMutationInput{
			SessionID: sessionID, UserID: "user-test", AccountScopeID: "account-test",
			ClientRequestID: requestID, IdempotencyKey: requestID,
			PayloadHash: requestID, RequestHash: requestID, Kind: SessionMutationRecordRunIntent,
			EventType: "session.run." + nextStatus,
			RunIntent: &pebblestore.V3SessionRunIntent{SessionID: sessionID, RunID: runID, Status: nextStatus, BlockedReason: nextReason, UpdatedAt: at},
			NowUnixMs: time.Now().UnixMilli(),
		})
		return err
	}
	if status != RunIntentPendingExecutor {
		if err := apply(RunIntentPendingExecutor, "", now-1); err != nil {
			return err
		}
	}
	return apply(status, reason, now)
}
