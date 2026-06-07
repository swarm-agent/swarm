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

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+created.ID+"/metadata", bytes.NewBufferString(`{"metadata":{"subagent":"clone","agent_name":"forbidden"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Session  pebblestore.SessionSnapshot         `json:"session"`
		Mutation pebblestore.V3SessionMutationResult `json:"mutation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode metadata response: %v", err)
	}
	if payload.Session.Metadata["subagent"] != "clone" {
		t.Fatalf("metadata = %+v", payload.Session.Metadata)
	}
	if payload.Session.Metadata["agent_name"] != "swarm" {
		t.Fatalf("protected metadata overwritten: %+v", payload.Session.Metadata)
	}
	if payload.Mutation.Event.EventType != "session.metadata.updated" {
		t.Fatalf("mutation event type = %q", payload.Mutation.Event.EventType)
	}
}

func TestSessionsV3PrimaryHydrateIncludesActivePlanAndHistory(t *testing.T) {
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
		ActivePlan    pebblestore.SessionPlanSnapshot   `json:"active_plan"`
		Revisions     []pebblestore.SessionPlanSnapshot `json:"plan_revisions"`
	}
	if err := json.Unmarshal(hydrateRec.Body.Bytes(), &hydrated); err != nil {
		t.Fatalf("decode hydrate response: %v", err)
	}
	if !hydrated.HasActivePlan || hydrated.ActivePlan.ID == "" || hydrated.ActivePlan.Plan != "# Plan v2" {
		t.Fatalf("active plan = %+v has=%v", hydrated.ActivePlan, hydrated.HasActivePlan)
	}
	if len(hydrated.Revisions) == 0 {
		t.Fatalf("plan revisions missing: %+v", hydrated.Revisions)
	}
}
