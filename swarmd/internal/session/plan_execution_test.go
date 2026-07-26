package session

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestNormalizePlanDocumentForSaveAddsExecutionPolicyAndActiveCheckpoint(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: "auto", Shape: "checkpoints"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Status: "done"},
			{ID: "cp-2"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("normalize execution document: %v", err)
	}
	if doc.ExecutionPolicy.Mode != PlanExecutionPolicyModeAutomatic || doc.ExecutionPolicy.Shape != PlanExecutionShapeCheckpointed {
		t.Fatalf("execution policy = %#v", doc.ExecutionPolicy)
	}
	if doc.Checkpoints[0].Status != PlanCheckpointStatusCompleted || doc.Checkpoints[1].Status != PlanCheckpointStatusPending {
		t.Fatalf("checkpoint statuses = %#v", doc.Checkpoints)
	}
	if doc.ActiveCheckpointID != "cp-2" {
		t.Fatalf("active checkpoint = %q, want next pending checkpoint", doc.ActiveCheckpointID)
	}
}

func TestValidatePlanDocumentRejectsInvalidExecutionMetadata(t *testing.T) {
	_, err := NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: "surprise"},
		Checkpoints:     []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "execution_policy.mode") {
		t.Fatalf("invalid policy error = %v, want execution_policy.mode", err)
	}

	_, err = NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID:     "cp-1",
			Status: "mystery",
		}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("invalid status error = %v, want status rejection", err)
	}
}

func TestPlanExecutionSummaryAndOutcomeDecisionAreDeterministic(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Status: PlanCheckpointStatusInProgress},
			{ID: "cp-2", Status: PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	checkpoint, summary, ok := SelectNextPlanCheckpoint(doc)
	if !ok || checkpoint.ID != "cp-1" || summary.NextCheckpointID != "cp-1" || !summary.AutoAdvanceAllowed || summary.ReviewRequired {
		t.Fatalf("next checkpoint = %#v summary=%#v ok=%v", checkpoint, summary, ok)
	}

	// Simulate a user flipping the sidebar policy while this checkpoint run is still
	// using stale automatic-mode prompt context. The backend document policy and
	// persisted run ownership must be authoritative when the terminal outcome applies.
	doc.ExecutionPolicy.Mode = PlanExecutionPolicyModeReviewEachCheckpoint
	doc.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateInProgress, ActiveAttemptID: "attempt-1", CurrentRunID: "run-1", CurrentSessionID: "child-session", ParentSessionID: "parent-session"}
	doc.Checkpoints[0].AttemptID = "attempt-1"
	doc.Checkpoints[0].RunID = "run-1"
	doc.Checkpoints[0].SessionID = "child-session"
	doc.Checkpoints[0].Attempts = []pebblestore.SessionPlanCheckpointAttempt{{ID: "attempt-1", CheckpointID: "cp-1", Status: PlanCheckpointStatusInProgress, RunID: "run-1", SessionID: "child-session", ParentSessionID: "parent-session"}}

	decision, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{
		CheckpointID:    "cp-1",
		Outcome:         PlanCheckpointStatusCompleted,
		AttemptID:       "attempt-1",
		RunID:           "run-1",
		SessionID:       "child-session",
		ParentSessionID: "parent-session",
		Report:          "cp1 complete",
		ChangedFiles:    []string{" swarmd/internal/session/plan_execution.go "},
		Validation:      []string{" targeted test "},
		StartedAt:       10,
		CompletedAt:     20,
	})
	if err != nil {
		t.Fatalf("apply outcome: %v", err)
	}
	if !decision.ReviewRequired || decision.AutoAdvanceAllowed || decision.NextCheckpointID != "" || decision.StopReason != PlanCheckpointStatusNeedsReview || doc.ActiveCheckpointID != "cp-1" {
		t.Fatalf("decision = %#v active=%q", decision, doc.ActiveCheckpointID)
	}
	if doc.Checkpoints[0].Status != PlanCheckpointStatusCompleted || doc.Checkpoints[0].AttemptID != "attempt-1" || len(doc.Checkpoints[0].Attempts) != 1 {
		t.Fatalf("checkpoint runtime metadata = %#v", doc.Checkpoints[0])
	}
	if doc.Checkpoints[0].Review == nil || doc.Checkpoints[0].Review.Status != PlanCheckpointReviewStatusPending {
		t.Fatalf("review metadata = %#v", doc.Checkpoints[0].Review)
	}
	if doc.ExecutionState.LastCheckpointID != "cp-1" || doc.ExecutionState.LastOutcome != PlanCheckpointStatusCompleted || doc.ExecutionState.ParentSessionID != "parent-session" || doc.ExecutionState.Status != PlanExecutionStateWaitingReview {
		t.Fatalf("execution state = %#v", doc.ExecutionState)
	}
}

func TestPlanCheckpointStartAndOutcomeRequireExactRunOwnership(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ID: "plan-owned", ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed}, ActiveCheckpointID: "cp-1",
		ExecutionState: &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateInProgress, ActiveAttemptID: "attempt-1", CurrentRunID: "run-1", CurrentSessionID: "child-1", ParentSessionID: "parent-1"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: PlanCheckpointStatusInProgress, AttemptID: "attempt-1", RunID: "run-1", SessionID: "child-1", Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "attempt-1", CheckpointID: "cp-1", Status: PlanCheckpointStatusInProgress, RunID: "run-1", SessionID: "child-1", ParentSessionID: "parent-1"}}}},
	}
	matching := PlanCheckpointStartOptions{PlanID: "plan-owned", CheckpointID: "cp-1", AttemptID: "attempt-1", RunID: "run-1", SessionID: "child-1", ParentSessionID: "parent-1"}
	if decision, err := ApplyPlanCheckpointStart(doc, matching); err != nil || decision.AttemptID != "attempt-1" || len(doc.Checkpoints[0].Attempts) != 1 {
		t.Fatalf("idempotent start retry: decision=%#v err=%v doc=%#v", decision, err, doc)
	}
	foreign := PlanCheckpointOutcomeOptions{PlanID: "plan-owned", CheckpointID: "cp-1", Outcome: PlanCheckpointStatusCompleted, AttemptID: "attempt-1", RunID: "foreign-run", SessionID: "child-1", ParentSessionID: "parent-1"}
	if _, err := ApplyPlanCheckpointOutcome(doc, foreign); err == nil || !strings.Contains(err.Error(), "active run ownership") {
		t.Fatalf("foreign outcome error = %v", err)
	}
	if doc.Checkpoints[0].Status != PlanCheckpointStatusInProgress {
		t.Fatalf("foreign outcome mutated checkpoint: %#v", doc.Checkpoints[0])
	}
	owned := foreign
	owned.RunID = "run-1"
	if _, err := ApplyPlanCheckpointOutcome(doc, owned); err != nil {
		t.Fatalf("owned outcome: %v", err)
	}
	if doc.Checkpoints[0].Status != PlanCheckpointStatusCompleted {
		t.Fatalf("owned outcome did not complete checkpoint: %#v", doc.Checkpoints[0])
	}
	if _, err := ApplyPlanCheckpointOutcome(doc, owned); err != nil {
		t.Fatalf("exact terminal outcome retry: %v", err)
	}
}

func TestPlanCheckpointOutcomeAutomaticUsesStoredPolicyForNextCheckpoint(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: PlanCheckpointStatusInProgress}, {ID: "cp-2", Status: PlanCheckpointStatusPending}},
		ActiveCheckpointID: "cp-1",
	}, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	decision, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{CheckpointID: "cp-1", Outcome: PlanCheckpointStatusCompleted})
	if err != nil {
		t.Fatalf("apply automatic outcome: %v", err)
	}
	if !decision.AutoAdvanceAllowed || decision.ReviewRequired || decision.NextCheckpointID != "cp-2" || doc.ActiveCheckpointID != "cp-2" {
		t.Fatalf("automatic decision = %#v active=%q", decision, doc.ActiveCheckpointID)
	}
	if doc.Checkpoints[0].Review != nil {
		t.Fatalf("automatic completion should not create checkpoint review: %#v", doc.Checkpoints[0].Review)
	}
}

func TestPlanCheckpointOutcomeCompletedAtomicallyCompletesSubtasksAndAdvances(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Status: PlanCheckpointStatusInProgress, ActiveSubtaskID: "task-1", Subtasks: []pebblestore.SessionPlanSubtask{
				{ID: "task-1", Title: "active", Status: PlanSubtaskStatusInProgress},
				{ID: "task-2", Title: "pending", Status: PlanSubtaskStatusPending},
				{ID: "task-3", Title: "done", Status: PlanSubtaskStatusCompleted, CompletedAt: 11},
			}},
			{ID: "cp-2", Status: PlanCheckpointStatusPending},
		},
		ActiveCheckpointID: "cp-1",
	}, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	decision, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{CheckpointID: "cp-1", Outcome: PlanCheckpointStatusCompleted, CompletedAt: 42})
	if err != nil {
		t.Fatalf("apply completed outcome: %v", err)
	}
	if decision.NextCheckpointID != "cp-2" || !decision.AutoAdvanceAllowed || doc.ActiveCheckpointID != "cp-2" {
		t.Fatalf("completion decision = %#v active=%q", decision, doc.ActiveCheckpointID)
	}
	checkpoint := doc.Checkpoints[0]
	if checkpoint.Status != PlanCheckpointStatusCompleted || checkpoint.ActiveSubtaskID != "" {
		t.Fatalf("completed checkpoint = %#v", checkpoint)
	}
	for _, subtask := range checkpoint.Subtasks {
		if subtask.Status != PlanSubtaskStatusCompleted {
			t.Fatalf("subtask %q status = %q", subtask.ID, subtask.Status)
		}
	}
	if checkpoint.Subtasks[0].CompletedAt != 42 || checkpoint.Subtasks[1].CompletedAt != 42 || checkpoint.Subtasks[2].CompletedAt != 11 {
		t.Fatalf("subtask completion timestamps = %#v", checkpoint.Subtasks)
	}
}

func TestPlanCheckpointNonCompletedOutcomesPreserveUnresolvedSubtasks(t *testing.T) {
	for _, outcome := range []string{PlanCheckpointStatusNeedsReview, PlanCheckpointStatusBlocked, PlanCheckpointStatusFailed} {
		t.Run(outcome, func(t *testing.T) {
			doc := &pebblestore.SessionPlanDocument{
				ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
				Checkpoints: []pebblestore.SessionPlanCheckpoint{{
					ID: "cp-1", Status: PlanCheckpointStatusInProgress, ActiveSubtaskID: "task-1",
					Subtasks: []pebblestore.SessionPlanSubtask{{ID: "task-1", Title: "active", Status: PlanSubtaskStatusInProgress}, {ID: "task-2", Title: "pending", Status: PlanSubtaskStatusPending}},
				}},
				ActiveCheckpointID: "cp-1",
			}
			if _, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{CheckpointID: "cp-1", Outcome: outcome, CompletedAt: 42}); err != nil {
				t.Fatalf("apply %s outcome: %v", outcome, err)
			}
			checkpoint := doc.Checkpoints[0]
			if checkpoint.ActiveSubtaskID != "task-1" || checkpoint.Subtasks[0].Status != PlanSubtaskStatusInProgress || checkpoint.Subtasks[1].Status != PlanSubtaskStatusPending {
				t.Fatalf("%s outcome changed unresolved subtasks: %#v", outcome, checkpoint)
			}
		})
	}
}

func TestPlanCheckpointOutcomeNeedsReviewStopsAutoAdvance(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: PlanCheckpointStatusInProgress}, {ID: "cp-2", Status: PlanCheckpointStatusPending}},
		ActiveCheckpointID: "cp-1",
	}, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	decision, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{CheckpointID: "cp-1", Outcome: PlanCheckpointStatusNeedsReview})
	if err != nil {
		t.Fatalf("apply review outcome: %v", err)
	}
	if !decision.ReviewRequired || decision.AutoAdvanceAllowed || decision.NextCheckpointID != "" || doc.ActiveCheckpointID != "cp-1" {
		t.Fatalf("review decision = %#v active=%q", decision, doc.ActiveCheckpointID)
	}
	if doc.Checkpoints[0].Review == nil || doc.Checkpoints[0].Review.Status != PlanCheckpointReviewStatusPending {
		t.Fatalf("review metadata = %#v", doc.Checkpoints[0].Review)
	}
	_, summary, ok := SelectNextPlanCheckpoint(doc)
	if ok || !summary.ReviewRequired || summary.StopReason != PlanCheckpointStatusNeedsReview {
		t.Fatalf("summary after review = %#v ok=%v", summary, ok)
	}
}

func TestPlanDocumentPatchUpdatesExecutionPolicyAndOutcome(t *testing.T) {
	doc, err := ApplyPlanDocumentPatch("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		ID:                 "plan-exec",
		Title:              "Execution Plan",
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: PlanCheckpointStatusInProgress}, {ID: "cp-2", Status: PlanCheckpointStatusPending}},
		ActiveCheckpointID: "cp-1",
	}, PlanDocumentPatch{Operations: []PlanDocumentPatchOperation{
		{Operation: "update_execution_policy", ExecutionPolicy: &pebblestore.SessionPlanExecutionPolicy{Mode: "automatic", Shape: "checkpointed"}},
		{Operation: "checkpoint_outcome", CheckpointID: "cp-1", Status: "completed", AttemptID: "attempt-1", RunID: "run-1", RunSessionID: "child-session", ParentSessionID: "parent-session"},
	}})
	if err != nil {
		t.Fatalf("apply execution patch: %v", err)
	}
	if doc.ExecutionPolicy.Mode != PlanExecutionPolicyModeAutomatic || doc.ActiveCheckpointID != "cp-2" {
		t.Fatalf("patched doc policy/active = %#v active=%q", doc.ExecutionPolicy, doc.ActiveCheckpointID)
	}
	if doc.Checkpoints[0].AttemptID != "attempt-1" || doc.ExecutionState.LastAttemptID != "attempt-1" {
		t.Fatalf("patched runtime metadata = checkpoint %#v state %#v", doc.Checkpoints[0], doc.ExecutionState)
	}
}

func TestPlanCheckpointStartRejectsAdvancingWhileAnotherCheckpointInProgress(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: PlanCheckpointStatusInProgress}, {ID: "cp-2", Status: PlanCheckpointStatusPending}},
		ActiveCheckpointID: "cp-1",
	}, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	_, err = ApplyPlanCheckpointStart(doc, PlanCheckpointStartOptions{CheckpointID: "cp-2", AttemptID: "cp-2:attempt-1"})
	if err == nil || !strings.Contains(err.Error(), "resolve it first") {
		t.Fatalf("start cp-2 error = %v, want continuity rejection", err)
	}
	if doc.ActiveCheckpointID != "cp-1" || doc.Checkpoints[0].Status != PlanCheckpointStatusInProgress || doc.Checkpoints[1].Status != PlanCheckpointStatusPending {
		t.Fatalf("doc mutated after rejected start: active=%q checkpoints=%#v", doc.ActiveCheckpointID, doc.Checkpoints)
	}
}

func TestValidatePlanDocumentRejectsUnresolvedCheckpointsBeforeActive(t *testing.T) {
	tests := []struct {
		name       string
		checkpoint pebblestore.SessionPlanCheckpoint
		want       string
	}{
		{name: "in progress", checkpoint: pebblestore.SessionPlanCheckpoint{ID: "cp-1", Status: PlanCheckpointStatusInProgress}, want: "status \"in_progress\" is before active_checkpoint_id"},
		{name: "pending", checkpoint: pebblestore.SessionPlanCheckpoint{ID: "cp-1", Status: PlanCheckpointStatusPending}, want: "status \"pending\" is before active_checkpoint_id"},
		{name: "waiting review", checkpoint: pebblestore.SessionPlanCheckpoint{ID: "cp-1", Status: PlanCheckpointStatusCompleted, Review: &pebblestore.SessionPlanCheckpointReview{Status: PlanCheckpointReviewStatusPending}}, want: "waiting for review before active_checkpoint_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizePlanDocumentForSave("plan-exec", "Execution Plan", &pebblestore.SessionPlanDocument{
				ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
				Checkpoints:        []pebblestore.SessionPlanCheckpoint{tt.checkpoint, {ID: "cp-2", Status: PlanCheckpointStatusPending}},
				ActiveCheckpointID: "cp-2",
			}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("normalize unresolved checkpoint error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPlanCheckpointCancellationPausesExactOwnedRunAndRestartResumes(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ID:                 "plan-cancel",
		Title:              "Cancellation plan",
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		ActiveCheckpointID: "cp-1",
		ExecutionState: &pebblestore.SessionPlanExecutionState{
			Status: PlanExecutionStateInProgress, ActiveAttemptID: "cp-1:attempt-1", CurrentRunID: "run-1", CurrentSessionID: "session-1", ParentSessionID: "parent-1",
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Status: PlanCheckpointStatusInProgress, AttemptID: "cp-1:attempt-1", RunID: "run-1", SessionID: "session-1",
			ActiveSubtaskID: "task-1", Subtasks: []pebblestore.SessionPlanSubtask{{ID: "task-1", Title: "work", Status: PlanSubtaskStatusInProgress}},
			Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "cp-1:attempt-1", CheckpointID: "cp-1", Status: PlanCheckpointStatusInProgress, RunID: "run-1", SessionID: "session-1", ParentSessionID: "parent-1"}},
		}},
	}
	stale := *doc
	stale.Checkpoints = clonePlanCheckpointSlice(doc.Checkpoints)
	stale.ExecutionState = &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateInProgress, ActiveAttemptID: "cp-1:attempt-1", CurrentRunID: "run-1", CurrentSessionID: "session-1", ParentSessionID: "parent-1"}
	decision, err := ApplyPlanCheckpointCancellation(&stale, PlanCheckpointCancellationOptions{PlanID: "plan-cancel", CheckpointID: "cp-1", AttemptID: "cp-1:attempt-1", RunID: "stale-run", SessionID: "session-1", ParentSessionID: "parent-1", CancelledAt: 42})
	if err != nil || decision.Changed || stale.Checkpoints[0].Status != PlanCheckpointStatusInProgress {
		t.Fatalf("stale cancellation changed plan: decision=%#v err=%v doc=%#v", decision, err, stale)
	}

	decision, err = ApplyPlanCheckpointCancellation(doc, PlanCheckpointCancellationOptions{PlanID: "plan-cancel", CheckpointID: "cp-1", AttemptID: "cp-1:attempt-1", RunID: "run-1", SessionID: "session-1", ParentSessionID: "parent-1", Reason: "user stopped run", CancelledAt: 42})
	if err != nil || !decision.Changed {
		t.Fatalf("matching cancellation: decision=%#v err=%v", decision, err)
	}
	checkpoint := doc.Checkpoints[0]
	if checkpoint.Status != PlanCheckpointStatusPaused || checkpoint.Result != "run_paused" || checkpoint.Attempts[0].Status != PlanCheckpointStatusPaused || checkpoint.Attempts[0].Outcome != PlanCheckpointStatusPaused {
		t.Fatalf("paused checkpoint/attempt = %#v", checkpoint)
	}
	if doc.ExecutionState.Status != PlanExecutionStatePaused || doc.ExecutionState.ActiveAttemptID != "" || doc.ExecutionState.CurrentRunID != "" || doc.ExecutionState.LastOutcome != PlanCheckpointStatusPaused {
		t.Fatalf("paused execution state = %#v", doc.ExecutionState)
	}
	if summary := SummarizePlanExecution(doc); !summary.Paused || summary.Failed || summary.StopReason != PlanCheckpointStatusPaused || summary.AutoAdvanceAllowed {
		t.Fatalf("paused execution summary = %#v", summary)
	}
	if checkpoint.Subtasks[0].Status != PlanSubtaskStatusPending || checkpoint.ActiveSubtaskID != "" {
		t.Fatalf("cancellation did not return active subtask to retryable pending state: %#v", checkpoint.Subtasks)
	}
	if _, err := ApplyPlanCheckpointReset(doc, PlanCheckpointResetOptions{CheckpointID: "cp-1"}); err != nil {
		t.Fatalf("paused checkpoint is not restartable: %v", err)
	}
	if doc.Checkpoints[0].Status != PlanCheckpointStatusPending || doc.ExecutionState.Status != PlanExecutionStateIdle {
		t.Fatalf("reset paused checkpoint = %#v state=%#v", doc.Checkpoints[0], doc.ExecutionState)
	}
	started, err := ApplyPlanCheckpointStart(doc, PlanCheckpointStartOptions{PlanID: "plan-cancel", CheckpointID: "cp-1", RunID: "run-2", SessionID: "session-1", ParentSessionID: "parent-1", StartedAt: 43})
	if err != nil || started.Status != PlanCheckpointStatusInProgress || doc.ExecutionState.Status != PlanExecutionStateInProgress || doc.ExecutionState.CurrentRunID != "run-2" {
		t.Fatalf("restart paused checkpoint: decision=%#v err=%v state=%#v", started, err, doc.ExecutionState)
	}
}

func TestPlanDocumentPatchRejectsSetActiveAndUpsertThatStrandInProgress(t *testing.T) {
	base := &pebblestore.SessionPlanDocument{
		ID:                 "plan-exec",
		Title:              "Execution Plan",
		ExecutionPolicy:    pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeAutomatic, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Status: PlanCheckpointStatusInProgress}, {ID: "cp-2", Status: PlanCheckpointStatusPending}},
		ActiveCheckpointID: "cp-1",
	}
	_, err := ApplyPlanDocumentPatch("plan-exec", "Execution Plan", base, PlanDocumentPatch{Operation: "set_active_checkpoint", ActiveCheckpointID: "cp-2"})
	if err == nil || !strings.Contains(err.Error(), "resolve it first") {
		t.Fatalf("set active error = %v, want continuity rejection", err)
	}
	_, err = ApplyPlanDocumentPatch("plan-exec", "Execution Plan", base, PlanDocumentPatch{Operation: "upsert_checkpoint", Checkpoint: &pebblestore.SessionPlanCheckpoint{ID: "cp-2", Status: PlanCheckpointStatusInProgress}})
	if err == nil || !strings.Contains(err.Error(), "resolve it first") {
		t.Fatalf("upsert in_progress error = %v, want continuity rejection", err)
	}
}
