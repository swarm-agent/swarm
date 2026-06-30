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

func TestPlanLifecycleRequestFollowupCheckpointAutoStartWithoutRunIDQueuesPendingCheckpoint(t *testing.T) {
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
		SessionID:     sessionID,
		PlanID:        plan.ID,
		ChangeRequest: "Run another pass.",
	})
	if err != nil {
		t.Fatalf("request follow-up auto-start: %v", err)
	}
	if result.CheckpointID != "followup-1" || result.AttemptID != "" {
		t.Fatalf("checkpoint/attempt = %q/%q", result.CheckpointID, result.AttemptID)
	}
	if result.Summary.NextCheckpointID != "followup-1" || result.Summary.NextCheckpointStatus != PlanCheckpointStatusPending {
		t.Fatalf("next checkpoint = %q/%q, want followup-1/pending", result.Summary.NextCheckpointID, result.Summary.NextCheckpointStatus)
	}
	if !result.Summary.AutoAdvanceAllowed {
		t.Fatalf("auto advance = false, want true")
	}
	followup := result.Plan.Document.Checkpoints[1]
	if followup.Status != PlanCheckpointStatusPending || followup.RunID != "" || followup.SessionID != "" || followup.AttemptID != "" {
		t.Fatalf("queued follow-up checkpoint = %#v", followup)
	}
	if result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.Status != PlanExecutionStateIdle || result.Plan.Document.ExecutionState.CurrentRunID != "" {
		t.Fatalf("execution state = %#v", result.Plan.Document.ExecutionState)
	}
}

func TestPlanLifecycleRequestFollowupCheckpointInsertsBeforeLaterActiveCheckpoint(t *testing.T) {
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
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted},
		{ID: "cp-2", Title: "Two", Status: PlanCheckpointStatusCompleted},
		{ID: "cp-3", Title: "Three", Status: PlanCheckpointStatusCompleted},
		{ID: "cp-4", Title: "Four", Status: PlanCheckpointStatusPending},
	})

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:       sessionID,
		PlanID:          plan.ID,
		ChangeRequest:   "Handle feedback before checkpoint four.",
		RunID:           "run-followup-1",
		RunSessionID:    "child-followup-1",
		ParentSessionID: sessionID,
		StartedAt:       1234,
	})
	if err != nil {
		t.Fatalf("request follow-up auto-start: %v", err)
	}
	if result.CheckpointID != "followup-1" || result.Plan.Document.ActiveCheckpointID != "followup-1" {
		t.Fatalf("checkpoint/active = %q/%q", result.CheckpointID, result.Plan.Document.ActiveCheckpointID)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,cp-2,cp-3,followup-1,cp-4" {
		t.Fatalf("checkpoint order = %v", got)
	}
	followup := result.Plan.Document.Checkpoints[3]
	later := result.Plan.Document.Checkpoints[4]
	if followup.ID != "followup-1" || followup.Order != 4 || followup.Status != PlanCheckpointStatusInProgress || followup.RunID != "run-followup-1" {
		t.Fatalf("inserted follow-up = %#v", followup)
	}
	if later.ID != "cp-4" || later.Order != 5 || later.Status != PlanCheckpointStatusPending {
		t.Fatalf("later checkpoint = %#v", later)
	}
	assertCheckpointOrdersNormalized(t, result.Plan.Document)
}

func TestPlanLifecycleRequestFollowupCheckpointInsertsFractionalIDBeforeActiveFollowup(t *testing.T) {
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
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted},
		{ID: "followup-1", Title: "Follow-up one", Status: PlanCheckpointStatusCompleted},
		{ID: "followup-2", Title: "Follow-up two", Status: PlanCheckpointStatusPending},
	})

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:       sessionID,
		PlanID:          plan.ID,
		ChangeRequest:   "Handle feedback before active follow-up two.",
		RunID:           "run-followup-1-5",
		RunSessionID:    "child-followup-1-5",
		ParentSessionID: sessionID,
		StartedAt:       1234,
	})
	if err != nil {
		t.Fatalf("request follow-up auto-start: %v", err)
	}
	if result.CheckpointID != "followup-1.5" || result.Plan.Document.ActiveCheckpointID != "followup-1.5" {
		t.Fatalf("checkpoint/active = %q/%q", result.CheckpointID, result.Plan.Document.ActiveCheckpointID)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,followup-1,followup-1.5,followup-2" {
		t.Fatalf("checkpoint order = %v", got)
	}
	followup := result.Plan.Document.Checkpoints[2]
	later := result.Plan.Document.Checkpoints[3]
	if followup.ID != "followup-1.5" || followup.Order != 3 || followup.Status != PlanCheckpointStatusInProgress || followup.RunID != "run-followup-1-5" {
		t.Fatalf("inserted follow-up = %#v", followup)
	}
	if later.ID != "followup-2" || later.Order != 4 || later.Status != PlanCheckpointStatusPending {
		t.Fatalf("later checkpoint = %#v", later)
	}
	assertCheckpointOrdersNormalized(t, result.Plan.Document)
}

func TestPlanLifecycleRequestFollowupCheckpointApprovalInsertsBeforeLaterActiveCheckpoint(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:                     PlanExecutionPolicyModeAutomatic,
		Shape:                    PlanExecutionShapeCheckpointed,
		FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyRequireApproval,
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted},
		{ID: "cp-4", Title: "Four", Status: PlanCheckpointStatusPending},
	})

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:         sessionID,
		PlanID:            plan.ID,
		ChangeRequest:     "Handle approved feedback before checkpoint four.",
		ApprovalConfirmed: true,
	})
	if err != nil {
		t.Fatalf("request approved follow-up: %v", err)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,followup-1,cp-4" {
		t.Fatalf("checkpoint order = %v", got)
	}
	followup := result.Plan.Document.Checkpoints[1]
	if result.Plan.Document.ActiveCheckpointID != "followup-1" || result.Summary.NextCheckpointID != "followup-1" || followup.Order != 2 || followup.Status != PlanCheckpointStatusPending {
		t.Fatalf("follow-up active/summary/checkpoint = active %q summary %q checkpoint %#v", result.Plan.Document.ActiveCheckpointID, result.Summary.NextCheckpointID, followup)
	}
	if later := result.Plan.Document.Checkpoints[2]; later.ID != "cp-4" || later.Order != 3 || later.Status != PlanCheckpointStatusPending {
		t.Fatalf("later checkpoint = %#v", later)
	}
	assertCheckpointOrdersNormalized(t, result.Plan.Document)
}

func TestPlanLifecycleRequestFollowupCheckpointClosesWaitingReviewBeforeInserting(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-lifecycle-test",
		Title: "Lifecycle Test Plan",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:                     PlanExecutionPolicyModeAutomatic,
			Shape:                    PlanExecutionShapeCheckpointed,
			FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyAutoStart,
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted},
			{ID: "cp-2", Title: "Two", Status: PlanCheckpointStatusCompleted, Review: &pebblestore.SessionPlanCheckpointReview{Status: PlanCheckpointReviewStatusPending}},
			{ID: "cp-3", Title: "Three", Status: PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-2",
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, doc.ID, doc.Title, "# Lifecycle Test Plan", "approved", "approved", true, PlanSaveMetadata{Document: doc})
	if err != nil {
		t.Fatalf("save waiting-review plan: %v", err)
	}

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:       sessionID,
		PlanID:          plan.ID,
		ChangeRequest:   "Handle review feedback before checkpoint three.",
		RunID:           "run-followup-1",
		RunSessionID:    "child-followup-1",
		ParentSessionID: sessionID,
		StartedAt:       1234,
	})
	if err != nil {
		t.Fatalf("request follow-up for waiting review: %v", err)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,cp-2,followup-1,cp-3" {
		t.Fatalf("checkpoint order = %v", got)
	}
	reviewed := result.Plan.Document.Checkpoints[1]
	followup := result.Plan.Document.Checkpoints[2]
	if reviewed.Status != PlanCheckpointStatusCompleted || reviewed.Review == nil || reviewed.Review.Status != PlanCheckpointReviewStatusApproved || reviewed.Result != "superseded_by_followup" {
		t.Fatalf("waiting review checkpoint was not closed: %#v", reviewed)
	}
	if result.Plan.Document.ActiveCheckpointID != "followup-1" || followup.Status != PlanCheckpointStatusInProgress || followup.RunID != "run-followup-1" {
		t.Fatalf("follow-up not active/started: active=%q followup=%#v", result.Plan.Document.ActiveCheckpointID, followup)
	}
	if later := result.Plan.Document.Checkpoints[3]; later.ID != "cp-3" || later.Status != PlanCheckpointStatusPending {
		t.Fatalf("later checkpoint = %#v", later)
	}
	assertCheckpointOrdersNormalized(t, result.Plan.Document)
}

func TestPlanLifecycleRequestFollowupCheckpointClosesFinalReviewBeforeAppending(t *testing.T) {
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
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted},
	})
	plan.Document.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateWaitingReview, LastCheckpointID: "cp-1"}
	plan.Document.ActiveCheckpointID = "cp-1"
	plan, _, err := svc.SavePlanWithMetadata(sessionID, plan.ID, plan.Title, plan.Plan, plan.Status, plan.ApprovalState, true, PlanSaveMetadata{Document: plan.Document})
	if err != nil {
		t.Fatalf("save final review state: %v", err)
	}

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:       sessionID,
		PlanID:          plan.ID,
		ChangeRequest:   "Add post-review follow-up.",
		RunID:           "run-followup-final",
		RunSessionID:    "child-followup-final",
		ParentSessionID: sessionID,
		StartedAt:       1234,
	})
	if err != nil {
		t.Fatalf("request follow-up for final review: %v", err)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,followup-1" {
		t.Fatalf("checkpoint order = %v", got)
	}
	closed := result.Plan.Document.Checkpoints[0]
	if closed.Review == nil || closed.Review.Status != PlanCheckpointReviewStatusApproved || closed.Result != "superseded_by_followup" {
		t.Fatalf("final review checkpoint not closed: %#v", closed)
	}
	followup := result.Plan.Document.Checkpoints[1]
	if result.Plan.Document.ActiveCheckpointID != "followup-1" || followup.Status != PlanCheckpointStatusInProgress || result.Summary.ReviewRequired {
		t.Fatalf("follow-up active/summary = active %q followup %#v summary %#v", result.Plan.Document.ActiveCheckpointID, followup, result.Summary)
	}
}

func TestPlanLifecycleRequestFollowupCheckpointResolvesInProgressBeforeApprovalAppend(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:                     PlanExecutionPolicyModeAutomatic,
		Shape:                    PlanExecutionShapeCheckpointed,
		FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyRequireApproval,
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusInProgress, AttemptID: "cp-1:attempt-1", RunID: "run-cp-1", SessionID: "child-cp-1", StartedAt: 100},
	})

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:         sessionID,
		PlanID:            plan.ID,
		ChangeRequest:     "Add approved follow-up.",
		ApprovalConfirmed: true,
	})
	if err != nil {
		t.Fatalf("request follow-up append: %v", err)
	}
	if result.CheckpointID != "followup-1" || result.Plan.Document.ActiveCheckpointID != "followup-1" {
		t.Fatalf("checkpoint/active = %q/%q", result.CheckpointID, result.Plan.Document.ActiveCheckpointID)
	}
	followup := result.Plan.Document.Checkpoints[0]
	old := result.Plan.Document.Checkpoints[1]
	if followup.ID != "followup-1" || followup.Status != PlanCheckpointStatusPending {
		t.Fatalf("follow-up status = %#v, want pending before old checkpoint", followup)
	}
	if old.ID != "cp-1" || old.Status == PlanCheckpointStatusInProgress || old.Status != PlanCheckpointStatusNeedsReview {
		t.Fatalf("old checkpoint status = %#v, want superseded needs_review", old)
	}
	if old.Result != "superseded_by_followup" || old.Review == nil || old.Review.Status != PlanCheckpointReviewStatusPending {
		t.Fatalf("old checkpoint supersede metadata = %#v", old)
	}
	assertNoInProgressBeforeActive(t, result.Plan.Document)
}

func TestPlanLifecycleRequestFollowupCheckpointAutoStartResolvesInProgressBeforeStartingFollowup(t *testing.T) {
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
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusInProgress, AttemptID: "cp-1:attempt-1", RunID: "run-cp-1", SessionID: "child-cp-1", StartedAt: 100},
	})

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:       sessionID,
		PlanID:          plan.ID,
		ChangeRequest:   "Auto-start follow-up.",
		RunID:           "run-followup-1",
		RunSessionID:    "child-followup-1",
		ParentSessionID: sessionID,
		StartedAt:       200,
	})
	if err != nil {
		t.Fatalf("request follow-up auto-start: %v", err)
	}
	followup := result.Plan.Document.Checkpoints[0]
	old := result.Plan.Document.Checkpoints[1]
	if old.ID != "cp-1" || old.Status != PlanCheckpointStatusNeedsReview || old.Result != "superseded_by_followup" {
		t.Fatalf("old checkpoint = %#v", old)
	}
	if followup.ID != "followup-1" || followup.Status != PlanCheckpointStatusInProgress || followup.RunID != "run-followup-1" || result.AttemptID != "followup-1:attempt-1" {
		t.Fatalf("follow-up checkpoint = %#v result_attempt=%q", followup, result.AttemptID)
	}
	if result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.Status != PlanExecutionStateInProgress || result.Plan.Document.ExecutionState.LastCheckpointID != "cp-1" {
		t.Fatalf("execution state = %#v", result.Plan.Document.ExecutionState)
	}
	assertNoInProgressBeforeActive(t, result.Plan.Document)
}

func TestPlanLifecycleRequestFollowupCheckpointAutoStartInsertsBeforeRunningFollowup(t *testing.T) {
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
	lifecycle := NewPlanLifecycleService(svc)

	first, err := lifecycle.RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "First follow-up.", RunID: "run-followup-1", RunSessionID: "child-followup-1", ParentSessionID: sessionID, StartedAt: 100})
	if err != nil {
		t.Fatalf("first follow-up: %v", err)
	}
	second, err := lifecycle.RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "Second follow-up.", RunID: "run-followup-2", RunSessionID: "child-followup-2", ParentSessionID: sessionID, StartedAt: 200})
	if err != nil {
		t.Fatalf("second follow-up: %v", err)
	}
	if first.CheckpointID != "followup-1" || second.CheckpointID != "followup-0.5" || second.AttemptID != "followup-0.5:attempt-1" {
		t.Fatalf("checkpoint sequence = %q/%q attempt=%q", first.CheckpointID, second.CheckpointID, second.AttemptID)
	}
	if len(second.Plan.Document.Checkpoints) != 3 {
		t.Fatalf("checkpoint count = %d, want 3", len(second.Plan.Document.Checkpoints))
	}
	followup2 := second.Plan.Document.Checkpoints[1]
	followup1 := second.Plan.Document.Checkpoints[2]
	if followup1.ID != "followup-1" || followup1.Status != PlanCheckpointStatusNeedsReview || followup1.Result != "superseded_by_followup" {
		t.Fatalf("first follow-up after second append = %#v", followup1)
	}
	if followup2.ID != "followup-0.5" || followup2.Status != PlanCheckpointStatusInProgress || followup2.RunID != "run-followup-2" || second.Plan.Document.ActiveCheckpointID != "followup-0.5" {
		t.Fatalf("second follow-up active state = checkpoint %#v active=%q", followup2, second.Plan.Document.ActiveCheckpointID)
	}
	assertNoInProgressBeforeActive(t, second.Plan.Document)
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

func checkpointIDs(checkpoints []pebblestore.SessionPlanCheckpoint) []string {
	ids := make([]string, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		ids = append(ids, strings.TrimSpace(checkpoint.ID))
	}
	return ids
}

func assertCheckpointOrdersNormalized(t *testing.T, doc *pebblestore.SessionPlanDocument) {
	t.Helper()
	if doc == nil {
		t.Fatalf("document is nil")
	}
	for i, checkpoint := range doc.Checkpoints {
		if checkpoint.Order != i+1 {
			t.Fatalf("checkpoint %q order = %d, want %d", checkpoint.ID, checkpoint.Order, i+1)
		}
	}
}

func assertNoInProgressBeforeActive(t *testing.T, doc *pebblestore.SessionPlanDocument) {
	t.Helper()
	if doc == nil {
		t.Fatalf("document is nil")
	}
	activeIdx := findPlanCheckpointIndex(doc.Checkpoints, doc.ActiveCheckpointID)
	if activeIdx < 0 {
		t.Fatalf("active checkpoint %q not found", doc.ActiveCheckpointID)
	}
	for i := 0; i < activeIdx; i++ {
		checkpoint := doc.Checkpoints[i]
		if checkpoint.Status == PlanCheckpointStatusInProgress {
			t.Fatalf("checkpoint %q before active %q is still in_progress", checkpoint.ID, doc.ActiveCheckpointID)
		}
		if checkpoint.Status == PlanCheckpointStatusCompleted && checkpoint.Review != nil && checkpoint.Review.Status == PlanCheckpointReviewStatusPending {
			t.Fatalf("checkpoint %q before active %q is still waiting for review", checkpoint.ID, doc.ActiveCheckpointID)
		}
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
