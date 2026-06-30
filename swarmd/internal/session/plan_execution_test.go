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
	if !decision.AutoAdvanceAllowed || decision.NextCheckpointID != "cp-2" || decision.PlanComplete || doc.ActiveCheckpointID != "cp-2" {
		t.Fatalf("decision = %#v active=%q", decision, doc.ActiveCheckpointID)
	}
	if doc.Checkpoints[0].Status != PlanCheckpointStatusCompleted || doc.Checkpoints[0].AttemptID != "attempt-1" || len(doc.Checkpoints[0].Attempts) != 1 {
		t.Fatalf("checkpoint runtime metadata = %#v", doc.Checkpoints[0])
	}
	if doc.ExecutionState.LastCheckpointID != "cp-1" || doc.ExecutionState.LastOutcome != PlanCheckpointStatusCompleted || doc.ExecutionState.ParentSessionID != "parent-session" {
		t.Fatalf("execution state = %#v", doc.ExecutionState)
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
