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
