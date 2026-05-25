package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionPlanHistoryEndpointReturnsSamePlanRevisionDiffs(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	sessions := pebblestore.NewSessionStore(store)
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(sessions, events)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, events, stream.NewHub(events))

	snapshot, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-plan-history-test",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Title:          "Plan History Test",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	original, _, err := sessionSvc.SavePlan(snapshot.ID, "", "Plan", "# Plan\n\n- [ ] old step", "draft", "draft", true)
	if err != nil {
		t.Fatalf("save original plan: %v", err)
	}
	_, _, err = sessionSvc.PatchPlan(snapshot.ID, sessionruntime.PlanPatchOptions{
		Patch: sessionruntime.PlanPatch{Operation: "replace_text", OldText: "old step", NewText: "new step"},
		Metadata: sessionruntime.PlanSaveMetadata{
			UpdateSummary: "small edit",
			UpdateScope:   "Plan",
			UpdateKind:    "patch",
		},
	})
	if err != nil {
		t.Fatalf("patch plan: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+snapshot.ID+"/plans/"+original.ID+"/history?limit=5", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("history status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK        bool   `json:"ok"`
		SessionID string `json:"session_id"`
		PlanID    string `json:"plan_id"`
		Count     int    `json:"count"`
		Revisions []struct {
			ID             string   `json:"id"`
			Plan           string   `json:"plan"`
			Version        int      `json:"version"`
			ParentRevision int      `json:"parent_revision"`
			UpdateSummary  string   `json:"update_summary"`
			DiffLines      []string `json:"diff_lines"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if !payload.OK || payload.SessionID != snapshot.ID || payload.PlanID != original.ID || payload.Count != 2 || len(payload.Revisions) != 2 {
		t.Fatalf("history payload identity/count = %#v", payload)
	}
	latest := payload.Revisions[0]
	if latest.ID != original.ID || latest.Version != 2 || latest.ParentRevision != 1 || latest.UpdateSummary != "small edit" {
		t.Fatalf("latest revision metadata = %#v", latest)
	}
	if !strings.Contains(latest.Plan, "new step") || len(latest.DiffLines) == 0 || !strings.Contains(strings.Join(latest.DiffLines, "\n"), "+ - [ ] new step") {
		t.Fatalf("latest revision diff/body = %#v", latest)
	}
	if payload.Revisions[1].ID != original.ID || payload.Revisions[1].Version != 1 || !strings.Contains(payload.Revisions[1].Plan, "old step") {
		t.Fatalf("base revision = %#v", payload.Revisions[1])
	}
}
