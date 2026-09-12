package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: POST /v3/sessions must create ordinary Swarm automation conversations
// without a definition or approved plan. Exercise the authenticated HTTP mutation
// boundary and durable replay; metadata spoofing must leave the session unchanged.
func TestAutomationManagementCreateReplayAndProtectedPurpose(t *testing.T) {
	server, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	workspace := t.TempDir()
	binding := seedSessionsV3PrimaryAuthority(t, server, workspace)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v3/sessions", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(req))
		return rec
	}
	body := fmt.Sprintf(`{"client_request_id":"management-create","workspace_path":%q,"workspace_binding_id":%q,"agent_name":"swarm","purpose":"automation_management","mode":"auto"}`, workspace, binding)
	first := post(body)
	if first.Code != http.StatusOK {
		t.Fatalf("create: %d %s", first.Code, first.Body.String())
	}
	var created struct {
		Session store.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Session.Automation != nil || store.SessionAutomationManagementWorkspace(created.Session) == "" || created.Session.MessageCount != 0 || created.Session.Mode != "auto" {
		t.Fatalf("unexpected creation: %+v", created.Session)
	}
	shell, err := sessionsV3SyncSessionShell(created.Session)
	if err != nil || store.SessionAutomationManagementWorkspace(shell) != store.SessionAutomationManagementWorkspace(created.Session) {
		t.Fatalf("sync shell lost purpose: %+v %v", shell, err)
	}
	replay := post(body)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	var repeated struct {
		Session store.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &repeated); err != nil {
		t.Fatal(err)
	}
	if repeated.Session.ID != created.Session.ID {
		t.Fatal("retry created a second conversation")
	}
	spoof := post(fmt.Sprintf(`{"client_request_id":"spoof","workspace_binding_id":%q,"agent_name":"swarm","metadata":{"swarm_v3_session_purpose":"automation_management"}}`, binding))
	if spoof.Code != http.StatusBadRequest {
		t.Fatalf("spoof admitted: %d", spoof.Code)
	}
	foreign := post(`{"client_request_id":"foreign-workspace","workspace_binding_id":"unowned-binding","agent_name":"swarm","purpose":"automation_management"}`)
	if foreign.Code == http.StatusOK {
		t.Fatal("unowned workspace admitted")
	}
	current, found, err := sessions.GetSession(created.Session.ID)
	if err != nil || !found || current.Automation != nil || current.MessageCount != 0 {
		t.Fatalf("creation granted execution: %+v %v", current, err)
	}
	if _, found, err := sessions.Store().GetV3SessionActiveRunIntent(current.ID); err != nil || found {
		t.Fatalf("creation started a run: %v %v", found, err)
	}
	if _, found, err := sessions.Store().GetActivePlan(current.ID); err != nil || found {
		t.Fatalf("creation installed a plan: %v %v", found, err)
	}
}
