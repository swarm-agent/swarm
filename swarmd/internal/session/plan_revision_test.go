package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanRevisionHistoryIncludesCurrentAndPriorDiffs(t *testing.T) {
	svc, cleanup := newPlanTestService(t)
	defer cleanup()

	sessionID := createPlanTestSession(t, svc)
	original, _, err := svc.SavePlan(sessionID, "", "Plan", "# Plan\n\n- [ ] first step\n", "draft", "draft", true)
	if err != nil {
		t.Fatalf("save original plan: %v", err)
	}

	second, _, err := svc.PatchPlan(sessionID, PlanPatchOptions{
		Patch: PlanPatch{Operation: "replace_text", OldText: "first step", NewText: "second step"},
		Metadata: PlanSaveMetadata{
			UpdateSummary: "second revision",
			UpdateScope:   "Plan",
			UpdateKind:    "patch",
		},
	})
	if err != nil {
		t.Fatalf("patch second revision: %v", err)
	}
	third, _, err := svc.PatchPlan(sessionID, PlanPatchOptions{
		PlanID: original.ID,
		Patch:  PlanPatch{Operation: "append_text", Section: "Plan", Text: "- [ ] third step"},
		Metadata: PlanSaveMetadata{
			UpdateSummary: "third revision",
			UpdateScope:   "Plan",
			UpdateKind:    "patch",
		},
	})
	if err != nil {
		t.Fatalf("patch third revision: %v", err)
	}

	revisions, err := svc.ListPlanRevisions(sessionID, original.ID, 10)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 3 {
		t.Fatalf("revision count = %d, want 3: %#v", len(revisions), revisions)
	}
	gotVersions := []int{revisions[0].Version, revisions[1].Version, revisions[2].Version}
	if !reflect.DeepEqual(gotVersions, []int{3, 2, 1}) {
		t.Fatalf("revision versions = %v, want [3 2 1]", gotVersions)
	}
	if revisions[0].ID != original.ID || revisions[1].ID != original.ID || revisions[2].ID != original.ID {
		t.Fatalf("revision plan IDs = %q %q %q, want all %q", revisions[0].ID, revisions[1].ID, revisions[2].ID, original.ID)
	}
	if revisions[0].Plan != third.Plan || revisions[0].ParentRevision != second.Version {
		t.Fatalf("latest revision = %#v, want current third revision with parent %d", revisions[0], second.Version)
	}
	if revisions[1].Plan != second.Plan || revisions[1].PriorPlan != original.Plan || revisions[1].ParentRevision != original.Version {
		t.Fatalf("second revision = %#v, want diff from original", revisions[1])
	}
	if revisions[2].Plan != original.Plan || revisions[2].ParentRevision != 0 || len(revisions[2].DiffLines) != 0 {
		t.Fatalf("first revision = %#v, want original base revision", revisions[2])
	}
	if !diffLinesContain(revisions[0].DiffLines, "+ - [ ] third step") {
		t.Fatalf("latest diff lines = %#v, want appended step diff", revisions[0].DiffLines)
	}
	if !diffLinesContain(revisions[1].DiffLines, "- - [ ] first step") || !diffLinesContain(revisions[1].DiffLines, "+ - [ ] second step") {
		t.Fatalf("second diff lines = %#v, want first-to-second diff", revisions[1].DiffLines)
	}
}

func diffLinesContain(lines []string, needle string) bool {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}
