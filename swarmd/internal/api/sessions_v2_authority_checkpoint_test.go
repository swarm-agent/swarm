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

func TestSessionsV2LifecycleLocalContainerAuthorityValidatedThenDispatchFailsClosed(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Session          pebblestore.SessionSnapshot     `json:"session"`
		SessionExecution sessionruntime.SessionExecution `json:"session_execution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if payload.SessionExecution.ExecutionClass != sessionruntime.SessionExecutionClassLocalContainer || strings.TrimSpace(payload.Session.ID) == "" {
		t.Fatalf("created execution = %+v session=%+v", payload.SessionExecution, payload.Session)
	}

	lifecycleReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+payload.Session.ID+"/messages", nil)
	lifecycleRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(lifecycleRec, withTestPrincipal(lifecycleReq))
	if lifecycleRec.Code != http.StatusBadRequest {
		t.Fatalf("lifecycle status = %d, want %d, body=%s", lifecycleRec.Code, http.StatusBadRequest, lifecycleRec.Body.String())
	}
	if !strings.Contains(lifecycleRec.Body.String(), "local-container lifecycle dispatch is not implemented") {
		t.Fatalf("body = %s, want local-container dispatch placeholder", lifecycleRec.Body.String())
	}
}

func TestSessionsV2LifecycleLocalContainerMutatingReadOnlyBindingFailsBeforeDispatch(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
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

	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-container-v2")
	if err != nil || !ok {
		t.Fatalf("get binding ok=%t err=%v", ok, err)
	}
	binding.AccessMode = pebblestore.TopologyWorkspaceBindingAccessModeReadOnly
	binding.Writable = false
	snapshot, err := server.topology.SnapshotForAccount(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatalf("snapshot topology: %v", err)
	}
	replaced := false
	for i := range snapshot.WorkspaceBindings {
		if snapshot.WorkspaceBindings[i].BindingID == binding.BindingID {
			snapshot.WorkspaceBindings[i] = binding
			replaced = true
			break
		}
	}
	if !replaced {
		t.Fatalf("workspace binding %q missing from snapshot", binding.BindingID)
	}
	if err := server.topology.ReplaceSnapshotForAccount(testPrincipal().AccountScopeID, snapshot); err != nil {
		t.Fatalf("update read-only binding: %v", err)
	}

	lifecycleReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+payload.Session.ID+"/messages", bytes.NewBufferString(`{"role":"user","content":"blocked"}`))
	lifecycleReq.Header.Set("Content-Type", "application/json")
	lifecycleRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(lifecycleRec, withTestPrincipal(lifecycleReq))
	if lifecycleRec.Code != http.StatusForbidden {
		t.Fatalf("lifecycle status = %d, want %d, body=%s", lifecycleRec.Code, http.StatusForbidden, lifecycleRec.Body.String())
	}
	if !strings.Contains(lifecycleRec.Body.String(), "read-only") {
		t.Fatalf("body = %s, want read-only rejection", lifecycleRec.Body.String())
	}
	if strings.Contains(lifecycleRec.Body.String(), "dispatch is not implemented") {
		t.Fatalf("authority failure should occur before dispatch placeholder: %s", lifecycleRec.Body.String())
	}
}

func TestSessionsV2AuthorityRejectsExecutionUserMismatch(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	execution, ok, err := sessionSvc.Store().GetSessionExecutionV2(sessionID)
	if err != nil || !ok {
		t.Fatalf("get execution ok=%t err=%v", ok, err)
	}
	execution.UserID = "other-user"
	session, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if err := sessionSvc.Store().CreateSessionWithExecutionV2(session, execution); err != nil {
		t.Fatalf("update execution user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "execution user does not match principal") {
		t.Fatalf("body = %s, want execution user mismatch", rec.Body.String())
	}
}

func TestSessionsV2AuthorityRejectsMissingRuntimePlacement(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	snapshot, err := server.topology.SnapshotForAccount(testPrincipal().AccountScopeID)
	if err != nil {
		t.Fatalf("snapshot topology: %v", err)
	}
	filtered := snapshot.RuntimePlacements[:0]
	for _, placement := range snapshot.RuntimePlacements {
		if placement.RuntimeSwarmID == "host-swarm-id" {
			continue
		}
		filtered = append(filtered, placement)
	}
	snapshot.RuntimePlacements = filtered
	if err := server.topology.ReplaceSnapshotForAccount(testPrincipal().AccountScopeID, snapshot); err != nil {
		t.Fatalf("delete runtime placement: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runtime placement") || !strings.Contains(rec.Body.String(), "was not found") {
		t.Fatalf("body = %s, want missing runtime placement rejection", rec.Body.String())
	}
}

func TestSessionsV2AuthorityRejectsStaleRuntimePlacementGeneration(t *testing.T) {
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
	if !strings.Contains(rec.Body.String(), "runtime placement generation mismatch") {
		t.Fatalf("body = %s, want runtime placement generation mismatch", rec.Body.String())
	}
}
