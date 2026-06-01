package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV2PrimaryAllowsTUIOnlyCWDCreateWithoutWorkspaceBinding(t *testing.T) {
	server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/primary", bytes.NewBufferString(`{"swarm_id":"host-swarm-id","workspace_path":"/tmp/cwd-only","title":"cwd","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Swarm-Client", "swarmtui")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		OK               bool                            `json:"ok"`
		Session          pebblestore.SessionSnapshot     `json:"session"`
		SessionExecution sessionruntime.SessionExecution `json:"session_execution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Session.WorkspacePath != "/tmp/cwd-only" || payload.SessionExecution.WorkspaceBindingID != "" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.SessionExecution.ExecutionClass != sessionruntime.SessionExecutionClassPrimary || payload.SessionExecution.RuntimeSwarmID != "host-swarm-id" || payload.SessionExecution.RuntimeWorkspacePath != "/tmp/cwd-only" {
		t.Fatalf("session execution = %+v", payload.SessionExecution)
	}
	storedExecution, ok, err := sessionSvc.Store().GetSessionExecutionV2(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("stored execution ok=%t err=%v", ok, err)
	}
	if storedExecution.WorkspaceBindingID != "" || !strings.HasPrefix(storedExecution.SourceWorkspaceID, sessionruntime.SessionExecutionTUICWDSourceIDPrefix) {
		t.Fatalf("stored execution = %+v", storedExecution)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want no legacy route for TUI cwd primary v2", routes, err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+payload.Session.ID, nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("lifecycle get status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
}

func TestSessionsV2PrimaryRejectsNonTUIWorkspacePathException(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_path":"/tmp/cwd-only","title":"cwd","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "workspace_binding_id is required") {
		t.Fatalf("body = %s, want binding required error", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsWorkspacePathWhenBindingPresent(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/primary", bytes.NewBufferString(`{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","workspace_path":"/tmp/cwd-only","title":"bad","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Swarm-Client", "swarmtui")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routing authority field") {
		t.Fatalf("body = %s, want routing authority rejection", rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}
