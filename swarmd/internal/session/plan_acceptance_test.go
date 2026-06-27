package session

import (
	"strings"
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
			name:    "checkpointed pauses by default",
			options: PlanAcceptanceExecutionOptions{},
			mode:    PlanExecutionPolicyModeReviewEachCheckpoint,
			shape:   PlanExecutionShapeCheckpointed,
		},
		{
			name:    "checkpointed automatic",
			options: PlanAcceptanceExecutionOptions{ExecutionGranularity: "checkpoint-by-checkpoint", ContinuationPolicy: "continue automatically"},
			mode:    PlanExecutionPolicyModeAutomatic,
			shape:   PlanExecutionShapeCheckpointed,
		},
		{
			name:    "run through ignores pause toggle",
			options: PlanAcceptanceExecutionOptions{ExecutionGranularity: "run straight through", ContinuationPolicy: "pause for review"},
			mode:    PlanExecutionPolicyModeAutomatic,
			shape:   PlanExecutionShapeSingleRun,
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

	_, err := NormalizePlanAcceptanceExecutionPolicy(PlanAcceptanceExecutionOptions{ExecutionGranularity: "surprise"})
	if err == nil || !strings.Contains(err.Error(), "execution granularity") {
		t.Fatalf("invalid granularity error = %v", err)
	}
	_, err = NormalizePlanAcceptanceExecutionPolicy(PlanAcceptanceExecutionOptions{ContinuationPolicy: "surprise"})
	if err == nil || !strings.Contains(err.Error(), "continuation policy") {
		t.Fatalf("invalid continuation error = %v", err)
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

func TestFinalCheckpointCompletionDoesNotForceReviewPause(t *testing.T) {
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
	if !decision.PlanComplete || decision.ReviewRequired || decision.StopReason != "" {
		t.Fatalf("decision = %#v, want final plan complete without review pause", decision)
	}
	if doc.ActiveCheckpointID != "" || doc.ExecutionState == nil || doc.ExecutionState.Status != PlanExecutionStateCompleted || doc.ExecutionState.CompletedAt != 1234 {
		t.Fatalf("final execution state = active %q state %#v", doc.ActiveCheckpointID, doc.ExecutionState)
	}
	if doc.Checkpoints[1].Review != nil {
		t.Fatalf("final checkpoint should not receive synthetic review metadata: %#v", doc.Checkpoints[1].Review)
	}
	summary := SummarizePlanExecution(doc)
	if !summary.PlanComplete || summary.ReviewRequired || summary.NextCheckpointID != "" || summary.AutoAdvanceAllowed {
		t.Fatalf("summary = %#v, want completed plan", summary)
	}
}
