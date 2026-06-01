package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV2LifecycleDoesNotReferenceLegacyHandlers(t *testing.T) {
	body, err := os.ReadFile("sessions_v2_lifecycle.go")
	if err != nil {
		t.Fatalf("read lifecycle file: %v", err)
	}
	for _, forbidden := range []string{
		"handleSessionByID",
		"handleSessions(",
		"createSessionFromRequest",
		"proxyRoutedSessionRequest",
		"localCanonicalSessionForRoutedFetch",
		"handleManagedHostSession",
		"sessionWorkspaceBindingForAccess",
		"enforceSessionBindingWriteAccess",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("sessions_v2_lifecycle.go contains forbidden legacy/routed symbol %q", forbidden)
		}
	}
}

func TestSessionsV2LifecycleGetAndAppendUseExecutionAuthority(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID, nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), `"ok":true`) || !strings.Contains(getRec.Body.String(), sessionID) {
		t.Fatalf("get body = %s", getRec.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"role":"user","content":"hello v2"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusOK {
		t.Fatalf("append status = %d, want %d, body=%s", postRec.Code, http.StatusOK, postRec.Body.String())
	}
	if !strings.Contains(postRec.Body.String(), `"message"`) || !strings.Contains(postRec.Body.String(), "hello v2") {
		t.Fatalf("append body = %s", postRec.Body.String())
	}
}

func TestSessionsV2LifecycleRejectsMissingExecution(t *testing.T) {
	server, sessionSvc := newSessionAccessModeTestServer(t, pebblestore.TopologyWorkspaceBindingAccessModeReadWrite)
	pref := pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "medium"}
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Title: "legacy", WorkspacePath: "/tmp/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pref})
	if err != nil {
		t.Fatalf("create legacy session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+session.ID, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session_v2_authority_not_found") {
		t.Fatalf("body = %s, want authority not found", rec.Body.String())
	}
}

func TestSessionsV2LifecycleRejectsStalePlacementGeneration(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	placement, ok, err := server.topology.GetRuntimePlacementForAccount(testPrincipal().AccountScopeID, "host-swarm-id")
	if err != nil || !ok {
		t.Fatalf("get placement ok=%t err=%v", ok, err)
	}
	placement.PlacementGeneration = 2
	if _, err := server.topology.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, placement); err != nil {
		t.Fatalf("put stale placement: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "placement generation mismatch") {
		t.Fatalf("body = %s, want generation mismatch", rec.Body.String())
	}
}

func TestSessionsV2LifecycleMetadataUpdateRejectsAuthorityKeys(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "backend_url", body: `{"metadata":{"backend_url":"https://example.invalid/backend"}}`},
		{name: "workspace_path", body: `{"metadata":{"workspace_path":"workspace-path"}}`},
		{name: "target_swarm_id", body: `{"metadata":{"target_swarm_id":"target-swarm"}}`},
		{name: "swarm_v2_runtime_swarm_id", body: `{"metadata":{"swarm_v2_runtime_swarm_id":"runtime-swarm"}}`},
		{name: "local_workspace_binding_id", body: `{"metadata":{"local_workspace_binding_id":"binding-primary-v2"}}`},
		{name: "nested", body: `{"metadata":{"safe":{"workspace_path":"/workspace"}}}`},
		{name: "nested_slice", body: `{"metadata":{"safe":[{"backend_url":"https://example.invalid/backend"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/metadata", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("metadata status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "routing authority key") {
				t.Fatalf("body = %s, want routing authority rejection", rec.Body.String())
			}
		})
	}
}

func TestSessionsV2LifecycleMetadataUpdateAcceptsSafeMetadata(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/metadata", bytes.NewBufferString(`{"metadata":{"ticket":"abc-123","labels":["safe"],"nested":{"note":"ok"}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ticket":"abc-123"`) || !strings.Contains(rec.Body.String(), `"note":"ok"`) {
		t.Fatalf("metadata body = %s, want safe metadata", rec.Body.String())
	}
}

func TestSessionsV2LifecycleReadOnlyBindingAllowsReadBlocksMutation(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-readonly-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadOnly, false)

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("read status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"role":"user","content":"blocked"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusForbidden {
		t.Fatalf("write status = %d, want %d, body=%s", postRec.Code, http.StatusForbidden, postRec.Body.String())
	}
	if !strings.Contains(postRec.Body.String(), "read-only") {
		t.Fatalf("body = %s, want read-only rejection", postRec.Body.String())
	}
}

func createPrimarySessionV2ForLifecycleTest(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, bindingID, accessMode string, writable bool) string {
	t.Helper()
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", bindingID, "/host/swarm-go")
	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"`+bindingID+`","title":"primary v2 lifecycle","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if strings.TrimSpace(payload.Session.ID) == "" {
		t.Fatalf("missing created session id: %s", rec.Body.String())
	}
	if accessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !writable {
		binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, bindingID)
		if err != nil || !ok {
			t.Fatalf("get binding ok=%t err=%v", ok, err)
		}
		binding.AccessMode = accessMode
		binding.Writable = writable
		if _, err := server.topology.UpsertWorkspaceBinding(binding); err != nil {
			t.Fatalf("update binding access mode: %v", err)
		}
	}
	return payload.Session.ID
}
