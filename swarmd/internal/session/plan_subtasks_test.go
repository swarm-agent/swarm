package session

import (
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanSubtasksNormalizeLegacyAndAdvanceWithoutCompletingCheckpoint(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
		ID: "plan-1", Title: "Plan", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Tasks: []string{"first", "second"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &doc.Checkpoints[0]
	if len(checkpoint.Subtasks) != 2 || checkpoint.Subtasks[0].ID != "task-1" {
		t.Fatalf("subtasks = %#v", checkpoint.Subtasks)
	}
	if _, err := ApplyPlanCheckpointStart(doc, PlanCheckpointStartOptions{CheckpointID: "cp-1", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if checkpoint.ActiveSubtaskID != "task-1" || checkpoint.Subtasks[0].Status != PlanSubtaskStatusInProgress {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	if err := completePlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", SubtaskID: "task-1", CompletedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if checkpoint.ActiveSubtaskID != "task-2" || checkpoint.Status != PlanCheckpointStatusInProgress {
		t.Fatalf("completion crossed checkpoint boundary: %#v", checkpoint)
	}
}

func TestPlanSubtaskCompletionRejectsLastTaskWithoutCheckpointCloseout(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
		ID: "plan-1", Title: "Plan", ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: "automatic", Shape: "checkpointed"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Tasks: []string{"first", "second"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlanCheckpointStart(doc, PlanCheckpointStartOptions{CheckpointID: "cp-1", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := completePlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", SubtaskID: "task-1", CompletedAt: 2}); err != nil {
		t.Fatal(err)
	}
	checkpoint := &doc.Checkpoints[0]
	err = completePlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", SubtaskID: "task-2", CompletedAt: 3})
	if err == nil || !strings.Contains(err.Error(), "complete_checkpoint=true") {
		t.Fatalf("expected formal checkpoint closeout error, got %v", err)
	}
	if checkpoint.Status != PlanCheckpointStatusInProgress || checkpoint.ActiveSubtaskID != "task-2" || checkpoint.Subtasks[1].Status != PlanSubtaskStatusInProgress {
		t.Fatalf("rejected final subtask completion mutated checkpoint: %#v", checkpoint)
	}
}

func TestPlanSubtaskCompletionBatchesAndCanFinishCheckpoint(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
		ID: "plan-1", Title: "Plan", ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: "automatic", Shape: "checkpointed"}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Tasks: []string{"first", "second", "third"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlanCheckpointStart(doc, PlanCheckpointStartOptions{CheckpointID: "cp-1", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := completePlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", SubtaskIDs: []string{"task-1", "task-2"}, CompletedAt: 2}); err != nil {
		t.Fatal(err)
	}
	checkpoint := &doc.Checkpoints[0]
	if checkpoint.Subtasks[0].Status != PlanSubtaskStatusCompleted || checkpoint.Subtasks[1].Status != PlanSubtaskStatusCompleted || checkpoint.ActiveSubtaskID != "task-3" {
		t.Fatalf("batched completion = %#v", checkpoint)
	}
	if err := completePlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", SubtaskIDs: []string{"task-3"}, CompleteCheckpoint: true, CompletedAt: 3, Report: "done", Result: "done"}); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Status != PlanCheckpointStatusCompleted || checkpoint.ActiveSubtaskID != "" || doc.ExecutionState == nil || doc.ExecutionState.Status != PlanExecutionStateWaitingReview {
		t.Fatalf("combined completion = %#v, state = %#v", checkpoint, doc.ExecutionState)
	}
}

func TestPlanSubtaskBatchCompletionIsAtomicOnUnknownID(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
		ID: "plan-1", Title: "Plan", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Tasks: []string{"first", "second"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := completePlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", SubtaskIDs: []string{"task-1", "missing"}, CompletedAt: 2}); err == nil {
		t.Fatal("expected unknown subtask error")
	}
	if doc.Checkpoints[0].Subtasks[0].Status == PlanSubtaskStatusCompleted {
		t.Fatalf("invalid batch partially mutated subtasks: %#v", doc.Checkpoints[0].Subtasks)
	}
}

func TestPlanSubtaskFocusDemotesPreviousActive(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
		ID: "plan-1", Title: "Plan", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Tasks: []string{"first", "second"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := focusPlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", SubtaskID: "task-1"}); err != nil {
		t.Fatal(err)
	}
	if err := focusPlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", SubtaskID: "task-2"}); err != nil {
		t.Fatal(err)
	}
	checkpoint := doc.Checkpoints[0]
	if checkpoint.Subtasks[0].Status != PlanSubtaskStatusPending || checkpoint.Subtasks[1].Status != PlanSubtaskStatusInProgress {
		t.Fatalf("subtasks = %#v", checkpoint.Subtasks)
	}
}

func TestAddPlanSubtaskReopensSameCheckpointWithoutResettingAttempt(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
		ID: "plan-1", Title: "Plan", ExecutionState: &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateWaitingReview, ActiveAttemptID: "attempt-1", CurrentRunID: "run-1", CurrentSessionID: "session-1"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Landing page", Status: PlanCheckpointStatusCompleted, Objective: "Polish the landing page", AcceptanceCriteria: []string{"Landing page is polished"},
			AttemptID: "attempt-1", RunID: "run-1", SessionID: "session-1", CompletedAt: 10, Review: &pebblestore.SessionPlanCheckpointReview{Status: PlanCheckpointReviewStatusPending},
			Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "attempt-1", CheckpointID: "cp-1", Status: PlanCheckpointStatusCompleted, RunID: "run-1", SessionID: "session-1", CompletedAt: 10}},
			Subtasks: []pebblestore.SessionPlanSubtask{{ID: "task-1", Title: "Build page", Status: PlanSubtaskStatusCompleted, CompletedAt: 9, Order: 1}},
		}}, ActiveCheckpointID: "cp-1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpointID := doc.Checkpoints[0].ID
	attemptID := doc.Checkpoints[0].AttemptID
	attempts := append([]pebblestore.SessionPlanCheckpointAttempt(nil), doc.Checkpoints[0].Attempts...)
	if err := addPlanCheckpointSubtask(doc, PlanDocumentPatchOperation{
		CheckpointID: "cp-1", Subtask: &pebblestore.SessionPlanSubtask{Title: "Make the hero headline blue"},
		RunID: "run-1", RunSessionID: "session-1", ParentSessionID: "parent-1", StartedAt: 11,
	}); err != nil {
		t.Fatal(err)
	}
	if len(doc.Checkpoints) != 1 {
		t.Fatalf("localized subtask created a new checkpoint: %#v", doc.Checkpoints)
	}
	checkpoint := doc.Checkpoints[0]
	if checkpoint.ID != checkpointID || checkpoint.AttemptID != attemptID {
		t.Fatalf("localized subtask reset checkpoint identity: %#v", checkpoint)
	}
	if checkpoint.Objective != "Polish the landing page" || !reflect.DeepEqual(checkpoint.AcceptanceCriteria, []string{"Landing page is polished"}) {
		t.Fatalf("localized subtask changed checkpoint contract: %#v", checkpoint)
	}
	if !reflect.DeepEqual(checkpoint.Attempts, attempts) {
		t.Fatalf("localized subtask reset attempt history: got %#v want %#v", checkpoint.Attempts, attempts)
	}
	if checkpoint.Status != PlanCheckpointStatusInProgress || checkpoint.CompletedAt != 0 || checkpoint.Review != nil || doc.ActiveCheckpointID != "cp-1" {
		t.Fatalf("localized subtask did not reopen checkpoint in place: %#v doc=%#v", checkpoint, doc.ExecutionState)
	}
	if len(checkpoint.Subtasks) != 2 || checkpoint.ActiveSubtaskID != "task-2" || checkpoint.Subtasks[1].Status != PlanSubtaskStatusInProgress {
		t.Fatalf("localized subtask was not appended and focused: %#v", checkpoint.Subtasks)
	}
}

func TestReplacePlanSubtasksMakesSubmittedListAuthoritativeAndPreservesContract(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
		ID: "plan-1", Title: "Plan", ExecutionState: &pebblestore.SessionPlanExecutionState{Status: PlanExecutionStateWaitingReview, ActiveAttemptID: "attempt-1", CurrentRunID: "old-run", CurrentSessionID: "session-1", ParentSessionID: "parent-1"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID: "cp-1", Title: "Landing page", Status: PlanCheckpointStatusCompleted, Objective: "Polish the landing page", AcceptanceCriteria: []string{"Landing page is polished"},
			AttemptID: "attempt-1", RunID: "old-run", SessionID: "session-1", CompletedAt: 10,
			Attempts: []pebblestore.SessionPlanCheckpointAttempt{{ID: "attempt-1", CheckpointID: "cp-1", Status: PlanCheckpointStatusCompleted, RunID: "old-run", SessionID: "session-1", ParentSessionID: "parent-1"}},
			Subtasks: []pebblestore.SessionPlanSubtask{{ID: "stale-1", Title: "Obsolete audit", Status: PlanSubtaskStatusCompleted}, {ID: "stale-2", Title: "Obsolete fix", Status: PlanSubtaskStatusPending}},
		}}, ActiveCheckpointID: "cp-1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := doc.Checkpoints[0]
	err = replacePlanCheckpointSubtasks(doc, PlanDocumentPatchOperation{
		CheckpointID: "cp-1", RunID: "new-run", RunSessionID: "session-1", ParentSessionID: "parent-1", StartedAt: 11,
		Subtasks: []pebblestore.SessionPlanSubtask{{ID: "new-1", Title: "Apply revised spacing"}, {ID: "new-2", Title: "Inspect result"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := doc.Checkpoints[0]
	if checkpoint.ID != before.ID || checkpoint.Objective != before.Objective || !reflect.DeepEqual(checkpoint.AcceptanceCriteria, before.AcceptanceCriteria) || checkpoint.AttemptID != before.AttemptID || !reflect.DeepEqual(checkpoint.Attempts, before.Attempts) {
		t.Fatalf("replace_subtasks changed checkpoint contract or attempt history: before=%#v after=%#v", before, checkpoint)
	}
	if len(checkpoint.Subtasks) != 2 || checkpoint.Subtasks[0].ID != "new-1" || checkpoint.Subtasks[1].ID != "new-2" || checkpoint.ActiveSubtaskID != "new-1" || checkpoint.Subtasks[0].Status != PlanSubtaskStatusInProgress {
		t.Fatalf("replacement checklist = %#v active=%q", checkpoint.Subtasks, checkpoint.ActiveSubtaskID)
	}
	if checkpoint.RunID != "new-run" || doc.ExecutionState.CurrentRunID != "new-run" || checkpoint.Status != PlanCheckpointStatusInProgress {
		t.Fatalf("replacement ownership/state not resumed: checkpoint=%#v state=%#v", checkpoint, doc.ExecutionState)
	}
}

func TestReplacePlanSubtasksRejectsInvalidListAtomically(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{ID: "plan-1", Title: "Plan", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Tasks: []string{"existing"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := doc.Checkpoints[0]
	err = replacePlanCheckpointSubtasks(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", Subtasks: []pebblestore.SessionPlanSubtask{{ID: "dup", Title: "One"}, {ID: "dup", Title: "Two"}}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate validation error, got %v", err)
	}
	if !reflect.DeepEqual(doc.Checkpoints[0], before) {
		t.Fatalf("invalid replacement partially mutated checkpoint: before=%#v after=%#v", before, doc.Checkpoints[0])
	}
}

func TestPlanSubtaskResumeRejectsBlockedAndFailedCheckpoints(t *testing.T) {
	for _, status := range []string{PlanCheckpointStatusBlocked, PlanCheckpointStatusFailed} {
		t.Run(status, func(t *testing.T) {
			doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
				ID: "plan-1", Title: "Plan", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Stopped", Status: status, AttemptID: "attempt-1"}}, ActiveCheckpointID: "cp-1",
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := doc.Checkpoints[0]
			err = addPlanCheckpointSubtask(doc, PlanDocumentPatchOperation{CheckpointID: "cp-1", Subtask: &pebblestore.SessionPlanSubtask{Title: "Localized edit"}})
			if err == nil {
				t.Fatalf("add_subtask unexpectedly resumed %s checkpoint", status)
			}
			if doc.Checkpoints[0].Status != before.Status || doc.Checkpoints[0].AttemptID != before.AttemptID || len(doc.Checkpoints[0].Subtasks) != len(before.Subtasks) {
				t.Fatalf("rejected add_subtask mutated %s checkpoint: before=%#v after=%#v", status, before, doc.Checkpoints[0])
			}
		})
	}
}
