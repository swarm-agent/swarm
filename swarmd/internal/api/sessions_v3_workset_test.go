package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3WorksetEndpointSupportsPaginationAndManifests(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	createdA := createSessionsV3PrimaryTestSession(t, server, "workset-a", "Workset A")
	createdB := createSessionsV3PrimaryTestSession(t, server, "workset-b", "Workset B")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdB.ID, "first")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, createdB.ID, "second")

	body := `{"session_ids":["` + createdB.ID + `"],"recent":{"limit":1},"history":{"mode":"full","max_messages_per_session":1,"manifest_policy":"manifest"},"response_budget":{"allow_manifest":true}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("workset status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK                        bool                                                     `json:"ok"`
		SessionsByID              map[string]pebblestore.SessionSnapshot                   `json:"sessions_by_id"`
		HistoryManifestsBySession map[string][]pebblestore.V3SessionHistoryChunkDescriptor `json:"history_manifests_by_session"`
		Omissions                 []pebblestore.V3SessionWorksetOmission                   `json:"omissions"`
		Pagination                pebblestore.V3SessionWorksetPagination                   `json:"pagination"`
		SessionOrder              []string                                                 `json:"session_order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode workset response: %v", err)
	}
	_ = createdA
	if !payload.OK || payload.SessionsByID[createdB.ID].ID != createdB.ID {
		t.Fatalf("sessions_by_id = %+v", payload.SessionsByID)
	}
	if len(payload.HistoryManifestsBySession[createdB.ID]) == 0 || len(payload.Omissions) == 0 {
		t.Fatalf("manifest/omissions missing: %+v %+v", payload.HistoryManifestsBySession, payload.Omissions)
	}
	if !payload.Pagination.HasMore || payload.Pagination.NextBeforeUpdatedAt == nil || payload.Pagination.NextBeforeSessionID == "" {
		t.Fatalf("pagination = %+v", payload.Pagination)
	}
	if len(payload.SessionOrder) != 1 || payload.SessionOrder[0] != createdB.ID {
		t.Fatalf("session_order = %+v", payload.SessionOrder)
	}
}

func TestSessionsV3WorksetEndpointRejectsBudgetWithoutManifest(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "workset-budget", "Workset Budget")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "large budget failure message")

	body := `{"session_ids":["` + created.ID + `"],"history":{"mode":"full","manifest_policy":"error"},"response_budget":{"max_bytes":20}}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:workset", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("workset budget status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func appendSessionsV3PrimaryMessageForWorksetTest(t *testing.T, server *Server, sessionID, content string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"client_request_id":"`+sessionID+content+`","role":"user","content":"`+content+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("append message status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
