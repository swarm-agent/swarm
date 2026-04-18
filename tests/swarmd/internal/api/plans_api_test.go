package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionPlansAPIEndToEnd(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "plans-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	hub := stream.NewHub(eventLog)
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	server := NewServer("test", nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, eventLog, hub)
	handler := server.Handler()

	session := createSessionViaAPI(t, handler, t.TempDir())

	saveResp := struct {
		OK        bool                            `json:"ok"`
		SessionID string                          `json:"session_id"`
		Plan      pebblestore.SessionPlanSnapshot `json:"plan"`
	}{}
	status := doJSONRequest(t, handler, http.MethodPost, fmt.Sprintf("/v1/sessions/%s/plans", session.ID), map[string]any{
		"plan_id":  "plan_alpha",
		"title":    "Alpha Plan",
		"plan":     "# Alpha\n\n- [ ] step",
		"status":   "draft",
		"activate": true,
	}, &saveResp)
	if status != http.StatusOK {
		t.Fatalf("save plan status=%d", status)
	}
	if saveResp.Plan.ID != "plan_alpha" {
		t.Fatalf("expected plan id plan_alpha, got %q", saveResp.Plan.ID)
	}

	listResp := struct {
		OK           bool                              `json:"ok"`
		SessionID    string                            `json:"session_id"`
		ActivePlanID string                            `json:"active_plan_id"`
		Plans        []pebblestore.SessionPlanSnapshot `json:"plans"`
	}{}
	status = doJSONRequest(t, handler, http.MethodGet, fmt.Sprintf("/v1/sessions/%s/plans", session.ID), nil, &listResp)
	if status != http.StatusOK {
		t.Fatalf("list plans status=%d", status)
	}
	if len(listResp.Plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(listResp.Plans))
	}
	if listResp.ActivePlanID != "plan_alpha" {
		t.Fatalf("expected active plan id plan_alpha, got %q", listResp.ActivePlanID)
	}

	activeResp := struct {
		OK         bool                            `json:"ok"`
		SessionID  string                          `json:"session_id"`
		HasActive  bool                            `json:"has_active"`
		ActivePlan pebblestore.SessionPlanSnapshot `json:"active_plan"`
	}{}
	status = doJSONRequest(t, handler, http.MethodGet, fmt.Sprintf("/v1/sessions/%s/plans/active", session.ID), nil, &activeResp)
	if status != http.StatusOK {
		t.Fatalf("get active plan status=%d", status)
	}
	if !activeResp.HasActive {
		t.Fatalf("expected has_active=true")
	}
	if activeResp.ActivePlan.ID != "plan_alpha" {
		t.Fatalf("expected active plan plan_alpha, got %q", activeResp.ActivePlan.ID)
	}
}
