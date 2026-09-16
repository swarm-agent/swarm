package session

import (
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: SummarizePlanExecution is the canonical projection of durable plan
// state. Blocker text must not turn review, failure or running permission waits
// into blocked work, and blocked work must never authorize automatic advance.
func TestPlanWaitingProjection(t *testing.T) {
	for _, active := range []string{"", "cp"} {
		for _, status := range []string{"blocked", "needs_review", "failed", "in_progress"} {
			doc := &store.SessionPlanDocument{ActiveCheckpointID: active, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp", Status: status, Handoff: &store.SessionPlanCheckpointHandoff{Overview: "Required input unavailable"}, Recommendation: &store.SessionPlanCheckpointRecommendation{Action: "Supply the required input"}}}}
			got := SummarizePlanExecution(doc)
			if status == "blocked" {
				if !got.Blocked || got.ReviewRequired || got.Failed || got.AutoAdvanceAllowed || got.WaitingReason != "Required input unavailable" || got.ResolutionAction != "Supply the required input" {
					t.Fatalf("blocked: %+v", got)
				}
			} else if got.Blocked || got.WaitingReason != "" || got.ResolutionAction != "" {
				t.Fatalf("conflated %s: %+v", status, got)
			}
		}
	}
}
