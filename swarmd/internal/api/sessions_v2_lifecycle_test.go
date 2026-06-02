package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestSessionsV2LifecycleDoesNotReferenceLegacyHandlers(t *testing.T) {
	for _, path := range []string{
		"sessions_v2_lifecycle.go",
		"sessions_v2_primary.go",
		"server_routes.go",
		"run_stream_ws.go",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, forbidden := range []string{
				"createSessionFromRequest",
				"sessionCreateRequest",
				"proxyRoutedSessionRequest",
				"localCanonicalSessionForRoutedFetch",
				"sessionWorkspaceBindingForAccess",
				"SessionRouteRecord",
				"swarm_target",
			} {
				if strings.Contains(string(body), forbidden) {
					t.Fatalf("%s contains forbidden legacy/routed symbol %q", path, forbidden)
				}
			}
		})
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

func TestSessionsV2LifecyclePrimarySurfaceSchemasAndMethods(t *testing.T) {
	server, _, permissionSvc, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	server.runner = &primaryV2RunRequestRecordingRunner{emitLifecycle: true}
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	pending, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: sessionID, RunID: "run-surface", CallID: "call-surface", ToolName: "bash", ToolArguments: "{}", Requirement: "approval", Mode: sessionruntime.ModeAuto})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	type lifecycleSurfaceCase struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantKeys   []string
	}
	cases := []lifecycleSurfaceCase{
		{name: "get session", method: http.MethodGet, path: "/v2/sessions/" + sessionID, wantStatus: http.StatusOK, wantKeys: []string{"ok", "session"}},
		{name: "get messages", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/messages", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "messages"}},
		{name: "post messages", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/messages", body: `{"role":"user","content":"surface"}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "message", "session"}},
		{name: "get metadata", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/metadata", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "metadata", "updated_at"}},
		{name: "post metadata", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/metadata", body: `{"metadata":{"ticket":"surface"}}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "session"}},
		{name: "get mode", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/mode", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "mode"}},
		{name: "post mode", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/mode", body: `{"mode":"auto"}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "mode", "updated_at", "warning"}},
		{name: "get preference", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/preference", wantStatus: http.StatusOK, wantKeys: []string{"preference", "context_window", "max_output_tokens"}},
		{name: "post preference", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/preference", body: `{"provider":"codex","model":"gpt-5.4","thinking":"medium","service_tier":"fast"}`, wantStatus: http.StatusOK, wantKeys: []string{"preference", "context_window", "max_output_tokens"}},
		{name: "get codex", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/codex", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "provider", "model", "thinking", "service_tier", "context_mode", "effective_context_window", "updated_at"}},
		{name: "post codex", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/codex", body: `{"service_tier":"flex","context_mode":"1m"}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "provider", "model", "thinking", "service_tier", "context_mode", "effective_context_window", "updated_at"}},
		{name: "get active plan empty", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/plans/active", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "has_active", "active_plan"}},
		{name: "post plans", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/plans", body: `{"id":"plan-surface","title":"Plan","plan":"# Plan\n1. Test","status":"draft","approval_state":"pending","activate":true}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "plan"}},
		{name: "get plans", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/plans", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "active_plan_id", "count", "plans"}},
		{name: "get active plan", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/plans/active", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "has_active", "active_plan"}},
		{name: "post active plan", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/plans/active", body: `{"plan_id":"plan-surface"}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "active_plan"}},
		{name: "get plan by id", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/plans/plan-surface", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "plan"}},
		{name: "get plan history", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/plans/plan-surface/history", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "plan_id", "count", "revisions"}},
		{name: "get permissions", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/permissions?status=pending", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "count", "permissions"}},
		{name: "resolve permission", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/permissions/" + pending.ID + "/resolve", body: `{"action":"deny_once","reason":"surface"}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "permission", "saved_rule"}},
		{name: "resolve all permissions", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/permissions/resolve_all", body: `{"action":"deny_once","reason":"surface","limit":10}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "count", "resolved"}},
		{name: "post run", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/run", body: `{"prompt":"surface","agent_name":"swarm"}`, wantStatus: http.StatusOK, wantKeys: []string{"ok", "result"}},
		{name: "get usage", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/usage", wantStatus: http.StatusOK, wantKeys: []string{"ok", "session_id", "has_usage_summary", "usage_summary", "turn_usage_records"}},
		{name: "post run stream start", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/run/stream", body: `{"type":"run.start","prompt":"surface","agent_name":"swarm"}`, wantStatus: http.StatusAccepted, wantKeys: []string{"ok", "session_id", "run_id", "status"}},
		{name: "post run stream resume missing run id", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/run/stream", body: `{"type":"run.resume"}`, wantStatus: http.StatusBadRequest, wantKeys: []string{"error"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
			}
			for _, key := range tc.wantKeys {
				if _, ok := payload[key]; !ok {
					t.Fatalf("response missing key %q: %s", key, rec.Body.String())
				}
			}
		})
	}

	methodCases := []lifecycleSurfaceCase{
		{name: "post session get rejects", method: http.MethodPost, path: "/v2/sessions/" + sessionID, body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "put messages rejects", method: http.MethodPut, path: "/v2/sessions/" + sessionID + "/messages", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "delete metadata rejects", method: http.MethodDelete, path: "/v2/sessions/" + sessionID + "/metadata", wantStatus: http.StatusMethodNotAllowed},
		{name: "put mode rejects", method: http.MethodPut, path: "/v2/sessions/" + sessionID + "/mode", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "put preference rejects", method: http.MethodPut, path: "/v2/sessions/" + sessionID + "/preference", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "delete codex rejects", method: http.MethodDelete, path: "/v2/sessions/" + sessionID + "/codex", wantStatus: http.StatusMethodNotAllowed},
		{name: "put active plan rejects", method: http.MethodPut, path: "/v2/sessions/" + sessionID + "/plans/active", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "delete plans rejects", method: http.MethodDelete, path: "/v2/sessions/" + sessionID + "/plans", wantStatus: http.StatusMethodNotAllowed},
		{name: "post plan by id rejects", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/plans/plan-surface", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "post plan history rejects", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/plans/plan-surface/history", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "post permissions rejects", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/permissions", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "get permission resolve rejects", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/permissions/" + pending.ID + "/resolve", wantStatus: http.StatusMethodNotAllowed},
		{name: "get resolve all rejects", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/permissions/resolve_all", wantStatus: http.StatusMethodNotAllowed},
		{name: "post usage rejects", method: http.MethodPost, path: "/v2/sessions/" + sessionID + "/usage", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "get run rejects", method: http.MethodGet, path: "/v2/sessions/" + sessionID + "/run", wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tc := range methodCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestSessionsV2LifecycleRejectsReservedEndpointIDs(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	for _, reservedID := range []string{"primary", "local-containers", "local_container"} {
		t.Run(reservedID, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+reservedID+"/messages", nil)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "session_v2_bad_request") || !strings.Contains(rec.Body.String(), "invalid sessions v2 lifecycle path") {
				t.Fatalf("body = %s, want bad request invalid path", rec.Body.String())
			}
		})
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

func TestSessionsV2LifecycleRejectsBindingAttestationMismatch(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)
	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-primary-v2")
	if err != nil || !ok {
		t.Fatalf("get binding ok=%t err=%v", ok, err)
	}
	binding.AttestedByHostSwarmID = "other-host-swarm-id"
	binding.LegacyTargetKind = "attestation-mismatch-v2-test"
	if _, err := server.topology.UpsertWorkspaceBinding(binding); err != nil {
		t.Fatalf("update binding attestation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "attesting host does not match authority host") {
		t.Fatalf("body = %s, want attestation mismatch", rec.Body.String())
	}
}

func TestSessionsV2LifecycleRejectsIncompleteMatchingExecutionAndBindingAuthority(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mutateExec    func(*pebblestore.SessionExecutionV2Record)
		mutateBinding func(*pebblestore.TopologyWorkspaceBindingRecord)
	}{
		{
			name: "source workspace id empty",
			mutateExec: func(execution *pebblestore.SessionExecutionV2Record) {
				execution.SourceWorkspaceID = ""
			},
			mutateBinding: func(binding *pebblestore.TopologyWorkspaceBindingRecord) {
				binding.SourceWorkspaceID = ""
			},
		},
		{
			name: "source workspace generation zero",
			mutateExec: func(execution *pebblestore.SessionExecutionV2Record) {
				execution.SourceWorkspaceGeneration = 0
			},
			mutateBinding: func(binding *pebblestore.TopologyWorkspaceBindingRecord) {
				binding.SourceWorkspaceGeneration = 0
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

			execution, ok, err := sessionSvc.Store().GetSessionExecutionV2(sessionID)
			if err != nil || !ok {
				t.Fatalf("get execution ok=%t err=%v", ok, err)
			}
			tc.mutateExec(&execution)
			session, ok, err := sessionSvc.GetSession(sessionID)
			if err != nil || !ok {
				t.Fatalf("get session ok=%t err=%v", ok, err)
			}
			if err := sessionSvc.Store().CreateSessionWithExecutionV2(session, execution); err != nil {
				t.Fatalf("corrupt execution: %v", err)
			}

			binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-primary-v2")
			if err != nil || !ok {
				t.Fatalf("get binding ok=%t err=%v", ok, err)
			}
			tc.mutateBinding(&binding)
			binding.LegacyTargetKind = "incomplete-authority-v2-test"
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
				t.Fatalf("corrupt binding: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "authority identity is incomplete") && !strings.Contains(rec.Body.String(), "source identity mismatch") {
				t.Fatalf("body = %s, want incomplete authority/source identity rejection", rec.Body.String())
			}
		})
	}
}

func TestSessionsV2LifecycleDoesNotUseWorkspacePathsOrNamesAsAuthority(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	execution, ok, err := sessionSvc.Store().GetSessionExecutionV2(sessionID)
	if err != nil || !ok {
		t.Fatalf("get execution ok=%t err=%v", ok, err)
	}
	execution.SourceWorkspaceName = "renamed-workspace"
	execution.SourceWorkspacePath = "renamed-source"
	execution.RuntimeWorkspacePath = "renamed-runtime"
	session, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if err := sessionSvc.Store().CreateSessionWithExecutionV2(session, execution); err != nil {
		t.Fatalf("update execution path/name fields: %v", err)
	}

	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-primary-v2")
	if err != nil || !ok {
		t.Fatalf("get binding ok=%t err=%v", ok, err)
	}
	binding.SourceWorkspaceName = "renamed-workspace"
	binding.SourceWorkspacePath = "other-source"
	binding.DestinationWorkspacePath = "other-runtime"
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
		t.Fatalf("update binding path/name fields: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
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

func TestSessionsV2LifecycleRunRejectsRequestTimeAuthorityOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "target_kind", body: `{"prompt":"hello","target_kind":"subagent"}`, want: "target_kind"},
		{name: "target_name", body: `{"prompt":"hello","target_name":"memory"}`, want: "target_name"},
		{name: "execution_context_workspace_path", body: `{"prompt":"hello","execution_context":{"workspace_path":"override-workspace"}}`, want: "execution_context.workspace_path"},
		{name: "execution_context_cwd", body: `{"prompt":"hello","execution_context":{"cwd":"override-cwd"}}`, want: "execution_context.cwd"},
		{name: "execution_context_worktree_root_path", body: `{"prompt":"hello","execution_context":{"worktree_root_path":"override-worktree"}}`, want: "execution_context.worktree_root_path"},
		{name: "execution_context_worktree_mode", body: `{"prompt":"hello","execution_context":{"worktree_mode":"off"}}`, want: "execution_context.worktree_mode"},
		{name: "execution_context_worktree_branch", body: `{"prompt":"hello","execution_context":{"worktree_branch":"feature"}}`, want: "execution_context.worktree_branch"},
		{name: "execution_context_worktree_base_branch", body: `{"prompt":"hello","execution_context":{"worktree_base_branch":"main"}}`, want: "execution_context.worktree_base_branch"},
		{name: "tool_scope", body: `{"prompt":"hello","tool_scope":{}}`, want: "tool_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			server.runner = &primaryV2RunRequestRecordingRunner{}
			sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

			req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("run status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "session_v2_bad_request") || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want bad request mentioning %s", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestSessionsV2LifecycleRunStreamControlRejectsRequestTimeAuthorityOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "target_kind", body: `{"type":"run.start","prompt":"hello","target_kind":"subagent"}`, want: "target_kind"},
		{name: "target_name", body: `{"type":"run.start","prompt":"hello","target_name":"memory"}`, want: "target_name"},
		{name: "execution_context_workspace_path", body: `{"type":"run.start","prompt":"hello","execution_context":{"workspace_path":"override-workspace"}}`, want: "execution_context.workspace_path"},
		{name: "execution_context_cwd", body: `{"type":"run.start","prompt":"hello","execution_context":{"cwd":"override-cwd"}}`, want: "execution_context.cwd"},
		{name: "execution_context_worktree_root_path", body: `{"type":"run.start","prompt":"hello","execution_context":{"worktree_root_path":"override-worktree"}}`, want: "execution_context.worktree_root_path"},
		{name: "execution_context_worktree_mode", body: `{"type":"run.start","prompt":"hello","execution_context":{"worktree_mode":"off"}}`, want: "execution_context.worktree_mode"},
		{name: "execution_context_worktree_branch", body: `{"type":"run.start","prompt":"hello","execution_context":{"worktree_branch":"feature"}}`, want: "execution_context.worktree_branch"},
		{name: "execution_context_worktree_base_branch", body: `{"type":"run.start","prompt":"hello","execution_context":{"worktree_base_branch":"main"}}`, want: "execution_context.worktree_base_branch"},
		{name: "tool_scope", body: `{"type":"run.start","prompt":"hello","tool_scope":{}}`, want: "tool_scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			server.runner = &primaryV2RunRequestRecordingRunner{}
			sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

			req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run/stream", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("run stream status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "session_v2_bad_request") || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want bad request mentioning %s", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestSessionsV2LifecycleRunAllowsSafeInstructionsAndBackgroundOwnership(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunRequestRecordingRunner{}
	server.runner = runner
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run", bytes.NewBufferString(`{"prompt":"hello","instructions":"safe user instructions","agent_name":"swarm","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("run status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	calls, recordedSessionID, recordedRequest, _ := runner.snapshot()
	if calls != 1 || recordedSessionID != sessionID {
		t.Fatalf("runner calls=%d session_id=%q, want one call for %q", calls, recordedSessionID, sessionID)
	}
	if recordedRequest.Prompt != "hello" || recordedRequest.Instructions != "safe user instructions" || !recordedRequest.Background {
		t.Fatalf("runner request = %+v", recordedRequest)
	}
	if recordedRequest.TargetKind != "" || recordedRequest.TargetName != "" || recordedRequest.ExecutionContext != nil || recordedRequest.ToolScope != nil {
		t.Fatalf("runner request carried authority override: %+v", recordedRequest)
	}
}

func TestSessionsV2LifecycleRunStreamControlAllowsSafeBackgroundOwnership(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunRequestRecordingRunner{emitLifecycle: true}
	server.runner = runner
	sessionID := createPrimarySessionV2ForLifecycleTest(t, server, swarmStore, "binding-primary-v2", pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, true)

	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run/stream", bytes.NewBufferString(`{"type":"run.start","prompt":"hello","instructions":"safe user instructions","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("run stream status = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	calls, recordedSessionID, recordedRequest, recordedMeta := runner.snapshot()
	if calls != 1 || recordedSessionID != sessionID {
		t.Fatalf("runner calls=%d session_id=%q, want one call for %q", calls, recordedSessionID, sessionID)
	}
	if recordedRequest.Prompt != "hello" || recordedRequest.Instructions != "safe user instructions" || !recordedRequest.Background {
		t.Fatalf("runner request = %+v", recordedRequest)
	}
	if recordedRequest.TargetKind != "" || recordedRequest.TargetName != "" || recordedRequest.ExecutionContext != nil || recordedRequest.ToolScope != nil {
		t.Fatalf("runner request carried authority override: %+v", recordedRequest)
	}
	if recordedMeta.OwnerTransport != "background_api" {
		t.Fatalf("owner transport = %q, want background_api", recordedMeta.OwnerTransport)
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

func TestSessionsV2LifecycleLocalContainerDispatchesNativeRuntime(t *testing.T) {
	hostServer, _, _, routeStore, hostSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runtimeServer, runtimeSessionSvc, _, _, runtimeSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	runner := &primaryV2RunRequestRecordingRunner{emitLifecycle: true}
	runtimeServer.runner = runner
	seedSessionsV2LocalContainerAuthority(t, hostServer, hostSwarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	seedRuntimeSessionsV2OpenContainerAuthority(t, runtimeServer, runtimeSwarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	setTestServerLocalSwarmID(t, runtimeServer, "container-swarm")
	seedRuntimeSessionsV2Pairing(t, runtimeSwarmStore, "host-swarm-id")

	var runtimeCalls atomic.Int32
	var lifecyclePathsMu sync.Mutex
	lifecyclePaths := make([]string, 0, 4)
	runtimeHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("runtime lifecycle peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		if r.Header.Get("X-Swarm-Principal-User-ID") != "" || r.Header.Get("X-Swarm-Principal-Account-Scope-ID") != "" {
			t.Fatalf("runtime lifecycle forwarded principal headers")
		}
		runtimeCalls.Add(1)
		if r.URL.Path != runtimeSessionsV2OpenPath {
			lifecyclePathsMu.Lock()
			lifecyclePaths = append(lifecyclePaths, r.Method+" "+r.URL.Path)
			lifecyclePathsMu.Unlock()
		}
		r = r.WithContext(context.WithValue(r.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
		if principal, ok := runtimeServer.trustedPairingPrincipalForPeerRequest(r); ok {
			r = withSessionsV2TestPrincipal(r, principal)
		}
		runtimeServer.Handler().ServeHTTP(w, r)
	}))
	defer runtimeHTTP.Close()
	if err := hostServer.RegisterAuthorityConnection(AuthorityConnection{AuthorityHostSwarmID: "container-swarm", AccountScopeID: testPrincipal().AccountScopeID, TransportKind: authorityConnectionTransportHTTP, TransportRef: runtimeHTTP.URL, Health: AuthorityConnectionHealthOnline}); err != nil {
		t.Fatalf("register runtime authority connection: %v", err)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/local-containers", bytes.NewBufferString(`{"swarm_id":"container-swarm","workspace_binding_id":"binding-container-v2","title":"container v2 lifecycle","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(createRec, withTestPrincipal(createReq))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d, body=%s", createRec.Code, http.StatusOK, createRec.Body.String())
	}
	var createPayload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	sessionID := strings.TrimSpace(createPayload.Session.ID)
	if sessionID == "" {
		t.Fatalf("created local-container session missing id: %s", createRec.Body.String())
	}

	var legacyCalls atomic.Int32
	legacyHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalls.Add(1)
		writeJSON(w, http.StatusTeapot, map[string]any{"ok": false, "error": "legacy route should not be used"})
	}))
	defer legacyHTTP.Close()
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: sessionID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "legacy-child", ChildBackendURL: legacyHTTP.URL, HostSwarmID: "host-swarm-id", HostContainerID: "legacy-container", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/legacy/workspace", WorkspaceBindingID: "legacy-binding"}); err != nil {
		t.Fatalf("put legacy route trap: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	getRec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("messages get status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/messages", bytes.NewBufferString(`{"role":"user","content":"via runtime"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusOK {
		t.Fatalf("messages post status = %d, want %d, body=%s", postRec.Code, http.StatusOK, postRec.Body.String())
	}

	runReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run", bytes.NewBufferString(`{"prompt":"run via runtime","instructions":"safe user instructions","background":true}`))
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(runRec, withTestPrincipal(runReq))
	if runRec.Code != http.StatusOK {
		t.Fatalf("run status = %d, want %d, body=%s", runRec.Code, http.StatusOK, runRec.Body.String())
	}
	calls, recordedSessionID, recordedRequest, _ := runner.snapshot()
	if calls != 1 || recordedSessionID != sessionID {
		t.Fatalf("runner calls=%d session_id=%q, want run call for %q", calls, recordedSessionID, sessionID)
	}
	if recordedRequest.Prompt != "run via runtime" || !recordedRequest.Background || recordedRequest.TargetKind != "" || recordedRequest.TargetName != "" || recordedRequest.ExecutionContext != nil || recordedRequest.ToolScope != nil {
		t.Fatalf("runtime run request = %+v", recordedRequest)
	}

	streamReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run/stream", bytes.NewBufferString(`{"type":"run.start","prompt":"stream via runtime","background":true}`))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(streamRec, withTestPrincipal(streamReq))
	if streamRec.Code != http.StatusAccepted {
		t.Fatalf("run stream status = %d, want %d, body=%s", streamRec.Code, http.StatusAccepted, streamRec.Body.String())
	}
	calls, recordedSessionID, recordedRequest, recordedMeta := runner.snapshot()
	if calls != 2 || recordedSessionID != sessionID {
		t.Fatalf("runner calls=%d session_id=%q, want second stream call for %q", calls, recordedSessionID, sessionID)
	}
	if recordedRequest.Prompt != "stream via runtime" || !recordedRequest.Background || recordedRequest.TargetKind != "" || recordedRequest.TargetName != "" || recordedRequest.ExecutionContext != nil || recordedRequest.ToolScope != nil {
		t.Fatalf("runtime stream request = %+v", recordedRequest)
	}
	if recordedMeta.OwnerTransport != "background_api" {
		t.Fatalf("owner transport = %q, want background_api", recordedMeta.OwnerTransport)
	}

	overrideReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run", bytes.NewBufferString(`{"prompt":"blocked","target_kind":"subagent"}`))
	overrideReq.Header.Set("Content-Type", "application/json")
	overrideRec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(overrideRec, withTestPrincipal(overrideReq))
	if overrideRec.Code != http.StatusBadRequest {
		t.Fatalf("override status = %d, want %d, body=%s", overrideRec.Code, http.StatusBadRequest, overrideRec.Body.String())
	}
	if !strings.Contains(overrideRec.Body.String(), "target_kind") {
		t.Fatalf("override body = %s, want target_kind rejection", overrideRec.Body.String())
	}

	if legacyCalls.Load() != 0 {
		t.Fatalf("legacy route calls = %d, want 0", legacyCalls.Load())
	}
	wantGet := http.MethodGet + " " + runtimeSessionsV2Prefix + sessionID + "/messages"
	wantPost := http.MethodPost + " " + runtimeSessionsV2Prefix + sessionID + "/messages"
	wantRun := http.MethodPost + " " + runtimeSessionsV2Prefix + sessionID + "/run"
	wantStream := http.MethodPost + " " + runtimeSessionsV2Prefix + sessionID + "/run/stream"
	lifecyclePathsMu.Lock()
	paths := strings.Join(lifecyclePaths, "\n")
	lifecyclePathsMu.Unlock()
	for _, wantPath := range []string{wantGet, wantPost, wantRun, wantStream} {
		if !strings.Contains(paths, wantPath) {
			t.Fatalf("runtime lifecycle paths = %q, missing %q", paths, wantPath)
		}
	}
	messages, err := runtimeSessionSvc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list runtime messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "via runtime" {
		t.Fatalf("runtime messages = %+v, want posted message", messages)
	}
	if runtimeCalls.Load() < 6 {
		t.Fatalf("runtime calls = %d, want open plus lifecycle/run calls", runtimeCalls.Load())
	}
}

func TestSessionsV2LifecycleLocalContainerFailsClosedWithoutRuntimeAuthorityConnection(t *testing.T) {
	hostServer, sessionSvc, _, routeStore, hostSwarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2LocalContainerAuthority(t, hostServer, hostSwarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	sessionID := "session-local-container-lifecycle-fail-closed"
	openReq := runtimeSessionsV2OpenTestRequest(sessionID, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	execution := openReq.SessionExecution
	snapshot := pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/workspaces/swarm-go",
		WorkspaceName:  "swarm-go",
		Title:          "local-container lifecycle fail closed",
		Mode:           sessionruntime.ModeAuto,
		Preference:     openReq.Config.Preference,
		Metadata:       sessionruntime.RuntimeSessionV2Metadata(nil, runtimeSessionsV2ExecutionFromRecord(execution)),
	}
	if err := sessionSvc.Store().CreateSessionWithExecutionV2(snapshot, execution); err != nil {
		t.Fatalf("seed local-container session execution: %v", err)
	}

	var legacyCalls atomic.Int32
	legacyHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "legacy": true})
	}))
	defer legacyHTTP.Close()
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: sessionID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "legacy-child", ChildBackendURL: legacyHTTP.URL, HostSwarmID: "host-swarm-id", HostContainerID: "legacy-container", HostWorkspacePath: "/host/swarm-go", RuntimeWorkspacePath: "/legacy/workspace", WorkspaceBindingID: "legacy-binding"}); err != nil {
		t.Fatalf("put legacy route trap: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+sessionID+"/messages", nil)
	rec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runtime session authority connection") {
		t.Fatalf("body = %s, want missing runtime authority connection", rec.Body.String())
	}

	runReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+sessionID+"/run", bytes.NewBufferString(`{"prompt":"blocked"}`))
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	hostServer.Handler().ServeHTTP(runRec, withTestPrincipal(runReq))
	if runRec.Code != http.StatusNotFound {
		t.Fatalf("run status = %d, want %d, body=%s", runRec.Code, http.StatusNotFound, runRec.Body.String())
	}
	if !strings.Contains(runRec.Body.String(), "runtime session authority connection") {
		t.Fatalf("run body = %s, want missing runtime authority connection", runRec.Body.String())
	}
	if legacyCalls.Load() != 0 {
		t.Fatalf("legacy route calls = %d, want 0", legacyCalls.Load())
	}
}

type primaryV2RunRequestRecordingRunner struct {
	mu            sync.Mutex
	calls         int
	sessionID     string
	request       runruntime.RunRequest
	meta          runruntime.RunStartMeta
	emitLifecycle bool
}

func (r *primaryV2RunRequestRecordingRunner) RunTurn(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta) (runruntime.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	r.mu.Unlock()
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background, TargetKind: request.TargetKind, TargetName: request.TargetName}, nil
}

func (r *primaryV2RunRequestRecordingRunner) RunTurnStreaming(_ context.Context, sessionID string, request runruntime.RunRequest, meta runruntime.RunStartMeta, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.sessionID = sessionID
	r.request = request
	r.meta = meta
	emitLifecycle := r.emitLifecycle
	r.mu.Unlock()
	if emitLifecycle && onEvent != nil {
		onEvent(runruntime.StreamEvent{Type: runruntime.StreamEventSessionLifecycle, SessionID: sessionID, RunID: meta.RunID, Lifecycle: &pebblestore.SessionLifecycleSnapshot{SessionID: sessionID, RunID: meta.RunID, Active: true, OwnerTransport: meta.OwnerTransport}})
	}
	return runruntime.RunResult{SessionID: sessionID, Background: request.Background, TargetKind: request.TargetKind, TargetName: request.TargetName}, nil
}

func (r *primaryV2RunRequestRecordingRunner) snapshot() (int, string, runruntime.RunRequest, runruntime.RunStartMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request := r.request
	if request.ToolScope != nil {
		scope := *request.ToolScope
		request.ToolScope = &scope
	}
	if request.ExecutionContext != nil {
		ctx := *request.ExecutionContext
		request.ExecutionContext = &ctx
	}
	return r.calls, r.sessionID, request, r.meta
}

func (r *primaryV2RunRequestRecordingRunner) StopSessionRun(sessionID, runID, reason string) error {
	return nil
}

func (r *primaryV2RunRequestRecordingRunner) ExecuteToolForSessionScope(context.Context, string, tool.Call) (string, error) {
	return "{}", nil
}

func (r *primaryV2RunRequestRecordingRunner) ListAgentToolDefinitions() []tool.Definition { return nil }

func (r *primaryV2RunRequestRecordingRunner) ListAgentToolDefinitionsForAccount(string) []tool.Definition {
	return nil
}

func (r *primaryV2RunRequestRecordingRunner) ResolveAgentToolContract(pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
}

func (r *primaryV2RunRequestRecordingRunner) ResolveAgentToolContractForAccount(string, pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, *permission.Policy, map[string]bool, error) {
	return runruntime.ResolvedAgentToolContract{}, nil, nil, nil
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
