package run

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExecutePlanManagePatchUpdatesActivePlan(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	initialRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"save","title":"Plan","plan":"# Plan\n\n- [ ] old step","status":"draft","approval_state":"draft"}`, "")
	if err != nil {
		t.Fatalf("save initial plan: %v output=%s", err, initialRaw)
	}
	planID := decodePlanManageTestPlanID(t, initialRaw)

	patchedRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"patch","old_text":"old step","new_text":"new step","update_summary":"small edit","update_kind":"patch"}`, "")
	if err != nil {
		t.Fatalf("patch plan: %v output=%s", err, patchedRaw)
	}
	var payload struct {
		Action string `json:"action"`
		Plan   struct {
			ID            string   `json:"id"`
			Plan          string   `json:"plan"`
			Version       int      `json:"version"`
			UpdateSummary string   `json:"update_summary"`
			UpdateKind    string   `json:"update_kind"`
			DiffLines     []string `json:"diff_lines"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(patchedRaw), &payload); err != nil {
		t.Fatalf("decode patch payload: %v", err)
	}
	if payload.Action != "patch" {
		t.Fatalf("action = %q, want patch", payload.Action)
	}
	if payload.Plan.ID != planID {
		t.Fatalf("patched plan id = %q, want %q", payload.Plan.ID, planID)
	}
	if !strings.Contains(payload.Plan.Plan, "new step") || strings.Contains(payload.Plan.Plan, "old step") {
		t.Fatalf("patched plan body = %q", payload.Plan.Plan)
	}
	if payload.Plan.UpdateSummary != "small edit" || payload.Plan.UpdateKind != "patch" {
		t.Fatalf("metadata not persisted in payload: %#v", payload.Plan)
	}
	if len(payload.Plan.DiffLines) == 0 {
		t.Fatal("expected patch payload to include diff lines")
	}
}

func TestExecutePlanManageUpdateSectionPartialEdit(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, err := runSvc.executePlanManageTool(sessionID, `{"action":"save","title":"Plan","plan":"# Plan\n\n## Scope\n\nOld text\n\n## Next\n\nKeep me"}`, "")
	if err != nil {
		t.Fatalf("save initial plan: %v", err)
	}
	patchedRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"update_section","section":"Scope","new_text":"New text"}`, "")
	if err != nil {
		t.Fatalf("update section: %v output=%s", err, patchedRaw)
	}
	var payload struct {
		Plan struct {
			Plan string `json:"plan"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(patchedRaw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !strings.Contains(payload.Plan.Plan, "## Scope\n\nNew text") || strings.Contains(payload.Plan.Plan, "Old text") || !strings.Contains(payload.Plan.Plan, "## Next\n\nKeep me") {
		t.Fatalf("updated section body = %q", payload.Plan.Plan)
	}
}

func TestExecutePlanManageUpdateWithoutPlanRequiresPatchFields(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	_, err := runSvc.executePlanManageTool(sessionID, `{"action":"save","title":"Plan","plan":"# Plan\n"}`, "")
	if err != nil {
		t.Fatalf("save initial plan: %v", err)
	}
	_, err = runSvc.executePlanManageTool(sessionID, `{"action":"update","update_summary":"metadata only"}`, "")
	if err == nil || !strings.Contains(err.Error(), "requires edit fields") {
		t.Fatalf("metadata-only update error = %v, want clear patch-field error", err)
	}
}

func TestExecutePlanManageHistoryIncludesRevisionBodiesAndDiffs(t *testing.T) {
	runSvc, sessionSvc, cleanup := newPlanManageRunTestService(t)
	defer cleanup()

	sessionID := createPlanManageTestSession(t, sessionSvc)
	initialRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"save","title":"Plan","plan":"# Plan\n\n- [ ] old step","status":"draft","approval_state":"draft"}`, "")
	if err != nil {
		t.Fatalf("save initial plan: %v output=%s", err, initialRaw)
	}
	planID := decodePlanManageTestPlanID(t, initialRaw)
	_, err = runSvc.executePlanManageTool(sessionID, `{"action":"patch","old_text":"old step","new_text":"new step","update_summary":"small edit","update_kind":"patch"}`, "")
	if err != nil {
		t.Fatalf("patch plan: %v", err)
	}

	historyRaw, err := runSvc.executePlanManageTool(sessionID, `{"action":"history","limit":5}`, "")
	if err != nil {
		t.Fatalf("history plan: %v output=%s", err, historyRaw)
	}
	var payload struct {
		Action    string `json:"action"`
		PlanID    string `json:"plan_id"`
		Revisions []struct {
			ID             string   `json:"id"`
			Plan           string   `json:"plan"`
			Preview        string   `json:"preview"`
			Version        int      `json:"version"`
			ParentRevision int      `json:"parent_revision"`
			UpdateSummary  string   `json:"update_summary"`
			DiffLines      []string `json:"diff_lines"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal([]byte(historyRaw), &payload); err != nil {
		t.Fatalf("decode history payload: %v", err)
	}
	if payload.Action != "history" || payload.PlanID != planID {
		t.Fatalf("history identity = action %q plan %q, want history %q", payload.Action, payload.PlanID, planID)
	}
	if len(payload.Revisions) != 2 {
		t.Fatalf("revision count = %d, want 2: %s", len(payload.Revisions), historyRaw)
	}
	latest := payload.Revisions[0]
	if latest.ID != planID || latest.Version != 2 || latest.ParentRevision != 1 {
		t.Fatalf("latest revision metadata = %#v", latest)
	}
	if !strings.Contains(latest.Plan, "new step") || !strings.Contains(latest.Preview, "new step") {
		t.Fatalf("latest revision body/preview = %#v", latest)
	}
	if len(latest.DiffLines) == 0 || !strings.Contains(strings.Join(latest.DiffLines, "\n"), "+ - [ ] new step") {
		t.Fatalf("latest diff lines = %#v, want new step diff", latest.DiffLines)
	}
	if payload.Revisions[1].Version != 1 || !strings.Contains(payload.Revisions[1].Plan, "old step") || len(payload.Revisions[1].DiffLines) != 0 {
		t.Fatalf("base revision = %#v", payload.Revisions[1])
	}
}
