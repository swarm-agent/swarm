package session

import (
	"strings"
	"testing"
)

func TestPatchPlanReplaceTextCreatesRevision(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	original, _, err := svc.SavePlan(sessionID, "", "Plan", "# Plan\n\n- [ ] old step\n", "draft", "draft", true)
	if err != nil {
		t.Fatalf("save original plan: %v", err)
	}

	patched, _, err := svc.PatchPlan(sessionID, PlanPatchOptions{
		Patch: PlanPatch{Operation: "replace_text", OldText: "old step", NewText: "new step"},
		Metadata: PlanSaveMetadata{
			UpdateSummary: "rename step",
			UpdateScope:   "Plan",
			UpdateKind:    "patch",
		},
	})
	if err != nil {
		t.Fatalf("patch plan: %v", err)
	}
	if patched.ID != original.ID {
		t.Fatalf("patched plan id = %q, want same plan %q", patched.ID, original.ID)
	}
	if patched.Version != original.Version+1 {
		t.Fatalf("patched version = %d, want %d", patched.Version, original.Version+1)
	}
	if !strings.Contains(patched.Plan, "new step") || strings.Contains(patched.Plan, "old step") {
		t.Fatalf("patched plan body = %q", patched.Plan)
	}
	if patched.UpdateSummary != "rename step" || patched.UpdateKind != "patch" {
		t.Fatalf("metadata not persisted: %#v", patched)
	}
	if len(patched.DiffLines) == 0 {
		t.Fatal("expected diff lines on patched plan")
	}

	revisions, err := svc.ListPlanRevisions(sessionID, original.ID, 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("revision count = %d, want 2", len(revisions))
	}
	if revisions[0].ID != original.ID || revisions[0].Plan != patched.Plan || revisions[0].Version != patched.Version || len(revisions[0].DiffLines) == 0 {
		t.Fatalf("latest revision = %#v, want patched plan with diff", revisions[0])
	}
	if revisions[1].ID != original.ID || revisions[1].Plan != original.Plan || revisions[1].Version != original.Version {
		t.Fatalf("base revision = %#v, want original plan", revisions[1])
	}
}

func TestPatchPlanReplaceSectionAndCheckbox(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	plan := "# Plan\n\n## Checkpoint 1\n\n- [ ] first task\n\n## Checkpoint 2\n\nOld paragraph\n"
	original, _, err := svc.SavePlan(sessionID, "", "Plan", plan, "draft", "draft", true)
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}

	sectionPatched, _, err := svc.PatchPlan(sessionID, PlanPatchOptions{
		PlanID: original.ID,
		Patch:  PlanPatch{Operation: "replace_section", Section: "Checkpoint 2", NewText: "New paragraph\n\n- [ ] new task"},
	})
	if err != nil {
		t.Fatalf("replace section: %v", err)
	}
	if !strings.Contains(sectionPatched.Plan, "## Checkpoint 2\n\nNew paragraph\n\n- [ ] new task") || strings.Contains(sectionPatched.Plan, "Old paragraph") {
		t.Fatalf("section patch body = %q", sectionPatched.Plan)
	}

	checked := true
	checkboxPatched, _, err := svc.PatchPlan(sessionID, PlanPatchOptions{
		PlanID: original.ID,
		Patch:  PlanPatch{Operation: "set_checkbox", ChecklistItem: "first task", Checked: &checked},
	})
	if err != nil {
		t.Fatalf("set checkbox: %v", err)
	}
	if !strings.Contains(checkboxPatched.Plan, "- [x] first task") {
		t.Fatalf("checkbox patch body = %q", checkboxPatched.Plan)
	}
}

func TestPatchPlanRejectsMetadataOnlyAndAmbiguousReplace(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	_, _, err := svc.SavePlan(sessionID, "", "Plan", "# Plan\n\nsame\nsame\n", "draft", "draft", true)
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}

	_, _, err = svc.PatchPlan(sessionID, PlanPatchOptions{Metadata: PlanSaveMetadata{UpdateSummary: "metadata only"}})
	if err == nil || !strings.Contains(err.Error(), "requires at least one edit field or document") {
		t.Fatalf("metadata-only patch error = %v, want clear edit-field error", err)
	}

	_, _, err = svc.PatchPlan(sessionID, PlanPatchOptions{Patch: PlanPatch{Operation: "replace_text", OldText: "same", NewText: "different"}})
	if err == nil || !strings.Contains(err.Error(), "matched 2 times") {
		t.Fatalf("ambiguous replace error = %v, want multiple-match error", err)
	}
}
