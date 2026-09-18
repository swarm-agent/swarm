package session

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestCompletedCheckpointDoomloopPrevention(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-1", "Plan", &pebblestore.SessionPlanDocument{
		ID:    "plan-1",
		Title: "Plan",
		ExecutionState: &pebblestore.SessionPlanExecutionState{
			Status:          PlanExecutionStateWaitingReview,
			ActiveAttemptID: "attempt-1",
			CurrentRunID:    "run-1",
			LastOutcome:     PlanCheckpointStatusCompleted,
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID:                 "cp-1",
			Title:              "Landing page",
			Status:             PlanCheckpointStatusCompleted,
			Objective:          "Build landing page",
			AcceptanceCriteria: []string{"Landing page is done"},
			CompletedAt:        100,
			Review:             &pebblestore.SessionPlanCheckpointReview{Status: PlanCheckpointReviewStatusPending},
			Subtasks: []pebblestore.SessionPlanSubtask{{
				ID:          "task-1",
				Title:       "Build page",
				Status:      PlanSubtaskStatusCompleted,
				CompletedAt: 90,
			}},
		}},
		ActiveCheckpointID: "cp-1",
	}, nil)
	if err != nil {
		t.Fatalf("normalize plan document: %v", err)
	}

	// 1. Verify that when all checkpoints are completed and plan is waiting review,
	// calling complete_checkpoint again is rejected.
	_, err = ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{
		CheckpointID: "cp-1",
		Outcome:      PlanCheckpointStatusCompleted,
	})
	if err == nil || !strings.Contains(err.Error(), "already waiting for final review") {
		t.Fatalf("expected already waiting for final review error, got: %v", err)
	}

	// 2. Verify SummarizePlanExecution indicates review is required when review is pending.
	summary := SummarizePlanExecution(doc)
	if !summary.ReviewRequired || summary.NextCheckpointID != "cp-1" {
		t.Fatalf("expected ReviewRequired=true with NextCheckpointID=cp-1, got: %+v", summary)
	}

	// 3. When review is accepted via ApplyPlanCheckpointReviewAcceptance, plan is complete.
	acceptedSummary, err := ApplyPlanCheckpointReviewAcceptance(doc, PlanCheckpointReviewAcceptanceOptions{CheckpointID: "cp-1", ReviewedAt: 120})
	if err != nil {
		t.Fatalf("accept review: %v", err)
	}
	if !acceptedSummary.PlanComplete || acceptedSummary.ReviewRequired || acceptedSummary.NextCheckpointID != "" {
		t.Fatalf("expected PlanComplete=true after review acceptance, got: %+v", acceptedSummary)
	}
}
