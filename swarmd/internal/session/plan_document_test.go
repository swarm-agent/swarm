package session

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestNormalizePlanDocumentForSaveFillsPlanIdentityAndValidatesStructure(t *testing.T) {
	doc, err := NormalizePlanDocumentForSave("plan-one", "One Plan", &pebblestore.SessionPlanDocument{
		Info: pebblestore.SessionPlanInfo{
			Goal:          " ship the model ",
			Decisions:     []string{" use one plan object ", ""},
			RelevantFiles: []string{" swarmd/internal/session/plan_document.go "},
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: " cp-1 ", Title: " Model ", Tasks: []string{" add document field ", ""}},
		},
		ActiveCheckpointID: " cp-1 ",
	}, nil)
	if err != nil {
		t.Fatalf("normalize document: %v", err)
	}
	if doc.ID != "plan-one" || doc.Title != "One Plan" {
		t.Fatalf("document identity = %q/%q, want plan identity", doc.ID, doc.Title)
	}
	if doc.ActiveCheckpointID != "cp-1" || doc.Checkpoints[0].ID != "cp-1" || doc.Checkpoints[0].Order != 1 {
		t.Fatalf("checkpoint normalization failed: %#v", doc)
	}
	if len(doc.Info.Decisions) != 1 || doc.Info.Decisions[0] != "use one plan object" || doc.Info.RelevantFiles[0] != "swarmd/internal/session/plan_document.go" {
		t.Fatalf("plan info normalization failed: %#v", doc.Info)
	}
}

func TestValidatePlanDocumentRejectsStructuralInconsistency(t *testing.T) {
	_, err := NormalizePlanDocumentForSave("plan-one", "One Plan", &pebblestore.SessionPlanDocument{
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}, {ID: "cp-1"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate checkpoint error = %v, want duplicate rejection", err)
	}

	_, err = NormalizePlanDocumentForSave("plan-one", "One Plan", &pebblestore.SessionPlanDocument{
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}},
		ActiveCheckpointID: "missing",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "active_checkpoint_id") {
		t.Fatalf("bad active checkpoint error = %v, want active checkpoint rejection", err)
	}

	_, err = NormalizePlanDocumentForSave("plan-one", "One Plan", &pebblestore.SessionPlanDocument{
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{Title: "missing id"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "requires id") {
		t.Fatalf("missing checkpoint id error = %v, want id rejection", err)
	}
}

func TestSavePlanPreservesExistingDocumentWhenNoIncomingDocument(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	initialDoc := &pebblestore.SessionPlanDocument{
		ID:            "plan-one",
		Title:         "One Plan",
		SchemaVersion: "session-plan-document/v1",
		Info: pebblestore.SessionPlanInfo{
			Goal:               "Implement one plan model",
			ValidationStrategy: "go test ./internal/session",
		},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Objective: "Add structured document", Tasks: []string{"add model"}, AcceptanceCriteria: []string{"document persists"}},
		},
		ActiveCheckpointID: "cp-1",
	}
	first, _, err := svc.SavePlanWithMetadata(sessionID, "plan-one", "One Plan", "# Plan", "draft", "draft", true, PlanSaveMetadata{Document: initialDoc})
	if err != nil {
		t.Fatalf("save initial plan document: %v", err)
	}
	if first.Document == nil || first.Document.Info.Goal != "Implement one plan model" {
		t.Fatalf("initial document not stored: %#v", first.Document)
	}

	initialDoc.Info.Goal = "mutated caller copy"
	updated, _, err := svc.SavePlanWithMetadata(sessionID, "plan-one", "One Plan", "# Plan\n\nText revision", "draft", "draft", true, PlanSaveMetadata{UpdateSummary: "markdown display update"})
	if err != nil {
		t.Fatalf("save revision without document: %v", err)
	}
	if updated.Document == nil {
		t.Fatal("document was dropped on revision without incoming document")
	}
	if updated.Document.Info.Goal != "Implement one plan model" {
		t.Fatalf("document goal = %q, want preserved stored document", updated.Document.Info.Goal)
	}
	if updated.Version != first.Version+1 || updated.ParentRevision != first.Version {
		t.Fatalf("revision linkage = version %d parent %d, want normal plan revision", updated.Version, updated.ParentRevision)
	}
	if updated.Document.RevisionID != "plan-one:v2" {
		t.Fatalf("document revision id = %q, want plan-one:v2", updated.Document.RevisionID)
	}

	revisions, err := svc.ListPlanRevisions(sessionID, "plan-one", 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 2 || revisions[0].Document == nil || revisions[1].Document == nil {
		t.Fatalf("revision documents not retained: %#v", revisions)
	}
}

func TestSavePlanRejectsInvalidIncomingDocument(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	_, _, err := svc.SavePlanWithMetadata(sessionID, "plan-one", "One Plan", "# Plan", "draft", "draft", true, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Checkpoints:        []pebblestore.SessionPlanCheckpoint{{ID: "cp-1"}},
		ActiveCheckpointID: "cp-missing",
	}})
	if err == nil || !strings.Contains(err.Error(), "active_checkpoint_id") {
		t.Fatalf("save invalid document error = %v, want validation error", err)
	}
}

func TestStartNewPlanAcceptsInitialDocument(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	created, _, err := svc.StartNewPlan(sessionID, "Structured Start", StartNewPlanOptions{Document: &pebblestore.SessionPlanDocument{
		ID:          "structured-start",
		Info:        pebblestore.SessionPlanInfo{Goal: "structured from creation"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Model"}},
	}})
	if err != nil {
		t.Fatalf("start plan with document: %v", err)
	}
	if created.ID != "structured-start" || created.Document == nil || created.Document.ID != created.ID || created.Document.Title != "Structured Start" || created.Document.Info.Goal != "structured from creation" {
		t.Fatalf("created document = %#v for plan %#v", created.Document, created)
	}
	if created.Document.RevisionID != "structured-start:v1" {
		t.Fatalf("created document revision id = %q", created.Document.RevisionID)
	}
}

func TestPatchPlanCanCreateDocumentOnlyRevision(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	first, _, err := svc.SavePlan(sessionID, "plan-one", "One Plan", "# Plan", "draft", "draft", true)
	if err != nil {
		t.Fatalf("save base plan: %v", err)
	}
	updated, _, err := svc.PatchPlan(sessionID, PlanPatchOptions{
		PlanID: first.ID,
		Document: &pebblestore.SessionPlanDocument{
			Info:        pebblestore.SessionPlanInfo{Goal: "document-only modular edit"},
			Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Model"}},
		},
		Metadata: PlanSaveMetadata{UpdateSummary: "document edit", UpdateScope: "plan info", UpdateKind: "document_update"},
	})
	if err != nil {
		t.Fatalf("patch document-only revision: %v", err)
	}
	if updated.Plan != first.Plan {
		t.Fatalf("document-only patch changed rendered plan = %q, want %q", updated.Plan, first.Plan)
	}
	if updated.Document == nil || updated.Document.Info.Goal != "document-only modular edit" {
		t.Fatalf("document-only patch document = %#v", updated.Document)
	}
	if updated.Version != first.Version+1 || updated.ParentRevision != first.Version {
		t.Fatalf("revision linkage = version %d parent %d", updated.Version, updated.ParentRevision)
	}
}

func TestSetActivePlanDoesNotCreateRevisionOrMutateDocument(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	created, _, err := svc.SavePlanWithMetadata(sessionID, "plan-one", "One Plan", "# Plan", "draft", "draft", false, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info:        pebblestore.SessionPlanInfo{Goal: "preserve active switch"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Model"}},
	}})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	activated, _, err := svc.SetActivePlan(sessionID, created.ID)
	if err != nil {
		t.Fatalf("set active plan: %v", err)
	}
	if activated.Version != created.Version || activated.Document == nil || activated.Document.Info.Goal != "preserve active switch" {
		t.Fatalf("activated plan mutated revision/document: %#v", activated)
	}
	revisions, err := svc.ListPlanRevisions(sessionID, created.ID, 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("revision count after activation = %d, want 1", len(revisions))
	}
}

func TestApplyPlanDocumentPatchBatchCreatesOneRevision(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	first, _, err := svc.SavePlanWithMetadata(sessionID, "plan-one", "One Plan", "# Plan", "draft", "draft", true, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info: pebblestore.SessionPlanInfo{Goal: "initial goal"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Model", Status: "active"},
			{ID: "cp-2", Title: "API", Status: "pending"},
		},
		ActiveCheckpointID: "cp-1",
	}})
	if err != nil {
		t.Fatalf("save structured plan: %v", err)
	}

	updated, _, err := svc.PatchPlan(sessionID, PlanPatchOptions{
		PlanID: first.ID,
		DocumentPatch: &PlanDocumentPatch{Operations: []PlanDocumentPatchOperation{
			{Operation: "update_info", Info: &pebblestore.SessionPlanInfo{Goal: "modular one-plan system", Decisions: []string{"structured document is canonical"}}},
			{Operation: "complete_checkpoint", CheckpointID: "cp-1", Report: "model complete", ChangedFiles: []string{"swarmd/internal/session/plan_document.go"}, Validation: []string{"go test ./internal/session"}},
			{Operation: "set_active_checkpoint", ActiveCheckpointID: "cp-2"},
			{Operation: "reorder_checkpoints", CheckpointOrder: []string{"cp-2", "cp-1"}},
		}},
		Metadata: PlanSaveMetadata{UpdateSummary: "batch modular plan update", UpdateScope: "document+checkpoints", UpdateKind: "document_patch"},
	})
	if err != nil {
		t.Fatalf("patch document batch: %v", err)
	}
	if updated.Version != first.Version+1 || updated.ParentRevision != first.Version {
		t.Fatalf("revision linkage = version %d parent %d", updated.Version, updated.ParentRevision)
	}
	if updated.Document == nil || updated.Document.Info.Goal != "modular one-plan system" || updated.Document.ActiveCheckpointID != "cp-2" {
		t.Fatalf("updated document = %#v", updated.Document)
	}
	if updated.Document.Checkpoints[0].ID != "cp-2" || updated.Document.Checkpoints[0].Order != 1 || updated.Document.Checkpoints[1].Status != "done" || updated.Document.Checkpoints[1].Report != "model complete" {
		t.Fatalf("checkpoint patch/order failed: %#v", updated.Document.Checkpoints)
	}
	revisions, err := svc.ListPlanRevisions(sessionID, first.ID, 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("revision count = %d, want one archived revision plus current", len(revisions))
	}
}

func TestApplyPlanDocumentPatchIsAtomicOnInvalidBatch(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	first, _, err := svc.SavePlanWithMetadata(sessionID, "plan-one", "One Plan", "# Plan", "draft", "draft", true, PlanSaveMetadata{Document: &pebblestore.SessionPlanDocument{
		Info:        pebblestore.SessionPlanInfo{Goal: "initial goal"},
		Checkpoints: []pebblestore.SessionPlanCheckpoint{{ID: "cp-1", Title: "Model"}},
	}})
	if err != nil {
		t.Fatalf("save structured plan: %v", err)
	}

	_, _, err = svc.PatchPlan(sessionID, PlanPatchOptions{
		PlanID: first.ID,
		DocumentPatch: &PlanDocumentPatch{Operations: []PlanDocumentPatchOperation{
			{Operation: "update_info", Info: &pebblestore.SessionPlanInfo{Goal: "should not persist"}},
			{Operation: "complete_checkpoint", CheckpointID: "missing"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("invalid batch error = %v, want missing checkpoint", err)
	}
	current, ok, err := svc.GetPlan(sessionID, first.ID)
	if err != nil || !ok {
		t.Fatalf("get current plan: ok=%v err=%v", ok, err)
	}
	if current.Version != first.Version || current.Document.Info.Goal != "initial goal" {
		t.Fatalf("invalid batch mutated current plan: %#v", current)
	}
}
