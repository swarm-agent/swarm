package session

import (
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
