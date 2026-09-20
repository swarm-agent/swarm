package run

import (
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestPlanManageAwaitingReviewRejectsInlineSubtaskResume(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	doc := &pebblestore.SessionPlanDocument{
		ExecutionPolicy: pebblestore.SessionPlanExecutionPolicy{
			Mode:  sessionruntime.PlanExecutionPolicyModeAutomatic,
			Shape: sessionruntime.PlanExecutionShapeCheckpointed,
		},
		ExecutionState: &pebblestore.SessionPlanExecutionState{
			Status:          sessionruntime.PlanExecutionStateWaitingReview,
			ActiveAttemptID: "attempt-1",
			CurrentRunID:    "run-1",
			LastOutcome:     sessionruntime.PlanCheckpointStatusCompleted,
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{
			ID:                 "cp-1",
			Title:              "Landing page",
			Status:             sessionruntime.PlanCheckpointStatusCompleted,
			Objective:          "Build landing page",
			AcceptanceCriteria: []string{"Done"},
			CompletedAt:        100,
			Review:             &pebblestore.SessionPlanCheckpointReview{Status: sessionruntime.PlanCheckpointReviewStatusPending},
			Subtasks: []pebblestore.SessionPlanSubtask{{
				ID:          "task-1",
				Title:       "Build page",
				Status:      sessionruntime.PlanSubtaskStatusCompleted,
				CompletedAt: 90,
			}},
		}},
		ActiveCheckpointID: "cp-1",
	}

	_, _, err := sessionSvc.SavePlanWithMetadata(sessionID, "plan-1", "Test Plan", "# Test Plan", "approved", "approved", true, sessionruntime.PlanSaveMetadata{
		Document: doc,
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}

	// 1. Invoking add_subtask via an inline provider-managed run when plan is waiting review must be rejected.
	_, err = runSvc.executePlanManageToolWithLifecycleRunContext(sessionID, `{"action":"add_subtask","checkpoint_id":"cp-1","subtask":{"title":"Unsolicited additive work"}}`, "", nil, planLifecycleRunContext{
		RunID:           "run-new",
		RunSessionID:    sessionID,
		ParentSessionID: sessionID,
		Inline:          true,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot add or update subtasks on a plan awaiting review") {
		t.Fatalf("expected awaiting review rejection error, got: %v", err)
	}

	// 2. Verify active plan was not mutated and remains in waiting_review.
	activePlan, ok, err := sessionSvc.GetActivePlan(sessionID)
	if err != nil || !ok {
		t.Fatalf("get active plan: ok=%v err=%v", ok, err)
	}
	if activePlan.Document.ExecutionState.Status != sessionruntime.PlanExecutionStateWaitingReview {
		t.Fatalf("plan status changed: got %q, want waiting_review", activePlan.Document.ExecutionState.Status)
	}
	if len(activePlan.Document.Checkpoints[0].Subtasks) != 1 {
		t.Fatalf("checkpoint subtasks count changed: got %d, want 1", len(activePlan.Document.Checkpoints[0].Subtasks))
	}
}
