package session

import (
	"encoding/json"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: card requirements must remain exact executable criteria, and localized
// edits must preserve other requirements and technical work. ApplyPlanDocumentPatch
// is the narrowest atomic transformation boundary; failures must not mutate input.
// Snapshot serialized input directly: the production clone normalizes nil attempt
// slices and therefore is not an independent mutation oracle.
func TestRequirementEditsPreserveScopeAndRejectStale(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{ID: "p", Title: "Settings", RevisionID: "p:v2",
		Requirements: []pebblestore.SessionPlanRequirement{{ID: "r1", Text: "Save preferences", CheckpointID: "cp"}, {ID: "r2", Text: "Keep keyboard access", CheckpointID: "cp"}},
		Checkpoints:  []pebblestore.SessionPlanCheckpoint{{ID: "cp", Title: "Settings", Status: "pending", Notes: "Keep technical implementation", AcceptanceCriteria: []string{"Save preferences", "Keep keyboard access"}}},
	}
	patch := PlanDocumentPatch{BaseRevisionID: "p:v2", Operation: "edit_requirement", RequirementID: "r1", Requirement: &pebblestore.SessionPlanRequirement{ID: "r1", Text: "Save preferences automatically", CheckpointID: "cp"}}
	before, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		after, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatal("input was mutated")
		}
	}
	got, err := ApplyPlanDocumentPatch("p", "Settings", doc, patch)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
	if got.Requirements[1] != doc.Requirements[1] || got.Checkpoints[0].Notes != doc.Checkpoints[0].Notes {
		t.Fatal("unrelated scope changed")
	}
	if err := validatePlanRequirements(got); err != nil {
		t.Fatal(err)
	}
	if len(got.RequirementChanges) != 1 {
		t.Fatal("missing concise change summary")
	}
	patch.BaseRevisionID = "p:v1"
	if _, err := ApplyPlanDocumentPatch("p", "Settings", doc, patch); err == nil {
		t.Fatal("stale revision accepted")
	}
	patch.BaseRevisionID = "p:v2"
	patch.Requirement.CheckpointID = "missing"
	if _, err := ApplyPlanDocumentPatch("p", "Settings", doc, patch); err == nil {
		t.Fatal("detached requirement accepted")
	}
	assertUnchanged()
	got.Checkpoints[0].AcceptanceCriteria = nil
	if err := ValidateExecutablePlanDocument(got); err == nil {
		t.Fatal("approval validation accepted inconsistent scope")
	}
}

// Purpose: stable identities survive add/remove/reorder without a full-plan
// replacement; duplicate order entries must fail atomically at the patch boundary.
func TestRequirementEditBatch(t *testing.T) {
	doc := &pebblestore.SessionPlanDocument{ID: "p", Title: "Settings", RevisionID: "p:v1", Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp", Title: "Settings", Status: "pending"}}}
	patch := PlanDocumentPatch{BaseRevisionID: "p:v1", Operations: []PlanDocumentPatch{
		{Operation: "add_requirement", Requirement: &pebblestore.SessionPlanRequirement{ID: "a", Text: "Save settings", CheckpointID: "cp"}},
		{Operation: "add_requirement", Requirement: &pebblestore.SessionPlanRequirement{ID: "b", Text: "Restore settings", CheckpointID: "cp"}},
		{Operation: "reorder_requirements", RequirementOrder: []string{"b", "a"}},
		{Operation: "remove_requirement", RequirementID: "a"},
	}}
	got, err := ApplyPlanDocumentPatch("p", "Settings", doc, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Requirements) != 1 || got.Requirements[0].ID != "b" || !reflect.DeepEqual(got.Checkpoints[0].AcceptanceCriteria, []string{"Restore settings"}) {
		t.Fatalf("wrong scope: %+v", got)
	}
	patch.Operations[2].RequirementOrder = []string{"a", "a"}
	if _, err := ApplyPlanDocumentPatch("p", "Settings", doc, patch); err == nil {
		t.Fatal("duplicate order accepted")
	}
	if len(doc.Requirements) != 0 || len(doc.Checkpoints[0].AcceptanceCriteria) != 0 {
		t.Fatal("partial batch leaked")
	}
}

// Purpose: targeted republishing must rotate the exact task approval binding and
// reject stale writers without changing durable state. The real lifecycle/store
// boundary is required to prove this, rather than a UI status assertion.
func TestRequirementPublicationRevisionGuard(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()
	workspace := t.TempDir()
	if err := svc.Store().PutProject("account", &pebblestore.ProjectRecord{ID: "project", AccountID: "account", Name: "Requirements", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: workspace, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store().PutProjectTask("account", &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", AccountID: "account", Title: "Preferences", Agent: "swarm", Status: "planning", WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
	lifecycle := NewPlanLifecycleService(svc)
	input := ProjectTaskPlanSubmissionInput{AccountScopeID: "account", UserID: "user", ProjectID: "project", TaskID: "task", WorkspacePath: workspace, Document: &pebblestore.SessionPlanDocument{ID: "plan", Title: "Preferences", Info: pebblestore.SessionPlanInfo{Goal: "Save preferences"}, Requirements: []pebblestore.SessionPlanRequirement{{ID: "save", Text: "Save preferences", CheckpointID: "cp"}}, Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp", Title: "Preferences", Tasks: []string{"Implement preference storage"}, Status: "pending", Order: 1, AcceptanceCriteria: []string{"Save preferences"}}}}}
	first, err := lifecycle.SubmitProjectTaskStructuredPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	patch := PlanDocumentPatch{BaseRevisionID: first.Plan.Document.RevisionID, Operation: "edit_requirement", RequirementID: "save", Requirement: &pebblestore.SessionPlanRequirement{ID: "save", Text: "Save preferences automatically", CheckpointID: "cp"}}
	input.Document, err = ApplyPlanDocumentPatch("plan", "Preferences", first.Plan.Document, patch)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevisionID = patch.BaseRevisionID
	second, err := lifecycle.SubmitProjectTaskStructuredPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Task.PlanBinding.DefinitionRevision != first.Task.PlanBinding.DefinitionRevision+1 || second.Receipt == first.Receipt || second.Plan.ApprovalState == "approved" {
		t.Fatal("old approval binding survived edit")
	}
	if _, err := lifecycle.SubmitProjectTaskStructuredPlan(input); err == nil {
		t.Fatal("stale publication accepted")
	}
	stored, found, err := svc.GetPlan(second.Plan.SessionID, "plan")
	if err != nil || !found || stored.Version != second.Plan.Version || !reflect.DeepEqual(stored.Document.Requirements, second.Plan.Document.Requirements) {
		t.Fatal("stale publication changed durable state")
	}
}
