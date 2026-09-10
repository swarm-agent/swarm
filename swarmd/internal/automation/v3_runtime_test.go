package automation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

func (f *fakeRepo) ClaimAutomationDispatch(store.AutomationScope, string, string) error { return nil }

// Purpose: V3Runtime.pinnedDocument is the narrowest pure boundary proving that
// immutable approved source content is retained without carrying completed run
// state into a new occurrence. Changed bytes must reject without source mutation.
func TestPinnedExecutionDocumentResetsOnlyProgress(t *testing.T) {
	s, _, _, plans, p, scope, d := fixture(t)
	plans.plan.Document = &store.SessionPlanDocument{Title: "Source", Info: store.SessionPlanInfo{Goal: "Preserve goal"}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Title: "Check", Status: "completed", Objective: "Inspect", AcceptanceCriteria: []string{"Evidence"}, Report: "old", RunID: "prior", Subtasks: []store.SessionPlanSubtask{{ID: "task-1", Title: "Read", Status: "completed", Result: "old"}}}}}
	data, err := json.Marshal(plans.plan.Document)
	if err != nil {
		t.Fatal(err)
	}
	d.Plans[0].Plan.DocumentSHA256 = executionDocumentDigest(data)
	v := &V3Runtime{domain: s}
	def := store.AutomationRecord{Scope: scope, Definition: &d}
	doc, err := v.pinnedDocument(context.Background(), p, def, "new-plan")
	if err != nil {
		t.Fatal(err)
	}
	cp := doc.Checkpoints[0]
	if cp.Status != "pending" || cp.Report != "" || cp.RunID != "" || cp.Subtasks[0].Result != "" || cp.Subtasks[0].Status != "pending" || cp.Objective != "Inspect" || cp.AcceptanceCriteria[0] != "Evidence" {
		t.Fatalf("incorrect reset: %+v", cp)
	}
	if plans.plan.Document.Checkpoints[0].Status != "completed" {
		t.Fatal("source was mutated")
	}
	plans.plan.Document.Checkpoints[0].Objective = "changed"
	if _, err := v.pinnedDocument(context.Background(), p, def, "new-plan"); !errors.Is(err, ErrDenied) {
		t.Fatalf("changed source accepted: %v", err)
	}
}

// Purpose: missing concrete execution authorities must fail at construction,
// before worktree/session effects. This tests the public composition boundary.
func TestV3RuntimeRejectsMissingAuthorities(t *testing.T) {
	if _, err := NewV3Runtime(nil, nil, nil, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("constructor: %v", err)
	}
}
