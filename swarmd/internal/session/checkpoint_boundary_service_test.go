package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCheckpointBoundaryTransitionAssignsCheckpointToCurrentRunAtomically(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Done", Status: PlanCheckpointStatusCompleted, Handoff: &pebblestore.SessionPlanCheckpointHandoff{Title: "Done handoff", Overview: "The completed checkpoint handoff remains available across the boundary."}}})
	session, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session ownership: ok=%t err=%v", ok, err)
	}
	session.UserID = "user-boundary"
	session.AccountScopeID = "account-boundary"
	if _, err := svc.ApplySessionMutation(SessionMutationInput{
		SessionID: sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: "boundary-ownership", IdempotencyKey: "boundary-ownership", PayloadHash: "boundary-ownership", RequestHash: "boundary-ownership",
		Kind: SessionMutationUpdateMetadata, Session: &session,
	}); err != nil {
		t.Fatalf("set session ownership: %v", err)
	}
	now := time.Now().UnixMilli()
	if _, err := svc.ApplySessionMutation(SessionMutationInput{
		SessionID: sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: "source-run", IdempotencyKey: "source-run", PayloadHash: "source-run", RequestHash: "source-run",
		Kind: SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-source", Status: RunIntentPendingExecutor}, NowUnixMs: now,
	}); err != nil {
		t.Fatalf("record source run: %v", err)
	}
	if _, err := svc.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{
		SessionID: sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		ClientRequestID: "source-epoch", PayloadHash: "source-epoch", Reason: "source", RunID: "run-source", SkipRunIntent: true, NowUnixMs: now + 1,
	}); err != nil {
		t.Fatalf("begin source epoch: %v", err)
	}

	result, err := NewCheckpointBoundaryService(svc).Transition(CheckpointBoundaryTransitionInput{
		SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "audit the release", Title: "Release audit",
		Tasks: []string{"Audit release"}, AcceptanceCriteria: []string{"Audit is complete"},
		SourceMessageID: "message-boundary", SourceRunID: "run-source", StartedAt: now + 2,
	})
	if err != nil {
		t.Fatalf("transition checkpoint boundary: %v", err)
	}
	if result.CheckpointID != "followup-1" || result.AttemptID != "followup-1:attempt-1" || result.RunIntent.RunID != "run-source" || result.RunIntent.SourceMessageID != "message-boundary" || result.RunIntent.EpochID == "" {
		t.Fatalf("transition identity = %#v", result)
	}
	if result.Plan.Document == nil || result.Plan.Document.ActiveCheckpointID != result.CheckpointID || result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.CurrentRunID != "run-source" {
		t.Fatalf("committed plan = %#v", result.Plan.Document)
	}
	if got := result.Plan.Document.Checkpoints[0].Handoff; got == nil || got.Title != "Done handoff" || !strings.Contains(got.Overview, "remains available") {
		t.Fatalf("completed checkpoint handoff was not preserved: %#v", got)
	}
	if source, ok, err := svc.GetV3SessionRunIntent(sessionID, "run-source"); err != nil || !ok || source.Status != RunIntentPendingExecutor || source.CheckpointID != "followup-1" || source.AttemptID != "followup-1:attempt-1" {
		t.Fatalf("source run did not retain ownership of the checkpoint: %#v ok=%t err=%v", source, ok, err)
	}
	if active, ok, err := svc.GetSessionActiveRunIntent(sessionID); err != nil || !ok || active.RunID != "run-source" {
		t.Fatalf("active run = %#v ok=%t err=%v", active, ok, err)
	}
	epoch, ok, err := svc.GetActiveExecutionEpoch(sessionID)
	if err != nil || !ok || epoch.EpochID != result.RunIntent.EpochID {
		t.Fatalf("active epoch = %#v ok=%t err=%v", epoch, ok, err)
	}
	if epoch.Ordinal != 2 || epoch.Boundary.Reason != "source" || epoch.Boundary.CheckpointID != "" || epoch.Boundary.SourceMessageID != "" {
		t.Fatalf("checkpoint ownership mutated execution boundary: %#v", epoch)
	}

	replayed, err := NewCheckpointBoundaryService(svc).Transition(CheckpointBoundaryTransitionInput{
		SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "audit the release", Title: "Release audit",
		AcceptanceCriteria: []string{"Audit is complete"}, SourceMessageID: "message-boundary", SourceRunID: "run-source",
	})
	if err != nil || !replayed.Replayed || replayed.CheckpointID != result.CheckpointID || replayed.RunIntent.RunID != "run-source" {
		t.Fatalf("replay = %#v err=%v", replayed, err)
	}
}

func TestCheckpointBoundaryTransitionFailureLeavesPlanRunAndEpochUnchanged(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Done", Status: PlanCheckpointStatusCompleted, Handoff: &pebblestore.SessionPlanCheckpointHandoff{Title: "Done handoff", Overview: "The completed checkpoint handoff remains available across the boundary."}}})
	session, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session ownership: ok=%t err=%v", ok, err)
	}
	session.UserID = "user-boundary-failure"
	session.AccountScopeID = "account-boundary-failure"
	if _, err := svc.ApplySessionMutation(SessionMutationInput{SessionID: sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: "failure-ownership", IdempotencyKey: "failure-ownership", PayloadHash: "failure-ownership", RequestHash: "failure-ownership", Kind: SessionMutationUpdateMetadata, Session: &session}); err != nil {
		t.Fatalf("set session ownership: %v", err)
	}
	if _, err := svc.ApplySessionMutation(SessionMutationInput{SessionID: sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: "failure-source-run", IdempotencyKey: "failure-source-run", PayloadHash: "failure-source-run", RequestHash: "failure-source-run", Kind: SessionMutationRecordRunIntent, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-source", Status: RunIntentPendingExecutor}}); err != nil {
		t.Fatalf("record source run: %v", err)
	}
	if _, err := svc.BeginExecutionEpoch(pebblestore.BeginExecutionEpochInput{SessionID: sessionID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: "failure-source-epoch", PayloadHash: "failure-source-epoch", Reason: "source", RunID: "run-source", SkipRunIntent: true}); err != nil {
		t.Fatalf("begin source epoch: %v", err)
	}
	beforePlan, _, _ := svc.GetPlan(sessionID, plan.ID)
	beforeEpoch, _, _ := svc.GetActiveExecutionEpoch(sessionID)
	restore := svc.Store().SetCheckpointBoundaryCommitHookForTest(func(string) error { return errors.New("injected boundary failure") })
	_, err = NewCheckpointBoundaryService(svc).Transition(CheckpointBoundaryTransitionInput{SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "must not commit", AcceptanceCriteria: []string{"Never committed"}, SourceMessageID: "failure-message", SourceRunID: "run-source"})
	restore()
	if err == nil || !strings.Contains(err.Error(), "injected boundary failure") {
		t.Fatalf("transition error = %v", err)
	}
	afterPlan, _, _ := svc.GetPlan(sessionID, plan.ID)
	afterEpoch, _, _ := svc.GetActiveExecutionEpoch(sessionID)
	if afterPlan.Version != beforePlan.Version || len(afterPlan.Document.Checkpoints) != len(beforePlan.Document.Checkpoints) || afterEpoch.EpochID != beforeEpoch.EpochID || afterEpoch.Ordinal != beforeEpoch.Ordinal {
		t.Fatalf("state changed after failure: before plan=%#v epoch=%#v after plan=%#v epoch=%#v", beforePlan, beforeEpoch, afterPlan, afterEpoch)
	}
	if source, _, _ := svc.GetV3SessionRunIntent(sessionID, "run-source"); source.Status != RunIntentPendingExecutor {
		t.Fatalf("source run changed after failure: %#v", source)
	}
	if source, _, _ := svc.GetV3SessionRunIntent(sessionID, "run-source"); source.CheckpointID != "" || source.AttemptID != "" {
		t.Fatalf("source run acquired checkpoint ownership after failed boundary commit: %#v", source)
	}
}

func TestCheckpointBoundaryTransitionRejectsFailedAndConflictingRuns(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-failed", Title: "Failed", Status: PlanCheckpointStatusFailed}})
	_, err := NewCheckpointBoundaryService(svc).Transition(CheckpointBoundaryTransitionInput{
		SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "do not recover", AcceptanceCriteria: []string{"Never runs"},
		SourceMessageID: "message-failed", SourceRunID: "run-missing",
	})
	if err == nil || !strings.Contains(err.Error(), "active source run") {
		t.Fatalf("missing source run error = %v", err)
	}
}
