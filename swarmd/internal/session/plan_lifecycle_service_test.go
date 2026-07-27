package session

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRequestFollowupCheckpointWithoutActivePlanUsesAtomicSessionStart(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	lifecycle := NewPlanLifecycleService(svc)
	result, err := lifecycle.RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:          sessionID,
		ChangeRequest:      "fix the timer",
		Title:              "Fix timer",
		Tasks:              []string{"Repair timer"},
		AcceptanceCriteria: []string{"Timer refreshes"},
		RunID:              "run-atomic",
		RunSessionID:       sessionID,
		ParentSessionID:    sessionID,
		StartedAt:          1234,
	})
	if err != nil {
		t.Fatalf("request follow-up without plan: %v", err)
	}
	if result.Action != "start_session_checkpoint" || result.CheckpointID != "cp-1" || result.AttemptID != "cp-1:attempt-1" {
		t.Fatalf("normalized lifecycle result = %#v", result)
	}
	if result.Plan.Document == nil || result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.CurrentRunID != "run-atomic" {
		t.Fatalf("normalized lifecycle document = %#v", result.Plan.Document)
	}
	if got := result.Plan.Document.Checkpoints[0].Status; got != PlanCheckpointStatusInProgress {
		t.Fatalf("checkpoint status = %q, want in_progress", got)
	}
}

func TestPlanLifecycleCheckpointSavePreservesSessionMetadataAndNamespacesPlanFields(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	before, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session before checkpoint save: session=%+v ok=%v err=%v", before, ok, err)
	}
	before.Metadata = map[string]any{"sidebar_label": "Durable label"}
	before, _, err = svc.UpdateMetadata(sessionID, before.Metadata)
	if err != nil {
		t.Fatalf("set durable session metadata: %v", err)
	}

	_, event, err := svc.SavePlanWithMetadata(sessionID, "plan-checkpoint", "Checkpoint title", "# Checkpoint", "approved", "approved", true, PlanSaveMetadata{
		Checkpoint: true,
		Document: &pebblestore.SessionPlanDocument{
			ID:    "plan-checkpoint",
			Title: "Checkpoint title",
			Info:  pebblestore.SessionPlanInfo{Goal: "preserve session metadata"},
			Checkpoints: []pebblestore.SessionPlanCheckpoint{{
				ID: "cp-1", Title: "Checkpoint title", Status: PlanCheckpointStatusInProgress, Order: 1,
			}},
		},
	})
	if err != nil {
		t.Fatalf("save checkpoint plan: %v", err)
	}
	after, ok, err := svc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session after checkpoint save: session=%+v ok=%v err=%v", after, ok, err)
	}
	if after.Title != before.Title || after.Metadata["sidebar_label"] != "Durable label" {
		t.Fatalf("checkpoint save replaced session metadata: before=%+v after=%+v", before, after)
	}
	var payload map[string]any
	if event == nil || json.Unmarshal(event.Payload, &payload) != nil {
		t.Fatalf("decode session.plan.saved payload: event=%+v", event)
	}
	if _, exists := payload["title"]; exists {
		t.Fatalf("session.plan.saved ambiguously exposes session title: %s", event.Payload)
	}
	if payload["plan_title"] != "Checkpoint title" || payload["plan_status"] != "approved" || payload["plan_approval_state"] != "approved" {
		t.Fatalf("session.plan.saved missing namespaced plan fields: %s", event.Payload)
	}
}

func TestPlanLifecycleAmendPlanReplacesFutureCheckpointsAndPreservesCompletedState(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "Done", Status: PlanCheckpointStatusCompleted, Report: "keep report", ChangedFiles: []string{"kept.go"}},
		{ID: "cp-2", Title: "Future two", Status: PlanCheckpointStatusPending},
		{ID: "cp-3", Title: "Future three", Status: PlanCheckpointStatusPending},
	})
	proposed := clonePlanLifecycleDocument(plan.Document)
	proposed.Info.Goal = "amended goal"
	proposed.Checkpoints = []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "Done", Status: PlanCheckpointStatusCompleted},
		{ID: "cp-2", Title: "Replacement two", Status: PlanCheckpointStatusPending, Report: "must reset"},
		{ID: "cp-4", Title: "Replacement four", Status: PlanCheckpointStatusPending},
	}

	result, err := NewPlanLifecycleService(svc).AmendPlan(PlanLifecycleAmendmentInput{
		SessionID:               sessionID,
		PlanID:                  plan.ID,
		Document:                proposed,
		BaseRevision:            plan.Version,
		UpdateSummary:           "Replace future checkpoints",
		ReplaceFromCheckpointID: "cp-2",
	})
	if err != nil {
		t.Fatalf("amend plan: %v", err)
	}
	if result.Plan.RevisionKind != PlanRevisionKindDefinition || result.Plan.UpdateKind != "plan_amendment" || result.Plan.Checkpoint {
		t.Fatalf("revision metadata = kind %q update %q checkpoint %v", result.Plan.RevisionKind, result.Plan.UpdateKind, result.Plan.Checkpoint)
	}
	if result.Plan.Version != plan.Version+1 || result.Plan.ParentRevision != plan.Version {
		t.Fatalf("revision linkage = v%d parent %d", result.Plan.Version, result.Plan.ParentRevision)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,cp-2,cp-4" {
		t.Fatalf("checkpoint order = %v", got)
	}
	kept := result.Plan.Document.Checkpoints[0]
	if kept.Report != "keep report" || len(kept.ChangedFiles) != 1 || kept.ChangedFiles[0] != "kept.go" {
		t.Fatalf("completed checkpoint state was not preserved: %#v", kept)
	}
	replaced := result.Plan.Document.Checkpoints[1]
	if replaced.Title != "Replacement two" || replaced.Report != "" || replaced.Status != PlanCheckpointStatusPending {
		t.Fatalf("future replacement not reset/preserved as pending: %#v", replaced)
	}
	if result.Plan.Document.ActiveCheckpointID != "cp-2" || result.Plan.Document.Info.Goal != "amended goal" {
		t.Fatalf("active/goal = %q/%q", result.Plan.Document.ActiveCheckpointID, result.Plan.Document.Info.Goal)
	}
}

func TestPlanLifecycleAmendPlanAppendsFutureToCompletedPlanAndSelectsIt(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{{
		ID: "cp-1", Title: "Done", Status: PlanCheckpointStatusCompleted, Tasks: []string{"done"}, AcceptanceCriteria: []string{"done"},
		Report: "preserve report", Result: "preserve result", AttemptID: "cp-1:attempt-1",
		Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-1:attempt-1", CheckpointID: "cp-1", Status: PlanCheckpointStatusCompleted, Report: "preserve report"}},
	}})
	current := clonePlanLifecycleDocument(plan.Document)
	current.Info.Goal = "preserve completed history and append future work"
	current.ActiveCheckpointID = "cp-1"
	current.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateWaitingReview, LastCheckpointID: "cp-1", LastAttemptID: "cp-1:attempt-1", LastOutcome: PlanCheckpointStatusCompleted}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, plan.ID, plan.Title, plan.Plan, plan.Status, plan.ApprovalState, true, PlanSaveMetadata{UpdateSummary: "final review", UpdateKind: "complete_checkpoint", RevisionKind: PlanRevisionKindExecution, Checkpoint: true, Document: current})
	if err != nil {
		t.Fatalf("save final-review plan: %v", err)
	}
	proposed := &pebblestore.SessionPlanDocument{
		Title:           plan.Title,
		Info:            pebblestore.SessionPlanInfo{Goal: "extend completed plan"},
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Untrusted copy", Status: PlanCheckpointStatusCompleted, Tasks: []string{"untrusted"}, AcceptanceCriteria: []string{"untrusted"}},
			{ID: "cp-2", Title: "New two", Status: PlanCheckpointStatusPending, Tasks: []string{"two"}, AcceptanceCriteria: []string{"two done"}},
			{ID: "cp-3", Title: "New three", Status: PlanCheckpointStatusPending, Tasks: []string{"three"}, AcceptanceCriteria: []string{"three done"}},
		},
	}

	result, err := NewPlanLifecycleService(svc).AmendPlan(PlanLifecycleAmendmentInput{
		SessionID: sessionID, PlanID: plan.ID, Document: proposed, BaseRevision: plan.Version,
		UpdateSummary: "append future work", AmendFutureCheckpoints: true,
	})
	if err != nil {
		t.Fatalf("append future checkpoints: %v", err)
	}
	if got := strings.Join(checkpointIDs(result.Plan.Document.Checkpoints), ","); got != "cp-1,cp-2,cp-3" {
		t.Fatalf("checkpoint order = %s", got)
	}
	kept := result.Plan.Document.Checkpoints[0]
	if kept.Report != "preserve report" || kept.Result != "preserve result" || kept.AttemptID != "cp-1:attempt-1" || len(kept.Attempts) != 1 {
		t.Fatalf("completed checkpoint runtime changed: %#v", kept)
	}
	for _, appended := range result.Plan.Document.Checkpoints[1:] {
		if appended.Status != PlanCheckpointStatusPending || appended.AttemptID != "" || appended.Report != "" || len(appended.Attempts) != 0 {
			t.Fatalf("appended checkpoint runtime not reset: %#v", appended)
		}
	}
	if result.Plan.Document.ActiveCheckpointID != "cp-2" || result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.Status != PlanExecutionStateIdle {
		t.Fatalf("appended future not selected: active=%q state=%#v", result.Plan.Document.ActiveCheckpointID, result.Plan.Document.ExecutionState)
	}
	if result.Summary.NextCheckpointID != "cp-2" || result.Summary.NextCheckpointStatus != PlanCheckpointStatusPending || !result.Summary.AutoAdvanceAllowed || result.Summary.ReviewRequired {
		t.Fatalf("append execution summary = %#v", result.Summary)
	}
}

func TestPlanLifecycleAmendPlanAppendRejectsDuplicateOrUnresolvedCurrentState(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Done", Status: PlanCheckpointStatusCompleted, Tasks: []string{"done"}, AcceptanceCriteria: []string{"done"}}})
	duplicate := &pebblestore.SessionPlanDocument{Title: plan.Title, Info: pebblestore.SessionPlanInfo{Goal: "duplicate"}, ExecutionPolicy: plan.Document.ExecutionPolicy, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Duplicate", Status: PlanCheckpointStatusCompleted, Tasks: []string{"duplicate"}, AcceptanceCriteria: []string{"rejected"}}}}
	_, err := NewPlanLifecycleService(svc).AmendPlan(PlanLifecycleAmendmentInput{SessionID: sessionID, PlanID: plan.ID, Document: duplicate, BaseRevision: plan.Version, UpdateSummary: "duplicate", AmendFutureCheckpoints: true})
	if err == nil || !strings.Contains(err.Error(), "at least one new checkpoint id") {
		t.Fatalf("duplicate append error = %v", err)
	}
	active, ok, getErr := svc.GetActivePlan(sessionID)
	if getErr != nil || !ok || active.Version != plan.Version || len(active.Document.Checkpoints) != 1 {
		t.Fatalf("invalid append mutated plan: ok=%v err=%v plan=%#v", ok, getErr, active)
	}
}

func TestPlanLifecycleAmendPlanRejectsStaleBaseRevision(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusPending}})
	proposed := clonePlanLifecycleDocument(plan.Document)

	_, err := NewPlanLifecycleService(svc).AmendPlan(PlanLifecycleAmendmentInput{
		SessionID:               sessionID,
		PlanID:                  plan.ID,
		Document:                proposed,
		BaseRevision:            plan.Version - 1,
		UpdateSummary:           "Stale amendment",
		ReplaceFromCheckpointID: "cp-1",
	})
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale amendment error = %v", err)
	}
}

func TestPlanLifecycleAmendPlanAllowsWaitingReviewFutureReplacement(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{
		{
			ID:           "cp-1",
			Title:        "Review me",
			Status:       PlanCheckpointStatusCompleted,
			Report:       "keep report",
			Result:       "keep result",
			ChangedFiles: []string{"kept.go"},
			Validation:   []string{"kept validation"},
			AttemptID:    "cp-1:attempt-1",
			RunID:        "run-cp-1",
			SessionID:    sessionID,
			StartedAt:    100,
			CompletedAt:  200,
			Review:       &pebblestore.SessionPlanCheckpointReview{Status: PlanCheckpointReviewStatusPending, Notes: "waiting for user"},
			Attempts: []pebblestore.SessionPlanCheckpointAttempt{{
				ID:              "cp-1:attempt-1",
				CheckpointID:    "cp-1",
				Status:          PlanCheckpointStatusCompleted,
				Outcome:         PlanCheckpointStatusCompleted,
				RunID:           "run-cp-1",
				SessionID:       sessionID,
				ParentSessionID: sessionID,
				StartedAt:       100,
				CompletedAt:     200,
				Report:          "keep report",
				Result:          "keep result",
				ChangedFiles:    []string{"kept.go"},
				Validation:      []string{"kept validation"},
			}},
		},
		{ID: "cp-2", Title: "Future", Status: PlanCheckpointStatusPending},
		{ID: "cp-3", Title: "Later", Status: PlanCheckpointStatusPending},
	})
	current := clonePlanLifecycleDocument(plan.Document)
	current.ActiveCheckpointID = "cp-1"
	current.ExecutionState = &pebblestore.SessionPlanExecutionState{
		Status:           PlanExecutionStateWaitingReview,
		LastCheckpointID: "cp-1",
		LastAttemptID:    "cp-1:attempt-1",
		LastOutcome:      PlanCheckpointStatusCompleted,
		ParentSessionID:  sessionID,
		CurrentSessionID: sessionID,
		CurrentRunID:     "run-cp-1",
		StartedAt:        100,
		UpdatedAt:        200,
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, plan.ID, plan.Title, plan.Plan, plan.Status, plan.ApprovalState, true, PlanSaveMetadata{UpdateSummary: "wait for review", UpdateKind: "complete_checkpoint", RevisionKind: PlanRevisionKindExecution, Checkpoint: true, Document: current})
	if err != nil {
		t.Fatalf("save waiting review plan: %v", err)
	}
	proposed := clonePlanLifecycleDocument(plan.Document)
	proposed.Checkpoints = []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-2", Title: "Replacement future", Status: PlanCheckpointStatusPending, Report: "must reset"},
		{ID: "cp-4", Title: "New future", Status: PlanCheckpointStatusPending},
	}

	result, err := NewPlanLifecycleService(svc).AmendPlan(PlanLifecycleAmendmentInput{
		SessionID:               sessionID,
		PlanID:                  plan.ID,
		Document:                proposed,
		BaseRevision:            plan.Version,
		UpdateSummary:           "Replace future while waiting for review",
		ReplaceFromCheckpointID: "cp-2",
	})
	if err != nil {
		t.Fatalf("amend waiting review future: %v", err)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,cp-2,cp-4" {
		t.Fatalf("checkpoint order = %v", got)
	}
	kept := result.Plan.Document.Checkpoints[0]
	if kept.Report != "keep report" || kept.Result != "keep result" || kept.AttemptID != "cp-1:attempt-1" || kept.RunID != "run-cp-1" || kept.CompletedAt != 200 {
		t.Fatalf("completed checkpoint runtime was not preserved: %#v", kept)
	}
	if kept.Review == nil || kept.Review.Status != PlanCheckpointReviewStatusPending || kept.Review.Notes != "waiting for user" {
		t.Fatalf("completed checkpoint review was not preserved: %#v", kept.Review)
	}
	if len(kept.Attempts) != 1 || kept.Attempts[0].ID != "cp-1:attempt-1" || kept.Attempts[0].Report != "keep report" {
		t.Fatalf("completed checkpoint attempts were not preserved: %#v", kept.Attempts)
	}
	if result.Plan.Document.ActiveCheckpointID != "cp-1" || result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.Status != PlanExecutionStateWaitingReview || result.Plan.Document.ExecutionState.LastCheckpointID != "cp-1" {
		t.Fatalf("waiting review execution state was not preserved: active=%q state=%#v", result.Plan.Document.ActiveCheckpointID, result.Plan.Document.ExecutionState)
	}
	replaced := result.Plan.Document.Checkpoints[1]
	if replaced.Title != "Replacement future" || replaced.Status != PlanCheckpointStatusPending || replaced.Report != "" {
		t.Fatalf("future replacement not reset/preserved as pending: %#v", replaced)
	}
}

func TestPlanLifecycleAmendPlanAllowsWaitingReviewOverrideStaleFutureReplacement(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{
		{
			ID:           "cp-1",
			Title:        "Review me",
			Status:       PlanCheckpointStatusCompleted,
			Report:       "keep report",
			Result:       "keep result",
			ChangedFiles: []string{"kept.go"},
			Validation:   []string{"kept validation"},
			AttemptID:    "cp-1:attempt-1",
			RunID:        "run-cp-1",
			SessionID:    sessionID,
			CompletedAt:  200,
			Review:       &pebblestore.SessionPlanCheckpointReview{Status: PlanCheckpointReviewStatusPending, Notes: "waiting for user"},
			Attempts: []pebblestore.SessionPlanCheckpointAttempt{{
				ID:              "cp-1:attempt-1",
				CheckpointID:    "cp-1",
				Status:          PlanCheckpointStatusCompleted,
				Outcome:         PlanCheckpointStatusCompleted,
				RunID:           "run-cp-1",
				SessionID:       sessionID,
				ParentSessionID: sessionID,
				CompletedAt:     200,
				Report:          "keep report",
				Result:          "keep result",
			}},
		},
		{ID: "cp-2", Title: "Future", Status: PlanCheckpointStatusPending},
		{ID: "cp-3", Title: "Later", Status: PlanCheckpointStatusPending},
	})
	current := clonePlanLifecycleDocument(plan.Document)
	current.ActiveCheckpointID = "cp-1"
	current.ExecutionState = &pebblestore.SessionPlanExecutionState{
		Status:           PlanExecutionStateWaitingReview,
		LastCheckpointID: "cp-1",
		LastAttemptID:    "cp-1:attempt-1",
		LastOutcome:      PlanCheckpointStatusCompleted,
		ParentSessionID:  sessionID,
		CurrentSessionID: sessionID,
		CurrentRunID:     "run-cp-1",
		UpdatedAt:        200,
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, plan.ID, plan.Title, plan.Plan, plan.Status, plan.ApprovalState, true, PlanSaveMetadata{UpdateSummary: "wait for review", UpdateKind: "complete_checkpoint", RevisionKind: PlanRevisionKindExecution, Checkpoint: true, Document: current})
	if err != nil {
		t.Fatalf("save waiting review plan: %v", err)
	}
	proposed := clonePlanLifecycleDocument(plan.Document)
	proposed.Checkpoints = []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-2", Title: "Override replacement", Status: PlanCheckpointStatusPending, Result: "must reset"},
		{ID: "cp-4", Title: "Override new future", Status: PlanCheckpointStatusPending},
	}

	result, err := NewPlanLifecycleService(svc).AmendPlan(PlanLifecycleAmendmentInput{
		SessionID:               sessionID,
		PlanID:                  plan.ID,
		Document:                proposed,
		BaseRevision:            plan.Version - 1,
		OverrideStale:           true,
		UpdateSummary:           "Override stale and replace future while waiting for review",
		ReplaceFromCheckpointID: "cp-2",
	})
	if err != nil {
		t.Fatalf("override stale waiting review amendment: %v", err)
	}
	if result.Plan.ParentRevision != plan.Version || result.Plan.Version != plan.Version+1 {
		t.Fatalf("revision linkage = v%d parent %d, want v%d parent %d", result.Plan.Version, result.Plan.ParentRevision, plan.Version+1, plan.Version)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,cp-2,cp-4" {
		t.Fatalf("checkpoint order = %v", got)
	}
	kept := result.Plan.Document.Checkpoints[0]
	if kept.Report != "keep report" || kept.Result != "keep result" || kept.RunID != "run-cp-1" || kept.Review == nil || kept.Review.Status != PlanCheckpointReviewStatusPending {
		t.Fatalf("waiting-review checkpoint state was not preserved: %#v", kept)
	}
	if result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.Status != PlanExecutionStateWaitingReview || result.Plan.Document.ExecutionState.LastCheckpointID != "cp-1" {
		t.Fatalf("waiting-review execution state was not preserved: %#v", result.Plan.Document.ExecutionState)
	}
	replaced := result.Plan.Document.Checkpoints[1]
	if replaced.Title != "Override replacement" || replaced.Status != PlanCheckpointStatusPending || replaced.Result != "" {
		t.Fatalf("future replacement was not reset: %#v", replaced)
	}
}

func TestPlanLifecycleAmendPlanPreservesCurrentInProgressCheckpoint(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "Current", Status: PlanCheckpointStatusInProgress, AttemptID: "cp-1:attempt-1", RunID: "run-current", SessionID: sessionID},
		{ID: "cp-2", Title: "Future", Status: PlanCheckpointStatusPending},
	})
	current := clonePlanLifecycleDocument(plan.Document)
	current.ActiveCheckpointID = "cp-1"
	current.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateInProgress, ActiveAttemptID: "cp-1:attempt-1", CurrentRunID: "run-current", CurrentSessionID: sessionID, ParentSessionID: sessionID}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, plan.ID, plan.Title, plan.Plan, plan.Status, plan.ApprovalState, true, PlanSaveMetadata{UpdateSummary: "start current", UpdateKind: "start_checkpoint", RevisionKind: PlanRevisionKindExecution, Checkpoint: true, Document: current})
	if err != nil {
		t.Fatalf("save in-progress plan: %v", err)
	}
	proposed := clonePlanLifecycleDocument(plan.Document)
	proposed.Checkpoints = []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "Current", Status: PlanCheckpointStatusInProgress},
		{ID: "cp-2", Title: "Replacement future", Status: PlanCheckpointStatusPending},
		{ID: "cp-3", Title: "New future", Status: PlanCheckpointStatusPending},
	}

	result, err := NewPlanLifecycleService(svc).AmendPlan(PlanLifecycleAmendmentInput{
		SessionID:               sessionID,
		PlanID:                  plan.ID,
		Document:                proposed,
		BaseRevision:            plan.Version,
		UpdateSummary:           "Replace future after current",
		ReplaceFromCheckpointID: "cp-2",
	})
	if err != nil {
		t.Fatalf("amend after current: %v", err)
	}
	currentCheckpoint := result.Plan.Document.Checkpoints[0]
	if result.Plan.Document.ExecutionState == nil || result.Plan.Document.ExecutionState.Status != PlanExecutionStateInProgress || result.Plan.Document.ExecutionState.CurrentRunID != "run-current" {
		t.Fatalf("current execution state was not preserved: %#v", result.Plan.Document.ExecutionState)
	}
	if result.Plan.Document.ActiveCheckpointID != "cp-1" || currentCheckpoint.Status != PlanCheckpointStatusInProgress || currentCheckpoint.RunID != "run-current" || currentCheckpoint.AttemptID != "cp-1:attempt-1" {
		t.Fatalf("current checkpoint was not preserved: active=%q checkpoint=%#v", result.Plan.Document.ActiveCheckpointID, currentCheckpoint)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,cp-2,cp-3" {
		t.Fatalf("checkpoint order = %v", got)
	}
}

func TestPlanLifecycleAmendPlanRejectsCompletedCheckpointReplacement(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	plan := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "Done", Status: PlanCheckpointStatusCompleted},
		{ID: "cp-2", Title: "Future", Status: PlanCheckpointStatusPending},
	})
	proposed := clonePlanLifecycleDocument(plan.Document)

	_, err := NewPlanLifecycleService(svc).AmendPlan(PlanLifecycleAmendmentInput{
		SessionID:               sessionID,
		PlanID:                  plan.ID,
		Document:                proposed,
		BaseRevision:            plan.Version,
		UpdateSummary:           "Bad replacement",
		ReplaceFromCheckpointID: "cp-1",
	})
	if err == nil || !strings.Contains(err.Error(), "protected status") {
		t.Fatalf("completed replacement error = %v", err)
	}
}

func TestPlanLifecycleRequestNewPlanReplacementApprovesActivePlanAndAllowsFollowup(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	original := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-old", Title: "Old", Status: PlanCheckpointStatusCompleted}})
	replacementDoc := &pebblestore.SessionPlanDocument{
		ID:    "wrong-id-should-be-replaced",
		Title: "Replacement Plan",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  PlanExecutionPolicyModeReviewEachCheckpoint,
			Shape: PlanExecutionShapeCheckpointed,
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{Status: "running", ActiveAttemptID: "stale-attempt"},
		Checkpoints:    []pebblestore.SessionPlanCheckpoint{{ID: "cp-new", Title: "New", Status: PlanCheckpointStatusInProgress, AttemptID: "stale-attempt"}},
	}

	result, err := NewPlanLifecycleService(svc).RequestNewPlan(PlanLifecycleProposalInput{
		SessionID:         sessionID,
		PlanID:            original.ID,
		Title:             "Replacement Plan",
		Plan:              "# Replacement Plan",
		Document:          replacementDoc,
		Reason:            "Replace current plan",
		ApprovalConfirmed: true,
	})
	if err != nil {
		t.Fatalf("request replacement plan: %v", err)
	}
	if result.Plan.ID != original.ID || !result.Plan.Active || result.Plan.Status != "approved" || result.Plan.ApprovalState != "approved" {
		t.Fatalf("replacement plan state = id %q active %v status %q approval %q", result.Plan.ID, result.Plan.Active, result.Plan.Status, result.Plan.ApprovalState)
	}
	if result.Plan.Document == nil || result.Plan.Document.ID != original.ID || result.Plan.Document.Status != "approved" || result.Plan.Document.ActiveCheckpointID != "cp-new" {
		t.Fatalf("replacement document = %#v", result.Plan.Document)
	}
	if result.Plan.Document.ExecutionPolicy.Mode != PlanExecutionPolicyModeAutomatic || result.Plan.Document.ExecutionPolicy.Shape != PlanExecutionShapeCheckpointed || result.Plan.Document.ExecutionState != nil {
		t.Fatalf("replacement approval policy/state = %#v", result.Plan.Document)
	}
	if len(result.Plan.Document.Checkpoints) != 1 || result.Plan.Document.Checkpoints[0].Status != PlanCheckpointStatusPending || result.Plan.Document.Checkpoints[0].AttemptID != "" {
		t.Fatalf("replacement checkpoint runtime was not reset: %#v", result.Plan.Document.Checkpoints)
	}
	active, ok, err := svc.GetActivePlan(sessionID)
	if err != nil || !ok {
		t.Fatalf("get active: ok=%v err=%v", ok, err)
	}
	if active.ID != original.ID || active.Title != "Replacement Plan" || active.ApprovalState != "approved" {
		t.Fatalf("active replacement = %#v", active)
	}

	followup, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:         sessionID,
		PlanID:            original.ID,
		ChangeRequest:     "Add a follow-up after replacement.",
		ApprovalConfirmed: true,
	})
	if err != nil {
		t.Fatalf("request follow-up after replacement: %v", err)
	}
	if got := checkpointIDs(followup.Plan.Document.Checkpoints); strings.Join(got, ",") != "followup-1,cp-new" {
		t.Fatalf("follow-up checkpoint order = %v", got)
	}
}

func TestPlanLifecycleRequestNewPlanReplacementPersistsExplicitManualCheckpointedControls(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	original := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-old", Title: "Old", Status: PlanCheckpointStatusCompleted}})
	manual := false

	result, err := NewPlanLifecycleService(svc).RequestNewPlan(PlanLifecycleProposalInput{
		SessionID:             sessionID,
		PlanID:                original.ID,
		Title:                 "Replacement Plan",
		Plan:                  "# Replacement Plan",
		Document:              &pebblestore.SessionPlanDocument{Title: "Replacement Plan", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-new", Title: "New", Status: PlanCheckpointStatusPending}}},
		Reason:                "Replace current plan",
		ApprovalConfirmed:     true,
		ExecutionGranularity:  PlanAcceptanceGranularityCheckpointed,
		ContinueAutomatically: &manual,
	})
	if err != nil {
		t.Fatalf("request replacement plan: %v", err)
	}
	if result.Plan.Document == nil || result.Plan.Document.ExecutionPolicy.Mode != PlanExecutionPolicyModeReviewEachCheckpoint || result.Plan.Document.ExecutionPolicy.Shape != PlanExecutionShapeCheckpointed {
		t.Fatalf("replacement manual checkpointed policy = %#v", result.Plan.Document)
	}
}

func TestPlanLifecycleRequestNewPlanWithoutPlanIDKeepsSeparateProposalInactive(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	original := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-old", Title: "Old", Status: PlanCheckpointStatusCompleted}})

	proposal, err := NewPlanLifecycleService(svc).RequestNewPlan(PlanLifecycleProposalInput{
		SessionID: sessionID,
		Title:     "Separate Proposal",
		Plan:      "# Separate Proposal",
		Document: &pebblestore.SessionPlanDocument{
			Title:       "Separate Proposal",
			Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-proposed", Title: "Proposed", Status: PlanCheckpointStatusPending}},
		},
	})
	if err != nil {
		t.Fatalf("request separate new plan: %v", err)
	}
	if proposal.Plan.ID == original.ID || proposal.Plan.Active || proposal.Plan.Status != "pending_approval" || proposal.Plan.ApprovalState != "pending" {
		t.Fatalf("separate proposal state = %#v", proposal.Plan)
	}
	active, ok, err := svc.GetActivePlan(sessionID)
	if err != nil || !ok {
		t.Fatalf("get active: ok=%v err=%v", ok, err)
	}
	if active.ID != original.ID || active.Title != original.Title || active.ApprovalState != "approved" {
		t.Fatalf("active plan should remain original, got %#v", active)
	}
}

func TestPlanLifecycleRequestNewPlanWithoutPlanIDApprovalActivatesApprovedPlan(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	original := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeReviewEachCheckpoint,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-old", Title: "Old", Status: PlanCheckpointStatusCompleted}})
	approved, err := NewPlanLifecycleService(svc).RequestNewPlan(PlanLifecycleProposalInput{
		SessionID:         sessionID,
		Title:             "Approved Separate Plan",
		Plan:              "# Approved Separate Plan",
		ApprovalConfirmed: true,
		Document: &pebblestore.SessionPlanDocument{
			Title:          "Approved Separate Plan",
			ExecutionState: &pebblestore.SessionPlanExecutionState{Status: "running", ActiveAttemptID: "stale-attempt"},
			Checkpoints:    []pebblestore.SessionPlanCheckpoint{{ID: "cp-proposed", Title: "Proposed", Status: PlanCheckpointStatusInProgress, AttemptID: "stale-attempt"}},
		},
	})
	if err != nil {
		t.Fatalf("approve separate new plan: %v", err)
	}
	if approved.Plan.ID == original.ID || !approved.Plan.Active || approved.Plan.Status != "approved" || approved.Plan.ApprovalState != "approved" {
		t.Fatalf("approved separate plan state = %#v", approved.Plan)
	}
	if approved.Summary.NextCheckpointID != "cp-proposed" || approved.Summary.PolicyMode != PlanExecutionPolicyModeAutomatic || approved.Summary.ExecutionShape != PlanExecutionShapeCheckpointed {
		t.Fatalf("approved separate execution summary = %#v", approved.Summary)
	}
	if approved.Plan.Document == nil || approved.Plan.Document.ID != approved.Plan.ID || approved.Plan.Document.Status != "approved" || approved.Plan.Document.ActiveCheckpointID != "cp-proposed" {
		t.Fatalf("approved separate document = %#v", approved.Plan.Document)
	}
	if approved.Plan.Document.ExecutionState != nil || approved.Plan.Document.Checkpoints[0].Status != PlanCheckpointStatusPending || approved.Plan.Document.Checkpoints[0].AttemptID != "" {
		t.Fatalf("approved separate runtime was not reset: %#v", approved.Plan.Document)
	}
	active, ok, err := svc.GetActivePlan(sessionID)
	if err != nil || !ok {
		t.Fatalf("get active: ok=%v err=%v", ok, err)
	}
	if active.ID != approved.Plan.ID || active.ID == original.ID || active.ApprovalState != "approved" {
		t.Fatalf("active approved separate plan = %#v", active)
	}
}

func TestPlanLifecycleRequestNewPlanApprovalRequiresStructuredDocument(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	_, err := NewPlanLifecycleService(svc).RequestNewPlan(PlanLifecycleProposalInput{
		SessionID:         sessionID,
		Title:             "Missing Document",
		Plan:              "# Missing Document",
		ApprovalConfirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "structured document") {
		t.Fatalf("approval without document error = %v", err)
	}
}

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

func TestPlanLifecycleStartSessionCheckpointCreatesApprovedStartedPlan(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	request := "fix my sidebar and make the active item stay visible"

	result, err := NewPlanLifecycleService(svc).StartSessionCheckpoint(PlanLifecycleSessionCheckpointInput{
		SessionID:          sessionID,
		ChangeRequest:      request,
		Title:              "Fix sidebar visibility",
		Tasks:              []string{"Inspect sidebar state", "Keep the active item visible"},
		AcceptanceCriteria: []string{"Active item remains visible after navigation"},
		Notes:              "Relevant files: web/src; validation: targeted UI check.",
		RunID:              "run-session-checkpoint-1",
		RunSessionID:       sessionID,
		ParentSessionID:    sessionID,
		StartedAt:          1234,
	})
	if err != nil {
		t.Fatalf("start session checkpoint: %v", err)
	}
	if result.Plan.ID == "" || !result.Plan.Active || result.Plan.Status != "approved" || result.Plan.ApprovalState != "approved" {
		t.Fatalf("saved plan metadata = %#v", result.Plan)
	}
	if result.CheckpointID != "cp-1" || result.AttemptID != "cp-1:attempt-1" {
		t.Fatalf("checkpoint/attempt = %q/%q", result.CheckpointID, result.AttemptID)
	}
	doc := result.Plan.Document
	if doc == nil || doc.Status != "approved" || doc.ExecutionPolicy.Mode != PlanExecutionPolicyModeAutomatic || doc.ExecutionPolicy.Shape != PlanExecutionShapeCheckpointed {
		t.Fatalf("document/policy = %#v", doc)
	}
	if doc.ActiveCheckpointID != "cp-1" || doc.ExecutionState == nil || doc.ExecutionState.Status != PlanExecutionStateInProgress || doc.ExecutionState.CurrentRunID != "run-session-checkpoint-1" {
		t.Fatalf("execution state = active %q state %#v", doc.ActiveCheckpointID, doc.ExecutionState)
	}
	if len(doc.Checkpoints) != 1 {
		t.Fatalf("checkpoint count = %d", len(doc.Checkpoints))
	}
	checkpoint := doc.Checkpoints[0]
	if checkpoint.ID != "cp-1" || checkpoint.Status != PlanCheckpointStatusInProgress || checkpoint.Objective != request || checkpoint.RunID != "run-session-checkpoint-1" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	if len(checkpoint.Tasks) != 2 || len(checkpoint.AcceptanceCriteria) != 1 {
		t.Fatalf("handoff fields not preserved: %#v", checkpoint)
	}
	for _, want := range []string{"Current user request / change_request:", request, "Handoff notes:", "Relevant files:"} {
		if !strings.Contains(checkpoint.Notes, want) {
			t.Fatalf("checkpoint notes missing %q: %s", want, checkpoint.Notes)
		}
	}
}

func TestPlanLifecycleFollowupCheckpointDeduplicatesSourceMessage(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)
	_, _, err := svc.SavePlanWithMetadata(sessionID, "plan-followup-dedup", "Followup", "# Followup", "approved", "approved", true, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed, FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyAutoStart},
		Checkpoints:     []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: PlanCheckpointStatusCompleted}},
	}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	lifecycle := NewPlanLifecycleService(svc)
	first, err := lifecycle.RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: "plan-followup-dedup", ChangeRequest: "add audit", SourceMessageID: "message-1"})
	if err != nil {
		t.Fatalf("first follow-up: %v", err)
	}
	second, err := lifecycle.RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: "plan-followup-dedup", ChangeRequest: "add audit", SourceMessageID: "message-1"})
	if err != nil {
		t.Fatalf("second follow-up: %v", err)
	}
	if first.CheckpointID == "" || second.CheckpointID != first.CheckpointID {
		t.Fatalf("dedup checkpoint ids = %q/%q", first.CheckpointID, second.CheckpointID)
	}
	plan, ok, err := svc.GetPlan(sessionID, "plan-followup-dedup")
	if err != nil || !ok {
		t.Fatalf("get plan: ok=%v err=%v", ok, err)
	}
	if len(plan.Document.Checkpoints) != 2 {
		t.Fatalf("checkpoint count = %d, want 2: %#v", len(plan.Document.Checkpoints), plan.Document.Checkpoints)
	}
}

func TestPlanLifecycleFollowupCheckpointDeduplicatesConcurrentSourceMessage(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)
	_, _, err := svc.SavePlanWithMetadata(sessionID, "plan-followup-race", "Followup", "# Followup", "approved", "approved", true, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed, FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyAutoStart},
		Checkpoints:     []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: PlanCheckpointStatusCompleted}},
	}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	lifecycle := NewPlanLifecycleService(svc)
	start := make(chan struct{})
	results := make(chan PlanLifecycleResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, callErr := lifecycle.RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: "plan-followup-race", ChangeRequest: "add audit", SourceMessageID: "message-race"})
			results <- result
			errs <- callErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("concurrent follow-up: %v", callErr)
		}
	}
	checkpointID := ""
	for result := range results {
		if checkpointID == "" {
			checkpointID = result.CheckpointID
		} else if result.CheckpointID != checkpointID {
			t.Fatalf("concurrent dedupe checkpoint ids = %q/%q", checkpointID, result.CheckpointID)
		}
	}
	plan, ok, err := svc.GetPlan(sessionID, "plan-followup-race")
	if err != nil || !ok || len(plan.Document.Checkpoints) != 2 {
		t.Fatalf("concurrent dedupe plan: ok=%v err=%v checkpoints=%#v", ok, err, plan.Document.Checkpoints)
	}
}

func TestPlanLifecycleTransitionsSerializeAcrossFollowupAndAmendment(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	sessionID := createPlanTestSession(t, svc)
	initial, _, err := svc.SavePlanWithMetadata(sessionID, "plan-lifecycle-serialization", "Serialized", "# Serialized", "approved", "approved", true, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints:     []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusCompleted}},
	}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}

	lifecycle := NewPlanLifecycleService(svc)
	resolverEntered := make(chan struct{})
	releaseResolver := make(chan struct{})
	lifecycle.SetGlobalFollowupCheckpointPolicyResolver(func(string) (string, error) {
		close(resolverEntered)
		<-releaseResolver
		return PlanFollowupCheckpointPolicyAutoStart, nil
	})
	followupErr := make(chan error, 1)
	go func() {
		_, callErr := lifecycle.RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: initial.ID, ChangeRequest: "add follow-up", ApprovalConfirmed: true})
		followupErr <- callErr
	}()
	<-resolverEntered

	proposed := clonePlanLifecycleDocument(initial.Document)
	proposed.Checkpoints = append(proposed.Checkpoints, pebblestore.SessionPlanCheckpoint{ID: "cp-amended", Title: "Amended", Status: PlanCheckpointStatusPending})
	amendErr := make(chan error, 1)
	go func() {
		_, callErr := lifecycle.AmendPlan(PlanLifecycleAmendmentInput{SessionID: sessionID, PlanID: initial.ID, Document: proposed, BaseRevision: initial.Version, UpdateSummary: "append amended checkpoint", AmendFutureCheckpoints: true})
		amendErr <- callErr
	}()

	close(releaseResolver)
	if err := <-followupErr; err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	if err := <-amendErr; err == nil || !strings.Contains(err.Error(), "base_revision") || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("amendment error = %v, want stale base revision after serialized follow-up", err)
	}
	plan, ok, err := svc.GetPlan(sessionID, initial.ID)
	if err != nil || !ok {
		t.Fatalf("get plan: ok=%v err=%v", ok, err)
	}
	if len(plan.Document.Checkpoints) != 2 || plan.Document.Checkpoints[1].ID == "cp-amended" {
		t.Fatalf("stale amendment overwrote serialized follow-up: %#v", plan.Document.Checkpoints)
	}
}

func TestPlanLifecycleStartSessionCheckpointRejectsActivePlan(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed}, []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "One", Status: PlanCheckpointStatusPending}})

	_, err := NewPlanLifecycleService(svc).StartSessionCheckpoint(PlanLifecycleSessionCheckpointInput{SessionID: sessionID, ChangeRequest: "do another task"})
	if err == nil || !strings.Contains(err.Error(), "requires no active plan") {
		t.Fatalf("error = %v, want active-plan refusal", err)
	}
}

func TestPlanLifecycleStartSessionCheckpointQueuesFreshRunWithoutRunID(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}

	result, err := NewPlanLifecycleService(svc).StartSessionCheckpoint(PlanLifecycleSessionCheckpointInput{SessionID: sessionID, ChangeRequest: "fix the sidebar", Title: "Fix sidebar"})
	if err != nil {
		t.Fatalf("start session checkpoint without run id: %v", err)
	}
	if result.CheckpointID != "cp-1" || result.AttemptID != "" {
		t.Fatalf("checkpoint/attempt = %q/%q", result.CheckpointID, result.AttemptID)
	}
	if result.Summary.NextCheckpointID != "cp-1" || result.Summary.NextCheckpointStatus != PlanCheckpointStatusPending || !result.Summary.AutoAdvanceAllowed {
		t.Fatalf("summary = %#v", result.Summary)
	}
	checkpoint := result.Plan.Document.Checkpoints[0]
	if checkpoint.Status != PlanCheckpointStatusPending || checkpoint.RunID != "" || checkpoint.AttemptID != "" {
		t.Fatalf("queued checkpoint = %#v", checkpoint)
	}
}

func TestPlanLifecycleRequestFollowupCheckpointBuildsSelfContainedHandoff(t *testing.T) {
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
	request := "ok then that's the next step. please order it. the idea is this. i am noticing the ai is adding in checkpoints that are losing the original request"

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID:          sessionID,
		PlanID:             plan.ID,
		ChangeRequest:      request,
		Title:              "Design session-checkpoint handoff payload requirements",
		Tasks:              []string{"Define full request preservation", "Specify handoff fields"},
		AcceptanceCriteria: []string{"The checkpoint preserves material parts of the original request", "The checkpoint reads as a self-contained handoff"},
		Notes:              "Relevant files: swarmd/internal/run/service_prompt.go; validation: targeted tests only.",
		ApprovalConfirmed:  true,
	})
	if err != nil {
		t.Fatalf("request follow-up handoff: %v", err)
	}
	if result.Plan.Document == nil || len(result.Plan.Document.Checkpoints) != 2 {
		t.Fatalf("document checkpoints = %#v", result.Plan.Document)
	}
	checkpoint := result.Plan.Document.Checkpoints[1]
	if checkpoint.Objective != request {
		t.Fatalf("objective = %q, want current request", checkpoint.Objective)
	}
	if checkpoint.Title != "Design session-checkpoint handoff payload requirements" || len(checkpoint.Tasks) != 2 || len(checkpoint.AcceptanceCriteria) != 2 {
		t.Fatalf("handoff fields not preserved: %#v", checkpoint)
	}
	for _, want := range []string{"Current user request / change_request:", request, "Handoff notes:", "Relevant files:"} {
		if !strings.Contains(checkpoint.Notes, want) {
			t.Fatalf("checkpoint notes missing %q: %s", want, checkpoint.Notes)
		}
	}
	if strings.Contains(checkpoint.Notes, "Original user request/context:") {
		t.Fatalf("follow-up checkpoint retained a competing original request: %s", checkpoint.Notes)
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

func TestPlanLifecycleRequestFollowupCheckpointResolvesBlockedAndAutoStartsInsertedCheckpoint(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	blockedAttempt := pebblestore.SessionPlanCheckpointAttempt{
		ID: "cp-1:attempt-1", CheckpointID: "cp-1", Status: PlanCheckpointStatusBlocked,
		Outcome: PlanCheckpointStatusBlocked, RunID: "run-blocked", SessionID: "child-blocked",
		ParentSessionID: sessionID, StartedAt: 100, CompletedAt: 200, Report: "dependency missing", Result: "blocked",
	}
	doc := &pebblestore.SessionPlanDocument{
		ID: "plan-blocked-followup", Title: "Blocked Follow-up Plan", Status: "approved",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed,
			FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyAutoStart,
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{
			Status: PlanExecutionStateBlocked, LastCheckpointID: "cp-1", LastAttemptID: blockedAttempt.ID,
			LastOutcome: PlanCheckpointStatusBlocked, ParentSessionID: sessionID,
		},
		ActiveCheckpointID: "cp-1",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Blocked", Status: PlanCheckpointStatusBlocked, AttemptID: blockedAttempt.ID, RunID: blockedAttempt.RunID, SessionID: blockedAttempt.SessionID, StartedAt: 100, CompletedAt: 200, Report: blockedAttempt.Report, Result: blockedAttempt.Result, Attempts: []pebblestore.SessionPlanCheckpointAttempt{blockedAttempt}},
			{ID: "cp-2", Title: "Later", Status: PlanCheckpointStatusPending},
		},
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, doc.ID, doc.Title, "# Blocked Follow-up Plan", "approved", "approved", true, PlanSaveMetadata{Document: doc})
	if err != nil {
		t.Fatalf("save blocked plan: %v", err)
	}

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "Continue with replacement work.",
		RunID: "run-followup", RunSessionID: "child-followup", ParentSessionID: sessionID, StartedAt: 300,
	})
	if err != nil {
		t.Fatalf("request follow-up for blocked plan: %v", err)
	}
	if got := strings.Join(checkpointIDs(result.Plan.Document.Checkpoints), ","); got != "cp-1,followup-1,cp-2" {
		t.Fatalf("checkpoint order = %s", got)
	}
	resolved := result.Plan.Document.Checkpoints[0]
	if resolved.Status != PlanCheckpointStatusCompleted || resolved.Result != "superseded_by_followup" || resolved.Review == nil || resolved.Review.Status != PlanCheckpointReviewStatusApproved || resolved.Review.Result != "superseded_by_followup" {
		t.Fatalf("resolved blocked checkpoint = %#v", resolved)
	}
	if len(resolved.Attempts) != 1 || resolved.Attempts[0].Status != PlanCheckpointStatusCompleted || resolved.Attempts[0].Outcome != PlanCheckpointStatusCompleted || resolved.Attempts[0].Result != "superseded_by_followup" || resolved.Attempts[0].CompletedAt == 0 {
		t.Fatalf("resolved blocked attempt = %#v", resolved.Attempts)
	}
	followup := result.Plan.Document.Checkpoints[1]
	if result.CheckpointID != "followup-1" || result.AttemptID != "followup-1:attempt-1" || result.Plan.Document.ActiveCheckpointID != "followup-1" || followup.Status != PlanCheckpointStatusInProgress || followup.RunID != "run-followup" {
		t.Fatalf("follow-up execution = result %#v checkpoint %#v", result, followup)
	}
	if result.Summary.Blocked || result.Summary.Failed || result.Summary.NextCheckpointID != "followup-1" {
		t.Fatalf("execution summary = %#v", result.Summary)
	}
	if state := result.Plan.Document.ExecutionState; state == nil || state.Status != PlanExecutionStateInProgress || state.LastCheckpointID != "cp-1" || state.LastAttemptID != blockedAttempt.ID || state.LastOutcome != PlanCheckpointStatusCompleted {
		t.Fatalf("execution state = %#v", state)
	}
	assertCheckpointOrdersNormalized(t, result.Plan.Document)
}

func TestValidatePlanDocumentForFollowupAllowsPausedCheckpointBeforeActive(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ID: "plan-paused-before-active", Title: "Paused Before Active",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed,
		},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStatePaused, LastCheckpointID: "followup-5", LastOutcome: PlanCheckpointStatusPaused},
		ActiveCheckpointID: "followup-6",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "followup-5", Title: "Paused", Status: PlanCheckpointStatusPaused},
			{ID: "followup-6", Title: "Selected", Status: PlanCheckpointStatusPending},
		},
	}

	if err := ValidatePlanDocument(doc); err == nil || !strings.Contains(err.Error(), `checkpoint "followup-5" status "paused" is before active_checkpoint_id "followup-6"`) {
		t.Fatalf("ordinary validation error = %v", err)
	}
	if err := validatePlanDocumentForLifecycleAction(doc, "request_followup_checkpoint"); err != nil {
		t.Fatalf("follow-up repair validation: %v", err)
	}
	if err := validatePlanDocumentForLifecycleAction(doc, "start_checkpoint"); err == nil {
		t.Fatal("non-follow-up lifecycle action accepted discontinuous document")
	}
	if doc.Checkpoints[0].Status != PlanCheckpointStatusPaused {
		t.Fatalf("validation mutated source checkpoint = %#v", doc.Checkpoints[0])
	}
}

func TestPlanLifecycleRequestFollowupCheckpointSupersedesPausedAndAutoStartsInsertedCheckpoint(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	pausedAttempt := pebblestore.SessionPlanCheckpointAttempt{
		ID: "followup-4:attempt-1", CheckpointID: "followup-4", Status: PlanCheckpointStatusPaused,
		Outcome: PlanCheckpointStatusPaused, RunID: "run-paused", SessionID: "child-paused",
		ParentSessionID: sessionID, StartedAt: 100, CompletedAt: 200, Report: "user stopped the run", Result: "run_paused",
	}
	doc := &pebblestore.SessionPlanDocument{
		ID: "plan-paused-followup", Title: "Paused Follow-up Plan", Status: "approved",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed,
			FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyAutoStart,
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{
			Status: PlanExecutionStatePaused, LastCheckpointID: "followup-4", LastAttemptID: pausedAttempt.ID,
			LastOutcome: PlanCheckpointStatusPaused, ParentSessionID: sessionID,
		},
		ActiveCheckpointID: "followup-4",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Done", Status: PlanCheckpointStatusCompleted},
			{ID: "followup-4", Title: "Paused", Status: PlanCheckpointStatusPaused, AttemptID: pausedAttempt.ID, RunID: pausedAttempt.RunID, SessionID: pausedAttempt.SessionID, StartedAt: 100, CompletedAt: 200, Report: pausedAttempt.Report, Result: pausedAttempt.Result, Attempts: []pebblestore.SessionPlanCheckpointAttempt{pausedAttempt}},
			{ID: "followup-5", Title: "Later", Status: PlanCheckpointStatusPending},
		},
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, doc.ID, doc.Title, "# Paused Follow-up Plan", "approved", "approved", true, PlanSaveMetadata{Document: doc})
	if err != nil {
		t.Fatalf("save paused plan: %v", err)
	}

	result, err := NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{
		SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "Supersede the paused work.",
		RunID: "run-followup", RunSessionID: "child-followup", ParentSessionID: sessionID, StartedAt: 300,
	})
	if err != nil {
		t.Fatalf("request follow-up for paused plan: %v", err)
	}
	if got := strings.Join(checkpointIDs(result.Plan.Document.Checkpoints), ","); got != "cp-1,followup-4,followup-4.5,followup-5" {
		t.Fatalf("checkpoint order = %s", got)
	}
	resolved := result.Plan.Document.Checkpoints[1]
	if resolved.Status != PlanCheckpointStatusCompleted || resolved.Result != "superseded_by_followup" || resolved.Review == nil || resolved.Review.Status != PlanCheckpointReviewStatusApproved {
		t.Fatalf("resolved paused checkpoint = %#v", resolved)
	}
	if len(resolved.Attempts) != 1 || resolved.Attempts[0].ID != pausedAttempt.ID || resolved.Attempts[0].Status != PlanCheckpointStatusPaused || resolved.Attempts[0].Outcome != PlanCheckpointStatusPaused || resolved.Attempts[0].Result != "run_paused" || resolved.Attempts[0].CompletedAt != 200 {
		t.Fatalf("paused attempt changed: %#v", resolved.Attempts)
	}
	followup := result.Plan.Document.Checkpoints[2]
	if result.CheckpointID != "followup-4.5" || result.AttemptID != "followup-4.5:attempt-1" || result.Plan.Document.ActiveCheckpointID != "followup-4.5" || followup.Status != PlanCheckpointStatusInProgress || followup.RunID != "run-followup" {
		t.Fatalf("follow-up execution = result %#v checkpoint %#v", result, followup)
	}
	if state := result.Plan.Document.ExecutionState; state == nil || state.Status != PlanExecutionStateInProgress || state.LastCheckpointID != "followup-4" || state.LastAttemptID != pausedAttempt.ID || state.LastOutcome != PlanCheckpointStatusCompleted {
		t.Fatalf("execution state = %#v", state)
	}
	assertCheckpointOrdersNormalized(t, result.Plan.Document)
}

func TestPlanLifecycleRestartPausedCheckpointCreatesFreshAttempt(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	pausedAttempt := pebblestore.SessionPlanCheckpointAttempt{
		ID: "cp-1:attempt-1", CheckpointID: "cp-1", Status: PlanCheckpointStatusPaused,
		Outcome: PlanCheckpointStatusPaused, RunID: "run-paused", SessionID: "child-paused",
		ParentSessionID: sessionID, StartedAt: 100, CompletedAt: 200, Result: "run_paused",
	}
	doc := &pebblestore.SessionPlanDocument{
		ID: "plan-restart-paused", Title: "Restart Paused Plan", Status: "approved",
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStatePaused, LastCheckpointID: "cp-1", LastAttemptID: pausedAttempt.ID, LastOutcome: PlanCheckpointStatusPaused},
		ActiveCheckpointID: "cp-1",
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Paused", Status: PlanCheckpointStatusPaused, AttemptID: pausedAttempt.ID, RunID: pausedAttempt.RunID, SessionID: pausedAttempt.SessionID, StartedAt: 100, CompletedAt: 200, Result: pausedAttempt.Result, Attempts: []pebblestore.SessionPlanCheckpointAttempt{pausedAttempt}}},
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, doc.ID, doc.Title, "# Restart Paused Plan", "approved", "approved", true, PlanSaveMetadata{Document: doc})
	if err != nil {
		t.Fatalf("save paused plan: %v", err)
	}

	result, err := NewPlanLifecycleService(svc).RestartCheckpointRun(PlanLifecycleExecutionInput{
		SessionID: sessionID, PlanID: plan.ID, CheckpointID: "cp-1", RunID: "run-restarted",
		RunSessionID: "child-restarted", ParentSessionID: sessionID, StartedAt: 300,
	})
	if err != nil {
		t.Fatalf("restart paused checkpoint: %v", err)
	}
	checkpoint := result.Plan.Document.Checkpoints[0]
	if result.CheckpointID != "cp-1" || result.AttemptID != "cp-1:attempt-2" || checkpoint.Status != PlanCheckpointStatusInProgress || checkpoint.RunID != "run-restarted" || len(checkpoint.Attempts) != 2 {
		t.Fatalf("restarted checkpoint = result %#v checkpoint %#v", result, checkpoint)
	}
	if checkpoint.Attempts[0].Status != PlanCheckpointStatusPaused || checkpoint.Attempts[0].Outcome != PlanCheckpointStatusPaused || checkpoint.Attempts[0].Result != "run_paused" || checkpoint.Attempts[1].Status != PlanCheckpointStatusInProgress {
		t.Fatalf("restart attempt history = %#v", checkpoint.Attempts)
	}
	if state := result.Plan.Document.ExecutionState; state == nil || state.Status != PlanExecutionStateInProgress || state.ActiveAttemptID != "cp-1:attempt-2" || state.CurrentRunID != "run-restarted" {
		t.Fatalf("restart execution state = %#v", state)
	}
}

func TestPlanLifecycleRequestFollowupCheckpointDoesNotClearFailedCheckpoint(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	doc := &pebblestore.SessionPlanDocument{
		ID: "plan-failed-followup", Title: "Failed Follow-up Plan", Status: "approved",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed,
			FollowupCheckpointPolicy: PlanFollowupCheckpointPolicyAutoStart,
		},
		ActiveCheckpointID: "cp-1",
		ExecutionState:     &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateFailed, LastCheckpointID: "cp-1", LastOutcome: PlanCheckpointStatusFailed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Failed", Status: PlanCheckpointStatusFailed, Result: "failed"},
			{ID: "cp-2", Title: "Later", Status: PlanCheckpointStatusPending},
		},
	}
	plan, _, err := svc.SavePlanWithMetadata(sessionID, doc.ID, doc.Title, "# Failed Follow-up Plan", "approved", "approved", true, PlanSaveMetadata{Document: doc})
	if err != nil {
		t.Fatalf("save failed plan: %v", err)
	}

	_, err = NewPlanLifecycleService(svc).RequestFollowupCheckpoint(PlanLifecycleFollowupCheckpointInput{SessionID: sessionID, PlanID: plan.ID, ChangeRequest: "Do not silently recover failure."})
	if err == nil || !strings.Contains(err.Error(), "cannot continue a failed plan") {
		t.Fatalf("error = %v, want explicit failed-plan rejection", err)
	}
	current, ok, getErr := svc.GetPlan(sessionID, plan.ID)
	if getErr != nil || !ok {
		t.Fatalf("get unchanged failed plan: ok=%v err=%v", ok, getErr)
	}
	if got := strings.Join(checkpointIDs(current.Document.Checkpoints), ","); got != "cp-1,cp-2" || current.Document.Checkpoints[0].Status != PlanCheckpointStatusFailed {
		t.Fatalf("failed plan was mutated: %#v", current.Document)
	}
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

func TestPlanLifecycleRestorePlanRevisionCreatesCurrentRevisionWithMetadata(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	original := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "Original one", Status: PlanCheckpointStatusPending},
		{ID: "cp-2", Title: "Original two", Status: PlanCheckpointStatusPending},
	})
	changedDoc := clonePlanLifecycleDocument(original.Document)
	changedDoc.Checkpoints = []pebblestore.SessionPlanCheckpoint{{ID: "changed", Title: "Changed checkpoint", Status: PlanCheckpointStatusPending}}
	changedDoc.ActiveCheckpointID = "changed"
	changed, _, err := svc.SavePlanWithMetadata(sessionID, original.ID, original.Title, original.Plan, "approved", "approved", true, PlanSaveMetadata{UpdateSummary: "whole plan edit", UpdateScope: "plan", UpdateKind: "test_definition_update", RevisionKind: PlanRevisionKindDefinition, Document: changedDoc})
	if err != nil {
		t.Fatalf("save changed plan: %v", err)
	}
	if changed.Version != original.Version+1 {
		t.Fatalf("changed version = %d, want %d", changed.Version, original.Version+1)
	}

	result, err := NewPlanLifecycleService(svc).RestorePlanRevision(PlanLifecycleRevisionRestoreInput{SessionID: sessionID, PlanID: original.ID, Version: original.Version})
	if err != nil {
		t.Fatalf("restore revision: %v", err)
	}
	if result.Plan.Version != changed.Version+1 || result.Plan.ParentRevision != changed.Version {
		t.Fatalf("restored revision linkage = v%d parent %d, want v%d parent %d", result.Plan.Version, result.Plan.ParentRevision, changed.Version+1, changed.Version)
	}
	if result.Plan.RestoredFromVersion != original.Version || result.Plan.UpdateKind != "restore_revision" || result.Plan.RevisionKind != PlanRevisionKindDefinition {
		t.Fatalf("restore metadata = kind %q revision_kind %q restored_from %d", result.Plan.UpdateKind, result.Plan.RevisionKind, result.Plan.RestoredFromVersion)
	}
	if got := checkpointIDs(result.Plan.Document.Checkpoints); strings.Join(got, ",") != "cp-1,cp-2" {
		t.Fatalf("restored checkpoints = %v", got)
	}
	if result.Plan.Document.ActiveCheckpointID != "cp-1" || result.Plan.Document.Checkpoints[0].Status != PlanCheckpointStatusPending {
		t.Fatalf("restore-only should reset execution to first pending checkpoint: %#v", result.Plan.Document)
	}
	revisions, err := svc.ListPlanRevisions(sessionID, original.ID, 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) < 3 || revisions[0].Version != result.Plan.Version || revisions[1].Version != changed.Version || revisions[2].Version != original.Version {
		t.Fatalf("restore history not retained newest-first: %#v", revisions)
	}
}

func TestPlanLifecycleRestartFromFirstRevisionPreparesFreshCheckpointRun(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	original := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "Original one", Status: PlanCheckpointStatusPending},
		{ID: "cp-2", Title: "Original two", Status: PlanCheckpointStatusPending},
	})
	changedDoc := clonePlanLifecycleDocument(original.Document)
	changedDoc.Checkpoints = []pebblestore.SessionPlanCheckpoint{{ID: "changed", Title: "Changed checkpoint", Status: PlanCheckpointStatusPending}}
	changedDoc.ActiveCheckpointID = "changed"
	if _, _, err := svc.SavePlanWithMetadata(sessionID, original.ID, original.Title, original.Plan, "approved", "approved", true, PlanSaveMetadata{UpdateSummary: "whole plan edit", UpdateScope: "plan", UpdateKind: "test_definition_update", RevisionKind: PlanRevisionKindDefinition, Document: changedDoc}); err != nil {
		t.Fatalf("save changed plan: %v", err)
	}

	result, err := NewPlanLifecycleService(svc).RestorePlanRevision(PlanLifecycleRevisionRestoreInput{SessionID: sessionID, PlanID: original.ID, Version: original.Version, CheckpointID: "cp-2", Start: true, SkipPrior: true, RunID: "run-restore-1", RunSessionID: sessionID, ParentSessionID: sessionID, StartedAt: 1234})
	if err != nil {
		t.Fatalf("restart from revision: %v", err)
	}
	if result.Action != "jump_to_checkpoint" || result.CheckpointID != "cp-2" || result.AttemptID != "cp-2:attempt-1" {
		t.Fatalf("jump result action/checkpoint/attempt = %q/%q/%q", result.Action, result.CheckpointID, result.AttemptID)
	}
	if result.Plan.RestoredFromVersion != original.Version || result.Plan.RevisionKind != PlanRevisionKindDefinition {
		t.Fatalf("restart restore metadata = restored_from %d revision_kind %q", result.Plan.RestoredFromVersion, result.Plan.RevisionKind)
	}
	first := result.Plan.Document.Checkpoints[0]
	second := result.Plan.Document.Checkpoints[1]
	if first.Status != PlanCheckpointStatusCompleted || first.Review == nil || first.Review.Result != "user_skipped_to_checkpoint" {
		t.Fatalf("prior checkpoint should be explicitly skipped by user jump: %#v", first)
	}
	if second.Status != PlanCheckpointStatusInProgress || second.RunID != "run-restore-1" || second.AttemptID != "cp-2:attempt-1" {
		t.Fatalf("selected checkpoint should be freshly in progress: %#v", second)
	}
}

func TestPlanLifecycleRestartFromRevisionRequiresExplicitSkipPriorForJump(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	if _, _, err := svc.SetMode(sessionID, ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	original := saveApprovedLifecyclePlan(t, svc, sessionID, pebblestore.SessionPlanExecutionPolicy{
		Mode:  PlanExecutionPolicyModeAutomatic,
		Shape: PlanExecutionShapeCheckpointed,
	}, []pebblestore.SessionPlanCheckpoint{
		{ID: "cp-1", Title: "Original one", Status: PlanCheckpointStatusPending},
		{ID: "cp-2", Title: "Original two", Status: PlanCheckpointStatusPending},
	})

	_, err := NewPlanLifecycleService(svc).RestorePlanRevision(PlanLifecycleRevisionRestoreInput{SessionID: sessionID, PlanID: original.ID, Version: original.Version, CheckpointID: "cp-2", Start: true, RunID: "run-restore-1", RunSessionID: sessionID, ParentSessionID: sessionID, StartedAt: 1234})
	if err == nil || !strings.Contains(err.Error(), "skip_prior=true") {
		t.Fatalf("restart without explicit skip_prior error = %v, want skip_prior requirement", err)
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
