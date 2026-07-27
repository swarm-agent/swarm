package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV3SearchEndpointRecentMetadataOnlyAndLimitCap(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	created := createSessionsV3PrimaryTestSession(t, server, "search-recent-a", "Search Recent A")
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, created.ID, "hello search recent")

	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:search", bytes.NewBufferString(`{"global":true,"limit":500}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK         bool                              `json:"ok"`
		Items      []pebblestore.V3SessionSearchItem `json:"items"`
		Pagination struct {
			HasMore bool `json:"has_more"`
		} `json:"pagination"`
		MessagesBySession map[string][]pebblestore.MessageSnapshot `json:"messages_by_session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if !payload.OK || len(payload.Items) == 0 || payload.Items[0].ID != created.ID || payload.Items[0].MessageCount != 1 {
		t.Fatalf("search payload = %+v", payload)
	}
	if len(payload.Items) > 50 {
		t.Fatalf("limit cap not enforced: %d items", len(payload.Items))
	}
	if payload.MessagesBySession != nil {
		t.Fatalf("search response must not hydrate messages: %+v", payload.MessagesBySession)
	}
}

func TestSessionsV3SearchArchivedSessionAppendReactivates(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	archived := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "search-reactivate", "Reactivate Needle", workspace)
	archiveReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:archive", strings.NewReader(`{"session_ids":["`+archived.ID+`"]}`))
	archiveReq.Header.Set("Content-Type", "application/json")
	archiveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(archiveRec, withTestPrincipal(archiveReq))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	searchReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:search", strings.NewReader(`{"query":"needle","archived_mode":"only","global":true,"limit":50}`))
	searchReq.Header.Set("Content-Type", "application/json")
	searchRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(searchRec, withTestPrincipal(searchReq))
	if searchRec.Code != http.StatusOK {
		t.Fatalf("search archived status = %d body=%s", searchRec.Code, searchRec.Body.String())
	}
	var searchPayload struct {
		Items []pebblestore.V3SessionSearchItem `json:"items"`
	}
	if err := json.Unmarshal(searchRec.Body.Bytes(), &searchPayload); err != nil {
		t.Fatalf("decode archived search: %v", err)
	}
	if len(searchPayload.Items) != 1 || searchPayload.Items[0].ID != archived.ID || !searchPayload.Items[0].Archived {
		t.Fatalf("archived search items = %+v", searchPayload.Items)
	}

	messageReq := httptest.NewRequest(http.MethodPost, "/v3/sessions/"+archived.ID+"/messages", strings.NewReader(`{"client_request_id":"reactivate-archived","message_id":"reactivate-archived-message","run_id":"reactivate-archived-run","role":"user","content":"new active message"}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(messageRec, withTestPrincipal(messageReq))
	if messageRec.Code != http.StatusOK {
		t.Fatalf("append archived message status = %d body=%s", messageRec.Code, messageRec.Body.String())
	}
	var messagePayload struct {
		Session    pebblestore.SessionSnapshot     `json:"session"`
		Projection pebblestore.V3SessionProjection `json:"projection"`
	}
	if err := json.Unmarshal(messageRec.Body.Bytes(), &messagePayload); err != nil {
		t.Fatalf("decode message response: %v", err)
	}
	if messagePayload.Session.ID != archived.ID || messagePayload.Session.MessageCount != 1 || messagePayload.Projection.LastEventSeq == 0 {
		t.Fatalf("message response session=%+v projection=%+v", messagePayload.Session, messagePayload.Projection)
	}

	activeReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:search", strings.NewReader(`{"query":"active","archived_mode":"exclude","global":true,"limit":50}`))
	activeReq.Header.Set("Content-Type", "application/json")
	activeRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(activeRec, withTestPrincipal(activeReq))
	if activeRec.Code != http.StatusOK {
		t.Fatalf("search active status = %d body=%s", activeRec.Code, activeRec.Body.String())
	}
	if err := json.Unmarshal(activeRec.Body.Bytes(), &searchPayload); err != nil {
		t.Fatalf("decode active search: %v", err)
	}
	if len(searchPayload.Items) != 1 || searchPayload.Items[0].ID != archived.ID || searchPayload.Items[0].Archived || searchPayload.Items[0].MessageCount != 1 {
		t.Fatalf("reactivated search items = %+v", searchPayload.Items)
	}
}

func TestSessionsV3SearchEndpointQueryArchivedDateWorkspaceFilters(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	active := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "search-active", "Needle Active", workspaceA)
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, active.ID, "message needle")
	otherWorkspace := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "search-other", "Needle Other", workspaceB)
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, otherWorkspace.ID, "message needle")
	archived := createSessionsV3PrimaryTestSessionWithWorkspace(t, server, "search-archived", "Needle Archived", workspaceA)
	appendSessionsV3PrimaryMessageForWorksetTest(t, server, archived.ID, "archived needle")
	archiveReq := httptest.NewRequest(http.MethodPost, "/v3/sessions:archive", strings.NewReader(`{"session_ids":["`+archived.ID+`"]}`))
	archiveReq.Header.Set("Content-Type", "application/json")
	archiveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(archiveRec, withTestPrincipal(archiveReq))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	body := `{"query":"needle","workspace_path":"` + workspaceA + `","archived_mode":"exclude","from_updated_at":1,"limit":50}`
	req := httptest.NewRequest(http.MethodPost, "/v3/sessions:search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("search active status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []pebblestore.V3SessionSearchItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode active search: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != active.ID || payload.Items[0].Archived || payload.Items[0].MessageCount != 1 || len(payload.Items[0].Snippets) == 0 {
		t.Fatalf("active filtered items = %+v", payload.Items)
	}

	body = `{"query":"needle","workspace_path":"` + workspaceA + `","archived_mode":"only","limit":50}`
	req = httptest.NewRequest(http.MethodPost, "/v3/sessions:search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("search archived status = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode archived search: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != archived.ID || !payload.Items[0].Archived {
		t.Fatalf("archived filtered items = %+v", payload.Items)
	}
}
