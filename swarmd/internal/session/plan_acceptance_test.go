package session

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestNormalizePlanAcceptanceExecutionPolicyMapsUserControls(t *testing.T) {
	tests := []struct {
		name    string
		options PlanAcceptanceExecutionOptions
		mode    string
		shape   string
	}{
		{
			name:    "checkpointed continues automatically by default",
			options: PlanAcceptanceExecutionOptions{},
			mode:    PlanExecutionPolicyModeAutomatic,
			shape:   PlanExecutionShapeCheckpointed,
		},
		{
			name:    "checkpointed automatic",
			options: PlanAcceptanceExecutionOptions{ExecutionGranularity: "checkpoint-by-checkpoint", ContinuationPolicy: "continue automatically"},
			mode:    PlanExecutionPolicyModeAutomatic,
			shape:   PlanExecutionShapeCheckpointed,
		},
		{
			name:    "manual review preserves checkpoints",
			options: PlanAcceptanceExecutionOptions{ContinuationPolicy: "pause for review"},
			mode:    PlanExecutionPolicyModeReviewEachCheckpoint,
			shape:   PlanExecutionShapeCheckpointed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := NormalizePlanAcceptanceExecutionPolicy(tt.options)
			if err != nil {
				t.Fatalf("normalize policy: %v", err)
			}
			if policy.Mode != tt.mode || policy.Shape != tt.shape {
				t.Fatalf("policy = %#v, want mode=%q shape=%q", policy, tt.mode, tt.shape)
			}
		})
	}
}

func TestApplyPlanAcceptanceExecutionPolicyOverridesAIRuntimeState(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-accept",
		Title: "Plan Accept",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  PlanExecutionPolicyModeAutomatic,
			Shape: PlanExecutionShapeCheckpointed,
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{
			Status:           PlanExecutionStateInProgress,
			ActiveAttemptID:  "ai-attempt",
			CurrentRunID:     "ai-run",
			CurrentSessionID: "ai-session",
		},
		ActiveCheckpointID: "cp-2",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{
				ID:           "cp-1",
				Status:       PlanCheckpointStatusCompleted,
				Report:       "ai report",
				Result:       "ai result",
				ChangedFiles: []string{"file.go"},
				Validation:   []string{"test"},
				AttemptID:    "ai-attempt",
				RunID:        "ai-run",
				SessionID:    "ai-session",
				Review:       &pebblestore.SessionPlanCheckpointReview{Status: PlanCheckpointReviewStatusPending},
				Attempts:     []pebblestore.SessionPlanCheckpointAttempt{{ID: "ai-attempt", Status: PlanCheckpointStatusCompleted}},
			},
			{ID: "cp-2", Status: PlanCheckpointStatusInProgress},
		},
	}

	policy, err := ApplyPlanAcceptanceExecutionPolicy(doc, PlanAcceptanceExecutionOptions{
		ExecutionGranularity: "checkpointed",
		ContinuationPolicy:   "pause for review",
	})
	if err != nil {
		t.Fatalf("apply acceptance policy: %v", err)
	}
	if policy.Mode != PlanExecutionPolicyModeReviewEachCheckpoint || policy.Shape != PlanExecutionShapeCheckpointed {
		t.Fatalf("policy = %#v", policy)
	}
	if doc.ExecutionState != nil {
		t.Fatalf("execution state was not cleared: %#v", doc.ExecutionState)
	}
	if doc.ActiveCheckpointID != "cp-1" {
		t.Fatalf("active checkpoint = %q, want first pending checkpoint", doc.ActiveCheckpointID)
	}
	for _, checkpoint := range doc.Checkpoints {
		if checkpoint.Status != PlanCheckpointStatusPending || checkpoint.AttemptID != "" || checkpoint.RunID != "" || checkpoint.SessionID != "" || checkpoint.Review != nil || len(checkpoint.Attempts) != 0 {
			t.Fatalf("checkpoint runtime was not reset: %#v", checkpoint)
		}
		if checkpoint.Report != "" || checkpoint.Result != "" || len(checkpoint.ChangedFiles) != 0 || len(checkpoint.Validation) != 0 {
			t.Fatalf("checkpoint completion fields were not reset: %#v", checkpoint)
		}
	}
}

func TestApplyPlanAcceptanceExecutionPolicyPreservesCheckpointBoundaries(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-single-preserve",
		Title: "Plan Single Preserve",
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: PlanCheckpointStatusPending, Objective: "Do first", Tasks: []string{"task one"}, AcceptanceCriteria: []string{"first done"}, Order: 1},
			{ID: "cp-2", Title: "Second", Status: PlanCheckpointStatusPending, Objective: "Do second", Tasks: []string{"task two"}, AcceptanceCriteria: []string{"second done"}, Order: 2},
		},
	}

	policy, err := ApplyPlanAcceptanceExecutionPolicy(doc, PlanAcceptanceExecutionOptions{})
	if err != nil {
		t.Fatalf("apply checkpointed acceptance policy: %v", err)
	}
	if policy.Shape != PlanExecutionShapeCheckpointed || doc.ActiveCheckpointID != "cp-1" {
		t.Fatalf("checkpointed policy/active = %#v active %q", policy, doc.ActiveCheckpointID)
	}
	if len(doc.Checkpoints) != 2 || doc.Checkpoints[0].ID != "cp-1" || doc.Checkpoints[1].ID != "cp-2" {
		t.Fatalf("checkpoint boundaries changed: %#v", doc.Checkpoints)
	}
	if len(doc.OriginalCheckpoints) != 0 {
		t.Fatalf("legacy shadow checkpoints unexpectedly created: %#v", doc.OriginalCheckpoints)
	}
}

func TestApplyPlanAcceptanceExecutionPolicyRestoresOriginalCheckpointsForCheckpointedRecovery(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{
		ID:    "plan-checkpointed-recovery",
		Title: "Plan Checkpointed Recovery",
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  PlanExecutionPolicyModeAutomatic,
			Shape: "single_run",
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "plan-run", Status: PlanCheckpointStatusInProgress, AttemptID: "attempt-1"}},
		OriginalCheckpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "First", Status: PlanCheckpointStatusPending, Objective: "Do first", Order: 1},
			{ID: "cp-2", Title: "Second", Status: PlanCheckpointStatusPending, Objective: "Do second", Order: 2},
		},
	}

	policy, err := ApplyPlanAcceptanceExecutionPolicy(doc, PlanAcceptanceExecutionOptions{ExecutionGranularity: "checkpointed"})
	if err != nil {
		t.Fatalf("apply checkpointed recovery policy: %v", err)
	}
	if policy.Shape != PlanExecutionShapeCheckpointed || doc.ActiveCheckpointID != "cp-1" {
		t.Fatalf("checkpointed policy/active = %#v active %q", policy, doc.ActiveCheckpointID)
	}
	if len(doc.Checkpoints) != 2 || doc.Checkpoints[0].ID != "cp-1" || doc.Checkpoints[1].ID != "cp-2" {
		t.Fatalf("checkpointed recovery did not restore original checkpoints: %#v", doc.Checkpoints)
	}
	if len(doc.OriginalCheckpoints) != 0 {
		t.Fatalf("original checkpoint shadow should be cleared after checkpointed recovery: %#v", doc.OriginalCheckpoints)
	}
}

func TestFinalCheckpointCompletionWaitsForPlanReview(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-final", "Final Plan", &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{Mode: PlanExecutionPolicyModeReviewEachCheckpoint, Shape: PlanExecutionShapeCheckpointed},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Status: PlanCheckpointStatusCompleted, Review: &pebblestore.SessionPlanCheckpointReview{Status: PlanCheckpointReviewStatusApproved}},
			{ID: "cp-2", Status: PlanCheckpointStatusInProgress},
		},
		ActiveCheckpointID: "cp-2",
	}, nil)
	if err != nil {
		t.Fatalf("normalize final doc: %v", err)
	}

	decision, err := ApplyPlanCheckpointOutcome(doc, PlanCheckpointOutcomeOptions{
		CheckpointID: "cp-2",
		Outcome:      PlanCheckpointStatusCompleted,
		CompletedAt:  1234,
	})
	if err != nil {
		t.Fatalf("complete final checkpoint: %v", err)
	}
	if decision.PlanComplete || !decision.ReviewRequired || decision.StopReason != PlanCheckpointStatusNeedsReview {
		t.Fatalf("decision = %#v, want final plan review pause", decision)
	}
	if doc.ActiveCheckpointID != "cp-2" || doc.ExecutionState == nil || doc.ExecutionState.Status != PlanExecutionStateWaitingReview || doc.ExecutionState.CompletedAt != 0 {
		t.Fatalf("final execution state = active %q state %#v", doc.ActiveCheckpointID, doc.ExecutionState)
	}
	if doc.Checkpoints[1].Status != PlanCheckpointStatusCompleted || doc.Checkpoints[1].Review == nil || doc.Checkpoints[1].Review.Status != PlanCheckpointReviewStatusPending {
		t.Fatalf("final checkpoint review metadata = %#v checkpoint=%#v", doc.Checkpoints[1].Review, doc.Checkpoints[1])
	}
	summary := SummarizePlanExecution(doc)
	if summary.PlanComplete || !summary.ReviewRequired || summary.NextCheckpointID != "cp-2" || summary.AutoAdvanceAllowed {
		t.Fatalf("summary = %#v, want waiting review", summary)
	}

	summary, err = ApplyPlanCheckpointReviewAcceptance(doc, PlanCheckpointReviewAcceptanceOptions{CheckpointID: "cp-2", ReviewedAt: 2345})
	if err != nil {
		t.Fatalf("accept final review: %v", err)
	}
	if !summary.PlanComplete || summary.ReviewRequired || summary.NextCheckpointID != "" {
		t.Fatalf("accepted summary = %#v, want completed plan", summary)
	}
	if doc.ActiveCheckpointID != "" || doc.ExecutionState.Status != PlanExecutionStateCompleted || doc.ExecutionState.CompletedAt != 2345 {
		t.Fatalf("accepted execution state = active %q state %#v", doc.ActiveCheckpointID, doc.ExecutionState)
	}
}
