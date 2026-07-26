package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3PrimaryMetadataUpdateUsesV3Mutation(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "metadata-create", "metadata", pebblestore.ModelPreference{})

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/metadata", bytes.NewBufferString(`{"metadata":{"subagent":"clone","swarm_v3_desktop_sidebar_pinned":true,"agent_name":"forbidden"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Session        *pebblestore.SessionSnapshot        `json:"session"`
		Metadata       map[string]any                      `json:"metadata"`
		Mutation       pebblestore.V3SessionMutationResult `json:"mutation"`
		RealtimeOutbox any                                 `json:"realtime_outbox"`
		Messages       []pebblestore.MessageSnapshot       `json:"messages"`
		Events         []pebblestore.V3SessionEvent        `json:"events"`
		WorksetID      string                              `json:"workset_id"`
		Worksets       []any                               `json:"worksets"`
		Subscriptions  []any                               `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode metadata response: %v", err)
	}
	if payload.Metadata["subagent"] != "clone" {
		t.Fatalf("metadata = %+v", payload.Metadata)
	}
	if payload.Metadata["swarm_v3_desktop_sidebar_pinned"] != true {
		t.Fatalf("desktop sidebar pin metadata missing: %+v", payload.Metadata)
	}
	if payload.Metadata["agent_name"] != "swarm" {
		t.Fatalf("protected metadata overwritten: %+v", payload.Metadata)
	}
	if payload.Mutation.Event.EventType != "session.metadata.updated" {
		t.Fatalf("mutation event type = %q", payload.Mutation.Event.EventType)
	}
	if payload.RealtimeOutbox == nil {
		t.Fatalf("metadata mutation response missing realtime_outbox: %s", rec.Body.String())
	}
	if payload.Session != nil || payload.Messages != nil || payload.Events != nil || payload.WorksetID != "" || payload.Worksets != nil || payload.Subscriptions != nil {
		t.Fatalf("metadata mutation should return metadata delta only, got body=%s", rec.Body.String())
	}
}

func TestSessionsV3PrimaryHydrateOmitsActivePlanAndHistory(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "plan-create", "plan", pebblestore.ModelPreference{})

	for _, body := range []string{
		`{"title":"Current Plan","plan":"# Plan v1"}`,
		`{"id":"plan-1","title":"Current Plan","plan":"# Plan v2"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/plans", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		if rec.Code != http.StatusOK {
			t.Fatalf("plan save status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	hydrateReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID, nil)
	hydrateRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(hydrateRec, withTestPrincipal(hydrateReq))
	if hydrateRec.Code != http.StatusOK {
		t.Fatalf("hydrate status = %d, want %d, body=%s", hydrateRec.Code, http.StatusOK, hydrateRec.Body.String())
	}

	var hydrated struct {
		HasActivePlan bool                              `json:"has_active_plan"`
		ActivePlan    *pebblestore.SessionPlanSnapshot  `json:"active_plan"`
		Revisions     []pebblestore.SessionPlanSnapshot `json:"plan_revisions"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if hydrated.HasActivePlan || hydrated.ActivePlan != nil || len(hydrated.Revisions) != 0 {
		t.Fatalf("routine hydrate should omit plan payloads, got active=%+v has=%v revisions=%d", hydrated.ActivePlan, hydrated.HasActivePlan, len(hydrated.Revisions))
	}

	activeReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID+"/plans/active", nil)
	activeRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(activeRec, withTestPrincipal(activeReq))
	if activeRec.Code != http.StatusOK {
		t.Fatalf("active plan status = %d, want %d, body=%s", activeRec.Code, http.StatusOK, activeRec.Body.String())
	}
	var active struct {
		HasActivePlan bool                            `json:"has_active"`
		ActivePlan    pebblestore.SessionPlanSnapshot `json:"active_plan"`
	}
	if err := json.Unmarshal(activeRec.Body.Bytes(), &active); err != nil {
		t.Fatalf("decode active plan response: %v", err)
	}
	if !active.HasActivePlan || !active.ActivePlan.Active || active.ActivePlan.ID == "" || active.ActivePlan.Plan != "# Plan v2" {
		t.Fatalf("active plan dedicated response = %+v has=%v", active.ActivePlan, active.HasActivePlan)
	}

	historyReq := httptest.NewRequest(http.MethodGet, "/v3/sessions/"+created.ID+"/plans/"+active.ActivePlan.ID+"/history?limit=100", nil)
	historyRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(historyRec, withTestPrincipal(historyReq))
	if historyRec.Code != http.StatusOK {
		t.Fatalf("plan history status = %d, want %d, body=%s", historyRec.Code, http.StatusOK, historyRec.Body.String())
	}
	var history struct {
		Revisions []pebblestore.SessionPlanSnapshot `json:"revisions"`
	}
	if err := json.Unmarshal(historyRec.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if len(history.Revisions) == 0 {
		t.Fatalf("plan revisions missing from dedicated history response: %+v", history.Revisions)
	}
}

func TestSessionsV3PrimaryPlanSaveReturnsPlanDeltaOnly(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSessionWithPreference(t, server, "plan-save-create", "plan save", pebblestore.ModelPreference{})

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/plans", bytes.NewBufferString(`{"title":"Current Plan","plan":"# Plan"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("plan save status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Plan                pebblestore.SessionPlanSnapshot     `json:"plan"`
		Mutation            pebblestore.V3SessionMutationResult `json:"mutation"`
		RealtimeOutbox      *pebblestore.V3RealtimeOutboxRecord `json:"realtime_outbox"`
		Session             *pebblestore.SessionSnapshot        `json:"session"`
		Messages            []pebblestore.MessageSnapshot       `json:"messages"`
		PlanRevisions       []pebblestore.SessionPlanSnapshot   `json:"plan_revisions"`
		PlanRevisionsBySess map[string]any                      `json:"plan_revisions_by_session"`
		PlansBySess         map[string]any                      `json:"plans_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode plan save response: %v", err)
	}
	if payload.Plan.ID == "" || payload.Plan.Plan != "# Plan" {
		t.Fatalf("plan save response plan = %+v", payload.Plan)
	}
	if payload.Mutation.Event.EventType != "session.plan.saved" || payload.RealtimeOutbox == nil {
		t.Fatalf("plan save must return its canonical V3 event and realtime outbox: %s", rec.Body.String())
	}
	if payload.Session != nil || payload.Messages != nil || payload.PlanRevisions != nil || payload.PlanRevisionsBySess != nil || payload.PlansBySess != nil {
		t.Fatalf("plan save should return changed plan delta only, got body=%s", rec.Body.String())
	}
}
