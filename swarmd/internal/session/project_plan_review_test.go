package session

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: publication and task acceptance use ValidateProjectPlanReview to
// reject empty/legacy reviews, not merely valid execution metadata. This pure
// boundary test proves checklist binding and readable content without providers.
func TestProjectPlanReviewRequiresBoundReadableContent(t *testing.T) {
	for _, kind := range []string{"valid", "nil", "missing-requirements", "blank-requirement", "unbound", "missing-checkpoints", "blank-task", "blank-goal"} {
		t.Run(kind, func(t *testing.T) {
			doc := &pebblestore.SessionPlanDocument{
				Title: "Repair settings", Info: pebblestore.SessionPlanInfo{Goal: "Persist preferences"},
				Requirements: []pebblestore.SessionPlanRequirement{{ID: "r1", Text: "Settings survive restart", CheckpointID: "cp-1"}},
				Checkpoints:  []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Order: 1, Title: "Persist", Status: "pending", Tasks: []string{"Write settings atomically"}, AcceptanceCriteria: []string{"Settings survive restart"}}},
			}
			switch kind {
			case "nil":
				doc = nil
			case "missing-requirements":
				doc.Requirements = nil
			case "blank-requirement":
				doc.Requirements[0].Text = " "
			case "unbound":
				doc.Requirements[0].Text = "Different outcome"
			case "missing-checkpoints":
				doc.Checkpoints = nil
			case "blank-task":
				doc.Checkpoints[0].Tasks = []string{" "}
			case "blank-goal":
				doc.Info.Goal = " "
			}
			err := ValidateProjectPlanReview(doc)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("review validation: %v", err)
			}
		})
	}
}
