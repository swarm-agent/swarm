package session

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanLifecycleRequestFollowupCheckpointGlobalAutoStartPreparesFreshRun(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted}})
	lifecycle := NewPlanLifecycleService(svc)
	lifecycle.SetGlobalFollowupCheckpointPolicyResolver(func(accountScopeID string) (string, error) { return PlanFollowupCheckpointPolicyAutoStart, nil })

	result, err := lifecycle.RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:       sessionID,
		PlanID:          plan.ID,
		ChangeRequest:   "Add a final audit note.",
		Tasks:           []string{" Add a final audit note. "},
		RunID:           "run-followup-global",
		RunSessionID:    "child-session-global",
		ParentSessionID: sessionID,
		StartedAt:       1234,
	})
	if err != nil {
		t.Fatalf("request follow-up: %v", err)
	}
	if result.Action != "request_followup_checkpoint" {
		t.Fatalf("action = %q", result.Action)
	}
	if result.CheckpointID != "followup-1" {
		t.Fatalf("checkpoint_id = %q, want followup-1", result.CheckpointID)
	}
	if result.Summary.NextCheckpointStatus != PlanCheckpointStatusInProgress {
		t.Fatalf("next checkpoint status = %q, want in_progress", result.Summary.NextCheckpointStatus)
	}
	if result.Summary.AutoAdvanceAllowed != true {
		t.Fatalf("auto advance = false, want true from plan policy")
	}
	if result.Plan.Document == nil || len(result.Plan.Document.Checkpoints) != 2 {
		t.Fatalf("document checkpoints = %#v", result.Plan.Document)
	}
	followup := result.Plan.Document.Checkpoints[1]
	if followup.ID != "followup-1" || followup.Status != PlanCheckpointStatusInProgress || followup.Objective != "Add a final audit note." {
		t.Fatalf("follow-up checkpoint = %#v", followup)
	}
	if followup.RunID != "run-followup-global" || followup.AttemptID != "followup-1:attempt-1" || result.AttemptID != "followup-1:attempt-1" {
		t.Fatalf("auto-start should prepare run: checkpoint=%#v result_attempt=%q", followup, result.AttemptID)
	}
	if result.Plan.Document.ExecutionPolicy.FollowupCheckpointPolicy != "" {
		t.Fatalf("global default should not be persisted as plan override, got %q", result.Plan.Document.ExecutionPolicy.FollowupCheckpointPolicy)
	}
	currentSession, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if NormalizeMode(currentSession.Mode) != ModeAuto {
		t.Fatalf("mode = %q, want auto", currentSession.Mode)
	}
}

func TestPlanLifecycleRequestFollowupCheckpointAutoStartPreparesFreshRun(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:                     PlanExecutionPolicyModeAutomatic,
		Shape:                    PlanExecutionShapeCheckpointed,
		FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyAutoStart,
	}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted}})

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:       sessionID,
		PlanID:          plan.ID,
		ChangeRequest:   "Run another pass.",
		RunID:           "run-followup-1",
		RunSessionID:    "child-session-1",
		ParentSessionID: sessionID,
		StartedAt:       1234,
	})
	if err != nil {
		t.Fatalf("request follow-up auto-start: %v", err)
	}
	if result.CheckpointID != "followup-1" || result.AttemptID != "followup-1:attempt-1" {
		t.Fatalf("checkpoint/attempt = %q/%q", result.CheckpointID, result.AttemptID)
	}
	if result.Summary.NextCheckpointStatus != PlanCheckpointStatusInProgress {
		t.Fatalf("next checkpoint status = %q, want in_progress", result.Summary.NextCheckpointStatus)
	}
	followup := result.Plan.Document.Checkpoints[1]
	if followup.Status != PlanCheckpointStatusInProgress || followup.RunID != "run-followup-1" || followup.SessionID != "child-session-1" || followup.AttemptID != "followup-1:attempt-1" {
		t.Fatalf("started follow-up checkpoint = %#v", followup)
	}
	if result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.Status != PlanExecutionStateInProgress || result.Plan.Document.ExecutionState.CurrentRunID != "run-followup-1" {
		t.Fatalf("execution state = %#v", result.Plan.Document.ExecutionState)
	}
	currentSession, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if NormalizeMode(currentSession.Mode) != ModeAuto {
		t.Fatalf("mode = %q, want auto", currentSession.Mode)
	}
}

func TestPlanLifecycleSetFollowupCheckpointPolicyOverridePersistsWithoutModeSwitch(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted}})

	result, err := NewPlanLifecycleService(svc).SetFollowupCheckpointPolicy(PlanLifecycleFollowupPolicyInput{SessionID: sessionID, PlanID: plan.ID, FollowupCheckpointPolicy: "auto_start", Reason: "Allow follow-up add and start"})
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if result.Action != "set_followup_checkpoint_policy" {
		t.Fatalf("action = %q", result.Action)
	}
	if result.Plan.Document == nil || result.Plan.Document.ExecutionPolicy.FollowupCheckpointPolicy != PlanFollowupCheckpointPolicyAutoStart {
		t.Fatalf("policy = %#v", result.Plan.Document)
	}
	currentSession, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if NormalizeMode(currentSession.Mode) != ModeAuto {
		t.Fatalf("mode = %q, want auto", currentSession.Mode)
	}
}

func TestPlanLifecycleRequestFollowupCheckpointRequiresResolvedApprovalByDefault(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted}})

	_, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "Add one more thing."})
	if err == nil || !strings.Contains(err.Error(), "requires user approval") {
		t.Fatalf("error = %v, want approval requirement", err)
	}
}

func saveApprovedLifecyclePlan(t *testing.T, svc *Service, sessionID string, policy pebblestore.SessionPlanExecutionPolicy, checkpoints []pebblestore.SessionPlanCheckpoint) pebblestore.SessionPlanSnapshot {
	t.Helper()
	doc := &pebblestore.SessionPlanDocument{
		ID:              "plan-lifecycle-test",
		Title:           "Lifecycle Test Plan",
		Status:          "approved",
		ExecutionPolicy: policy,
		Checkpoints:     checkpoints,
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, doc.ID, doc.Title, "# Lifecycle Test Plan", "approved", "approved", true, PlanSaveMetadata{Document: doc})
	if err != nil {
		t.Fatalf("save approved plan: %v", err)
	}
	return plan
}
