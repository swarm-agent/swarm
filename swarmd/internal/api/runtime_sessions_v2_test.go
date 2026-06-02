package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRuntimeSessionsV2RoutesRegisteredFailClosed(t *testing.T) {
	server := &Server{}
	mux := server.apiMux()
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "open", method: http.MethodPost, path: "/v2/internal/runtime-sessions/open"},
		{name: "sync state", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/sync/state"},
		{name: "run", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/run"},
		{name: "stream get", method: http.MethodGet, path: "/v2/internal/runtime-sessions/session-123/run/stream"},
		{name: "stream post", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/run/stream"},
		{name: "mirror batch", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/mirror/batch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			want := http.StatusNotImplemented
			switch tt.path {
			case runtimeSessionsV2OpenPath:
				want = http.StatusBadRequest
			case "/v2/internal/runtime-sessions/session-123/run", "/v2/internal/runtime-sessions/session-123/run/stream", "/v2/internal/runtime-sessions/session-123/mirror/batch":
				want = http.StatusForbidden
			}
			if rec.Code != want {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, want, rec.Body.String())
			}
			if tt.path != runtimeSessionsV2OpenPath && want == http.StatusNotImplemented && !strings.Contains(rec.Body.String(), "runtime_session_not_implemented") {
				t.Fatalf("body = %s, want not implemented code", rec.Body.String())
			}
		})
	}
}

func TestRuntimeSessionsV2RoutesRejectUnknownPathAndMethod(t *testing.T) {
	server := &Server{}
	mux := server.apiMux()

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "unknown action", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/unknown", want: http.StatusNotFound},
		{name: "missing id", method: http.MethodPost, path: "/v2/internal/runtime-sessions/%20/run", want: http.StatusBadRequest},
		{name: "trailing slash not canonicalized", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123/run/", want: http.StatusNotFound},
		{name: "session get wrong method", method: http.MethodPost, path: "/v2/internal/runtime-sessions/session-123", want: http.StatusMethodNotAllowed},
		{name: "wrong method", method: http.MethodGet, path: "/v2/internal/runtime-sessions/open", want: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.name == "missing id" {
				req.URL.Path = "/v2/internal/runtime-sessions/ /run"
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestRuntimeSessionsV2HandlersDoNotCallLegacyRoutedHandlers(t *testing.T) {
	body, err := os.ReadFile("runtime_sessions_v2.go")
	if err != nil {
		t.Fatalf("read runtime_sessions_v2.go: %v", err)
	}
	for _, forbidden := range []string{
		"handlePeerSessionOpen(",
		"createSessionFromRequestWithSessionID(",
		"proxyRoutedSessionRequest(",
		"routedSessionTarget(",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("runtime_sessions_v2.go contains forbidden legacy symbol %q", forbidden)
		}
	}
}

func TestRuntimeSessionsV2OpenCreatesContainerLocalSession(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedRuntimeSessionsV2OpenContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	reqBody := runtimeSessionsV2OpenTestRequest("session-runtime-open", "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	rec := postRuntimeSessionsV2Open(t, server, reqBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload sessionruntime.RuntimeSessionOpenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode open response: %v", err)
	}
	if !payload.OK || payload.SessionID != "session-runtime-open" || payload.RuntimeSwarmID != "container-swarm" || payload.AuthorityHostSwarmID != "host-swarm-id" || payload.AuthorityContainerID != "host-container-1" || payload.WorkspaceBindingID != "binding-container-v2" || payload.RuntimeWorkspacePath != "/workspaces/swarm-go" || payload.Status != "opened" {
		t.Fatalf("unexpected open response: %+v", payload)
	}
	snapshot, ok, err := sessionSvc.GetSession("session-runtime-open")
	if err != nil || !ok {
		t.Fatalf("get runtime session ok=%t err=%v", ok, err)
	}
	if snapshot.WorkspacePath != "/workspaces/swarm-go" || snapshot.Metadata["swarm_v2_execution_class"] != sessionruntime.SessionExecutionClassLocalContainer || snapshot.Metadata["local_workspace_binding_id"] != "binding-container-v2" {
		t.Fatalf("unexpected runtime session snapshot: %+v", snapshot)
	}
	storedExecution, ok, err := sessionSvc.Store().GetSessionExecutionV2("session-runtime-open")
	if err != nil || !ok {
		t.Fatalf("get execution ok=%t err=%v", ok, err)
	}
	if storedExecution.ExecutionClass != sessionruntime.SessionExecutionClassLocalContainer || storedExecution.RuntimeSwarmID != "container-swarm" || storedExecution.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("unexpected stored execution: %+v", storedExecution)
	}
}

func TestRuntimeSessionsV2OpenRepairsBindingFromAuthoritySnapshot(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedRuntimeSessionsV2OpenContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	stale, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-container-v2")
	if err != nil || !ok {
		t.Fatalf("get seeded binding ok=%t err=%v", ok, err)
	}
	snapshot := stale
	snapshot.SourceWorkspaceID = "workspace-primary-v2"
	snapshot.SourceWorkspaceGeneration = 9
	stale.SourceWorkspaceID = "workspace-container-local"
	stale.SourceWorkspaceGeneration = 1
	if _, err := server.topology.UpsertWorkspaceBinding(stale); err != nil {
		t.Fatalf("write stale binding: %v", err)
	}
	reqBody := runtimeSessionsV2OpenTestRequest("session-runtime-repair", "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	reqBody.SessionExecution.SourceWorkspaceID = snapshot.SourceWorkspaceID
	reqBody.SessionExecution.SourceWorkspaceGeneration = snapshot.SourceWorkspaceGeneration
	reqBody.Authority.SourceWorkspaceID = snapshot.SourceWorkspaceID
	reqBody.Authority.SourceWorkspaceGeneration = snapshot.SourceWorkspaceGeneration
	reqBody.SourceWorkspace.WorkspaceID = snapshot.SourceWorkspaceID
	reqBody.SourceWorkspace.WorkspaceGeneration = snapshot.SourceWorkspaceGeneration
	reqBody.DestinationRuntimeWorkspace.WorkspaceID = snapshot.SourceWorkspaceID
	reqBody.DestinationRuntimeWorkspace.WorkspaceGeneration = snapshot.SourceWorkspaceGeneration
	reqBody.BindingAuthoritySnapshot = &snapshot

	rec := postRuntimeSessionsV2Open(t, server, reqBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	repaired, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-container-v2")
	if err != nil || !ok {
		t.Fatalf("get repaired binding ok=%t err=%v", ok, err)
	}
	if repaired.SourceWorkspaceID != snapshot.SourceWorkspaceID || repaired.SourceWorkspaceGeneration != snapshot.SourceWorkspaceGeneration {
		t.Fatalf("binding source identity = %q/%d", repaired.SourceWorkspaceID, repaired.SourceWorkspaceGeneration)
	}
}

func TestRuntimeSessionsV2OpenRejectsBindingAuthoritySnapshotForWrongContainer(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedRuntimeSessionsV2OpenContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	reqBody := runtimeSessionsV2OpenTestRequest("session-runtime-repair-wrong-container", "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	binding, ok, err := server.topology.GetWorkspaceBindingForAccount(testPrincipal().AccountScopeID, "binding-container-v2")
	if err != nil || !ok {
		t.Fatalf("get binding ok=%t err=%v", ok, err)
	}
	binding.DestinationContainerID = "other-container"
	reqBody.BindingAuthoritySnapshot = &binding
	rec := postRuntimeSessionsV2Open(t, server, reqBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "destination container mismatch") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestRuntimeSessionsV2OpenAttachesExistingContainerLocalSession(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedRuntimeSessionsV2OpenContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")

	reqBody := runtimeSessionsV2OpenTestRequest("session-runtime-attach", "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	first := postRuntimeSessionsV2Open(t, server, reqBody)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	second := postRuntimeSessionsV2Open(t, server, reqBody)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	var payload sessionruntime.RuntimeSessionOpenResponse
	if err := json.Unmarshal(second.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode attach response: %v", err)
	}
	if payload.Status != "attached" || payload.SessionID != "session-runtime-attach" {
		t.Fatalf("unexpected attach response: %+v", payload)
	}
}

func TestRuntimeSessionsV2OpenRejectsFailClosedMismatches(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*sessionruntime.RuntimeSessionOpenRequest)
		wantStatus int
		wantBody   string
	}{
		{name: "primary class", mutate: func(req *sessionruntime.RuntimeSessionOpenRequest) {
			req.Authority.ExecutionClass = sessionruntime.SessionExecutionClassPrimary
		}, wantStatus: http.StatusBadRequest, wantBody: "local_container execution class"},
		{name: "runtime swarm mismatch", mutate: func(req *sessionruntime.RuntimeSessionOpenRequest) { req.Authority.RuntimeSwarmID = "other-container" }, wantStatus: http.StatusConflict, wantBody: "runtime swarm id mismatch"},
		{name: "host mismatch", mutate: func(req *sessionruntime.RuntimeSessionOpenRequest) { req.Authority.AuthorityHostSwarmID = "other-host" }, wantStatus: http.StatusConflict, wantBody: "authority host swarm id mismatch"},
		{name: "container mismatch", mutate: func(req *sessionruntime.RuntimeSessionOpenRequest) {
			req.Authority.AuthorityContainerID = "other-container-id"
		}, wantStatus: http.StatusConflict, wantBody: "authority container id mismatch"},
		{name: "binding generation mismatch", mutate: func(req *sessionruntime.RuntimeSessionOpenRequest) { req.Authority.BindingGeneration = 2 }, wantStatus: http.StatusConflict, wantBody: "binding generation mismatch"},
		{name: "source rewritten", mutate: func(req *sessionruntime.RuntimeSessionOpenRequest) { req.SourceWorkspace.WorkspacePath = "/host/other" }, wantStatus: http.StatusConflict, wantBody: "source workspace facts path mismatch"},
		{name: "runtime path missing", mutate: func(req *sessionruntime.RuntimeSessionOpenRequest) {
			req.DestinationRuntimeWorkspace.RuntimeWorkspacePath = ""
		}, wantStatus: http.StatusBadRequest, wantBody: "runtime workspace path is required"},
		{name: "runtime owner missing", mutate: func(req *sessionruntime.RuntimeSessionOpenRequest) {
			req.SessionExecution.RuntimeSwarmID = "container-ownerless"
			req.Authority.RuntimeSwarmID = "container-ownerless"
			req.Authority.DestinationRuntimeSwarmID = "container-ownerless"
			req.Authority.WorkspaceBindingID = "binding-ownerless-v2"
			req.SessionExecution.WorkspaceBindingID = "binding-ownerless-v2"
		}, wantStatus: http.StatusConflict, wantBody: "runtime owner identity is incomplete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			seedRuntimeSessionsV2OpenContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
			if tt.name == "runtime owner missing" {
				if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "container-ownerless", Name: "ownerless", Role: "child"}); err != nil {
					t.Fatalf("put ownerless local node: %v", err)
				}
				if _, err := server.topology.PutRuntimeForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "container-ownerless", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "ownerless", Relationship: "child", Status: "online"}); err != nil {
					t.Fatalf("put ownerless runtime: %v", err)
				}
				if _, err := server.topology.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "container-ownerless", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "host-swarm-id", AuthorityContainerID: "host-container-1", RuntimeKind: pebblestore.TopologyRuntimeKindContainer, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
					t.Fatalf("put ownerless placement: %v", err)
				}
				if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{BindingID: "binding-ownerless-v2", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, SourceWorkspaceID: "workspace-container-v2", SourceWorkspaceGeneration: 1, SourceWorkspacePath: "/host/swarm-go", SourceWorkspaceName: "swarm-go", DestinationRuntimeSwarmID: "container-ownerless", DestinationAuthorityHostSwarmID: "host-swarm-id", DestinationRuntimeKind: pebblestore.TopologyRuntimeKindContainer, DestinationContainerID: "host-container-1", DestinationWorkspacePath: "/workspaces/swarm-go", PlacementGeneration: 1, BindingGeneration: 1, State: pebblestore.TopologyWorkspaceBindingStateBound, AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, MaterializationKind: pebblestore.TopologyWorkspaceBindingMaterializationSource, AttestedByHostSwarmID: "host-swarm-id", Writable: true}); err != nil {
					t.Fatalf("upsert ownerless binding: %v", err)
				}
			}
			reqBody := runtimeSessionsV2OpenTestRequest("session-runtime-fail-"+strings.ReplaceAll(tt.name, " ", "-"), "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
			tt.mutate(&reqBody)
			rec := postRuntimeSessionsV2Open(t, server, reqBody)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func postRuntimeSessionsV2Open(t *testing.T, server *Server, body sessionruntime.RuntimeSessionOpenRequest) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal open request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, runtimeSessionsV2OpenPath, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, body.SessionExecution.AuthorityHostSwarmID)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}

func runtimeSessionsV2OpenTestRequest(sessionID, hostSwarmID, containerSwarmID, authorityContainerID, bindingID, sourceWorkspacePath, runtimeWorkspacePath string) sessionruntime.RuntimeSessionOpenRequest {
	execution := pebblestore.SessionExecutionV2Record{
		SessionID:                 sessionID,
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		ExecutionClass:            sessionruntime.SessionExecutionClassLocalContainer,
		RuntimeSwarmID:            containerSwarmID,
		RuntimeKind:               pebblestore.TopologyRuntimeKindContainer,
		AuthorityHostSwarmID:      hostSwarmID,
		AuthorityContainerID:      authorityContainerID,
		WorkspaceBindingID:        bindingID,
		SourceWorkspaceID:         "workspace-container-v2",
		SourceWorkspaceGeneration: 1,
		SourceWorkspaceName:       "swarm-go",
		SourceWorkspacePath:       sourceWorkspacePath,
		RuntimeWorkspacePath:      runtimeWorkspacePath,
		PlacementGeneration:       1,
		BindingGeneration:         1,
	}
	return sessionruntime.RuntimeSessionOpenRequest{
		SessionID: sessionID,
		Authority: sessionruntime.RuntimeSessionAuthority{
			SessionID:                 sessionID,
			UserID:                    testPrincipal().UserID,
			AccountScopeID:            testPrincipal().AccountScopeID,
			ExecutionClass:            execution.ExecutionClass,
			RuntimeSwarmID:            execution.RuntimeSwarmID,
			RuntimeKind:               execution.RuntimeKind,
			AuthorityHostSwarmID:      execution.AuthorityHostSwarmID,
			AuthorityContainerID:      execution.AuthorityContainerID,
			WorkspaceBindingID:        execution.WorkspaceBindingID,
			PlacementGeneration:       execution.PlacementGeneration,
			BindingGeneration:         execution.BindingGeneration,
			SourceWorkspaceID:         execution.SourceWorkspaceID,
			SourceWorkspaceGeneration: execution.SourceWorkspaceGeneration,
			SourceWorkspaceName:       execution.SourceWorkspaceName,
			SourceWorkspacePath:       execution.SourceWorkspacePath,
			DestinationRuntimeSwarmID: execution.RuntimeSwarmID,
			DestinationRuntimeKind:    execution.RuntimeKind,
			DestinationAuthorityHost:  execution.AuthorityHostSwarmID,
			DestinationContainerID:    execution.AuthorityContainerID,
			RuntimeWorkspacePath:      execution.RuntimeWorkspacePath,
		},
		SessionExecution: execution,
		SourceWorkspace: sessionruntime.RuntimeSessionWorkspaceFacts{
			WorkspaceID:          execution.SourceWorkspaceID,
			WorkspaceGeneration:  execution.SourceWorkspaceGeneration,
			WorkspaceName:        execution.SourceWorkspaceName,
			WorkspacePath:        execution.SourceWorkspacePath,
			RuntimeWorkspacePath: execution.SourceWorkspacePath,
		},
		DestinationRuntimeWorkspace: sessionruntime.RuntimeSessionWorkspaceFacts{
			WorkspaceID:          execution.SourceWorkspaceID,
			WorkspaceGeneration:  execution.SourceWorkspaceGeneration,
			WorkspaceName:        execution.SourceWorkspaceName,
			WorkspacePath:        execution.RuntimeWorkspacePath,
			RuntimeWorkspacePath: execution.RuntimeWorkspacePath,
		},
		Config: sessionruntime.RuntimeSessionConfig{
			Title:        "runtime open",
			Mode:         sessionruntime.ModeAuto,
			AgentName:    "swarm",
			WorktreeMode: "off",
			Preference: pebblestore.ModelPreference{
				Provider: "codex",
				Model:    "gpt-5.4",
				Thinking: "medium",
			},
			Metadata: map[string]any{"purpose": "runtime-open-test"},
		},
	}
}

func seedRuntimeSessionsV2OpenContainerAuthority(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, primarySwarmID, containerSwarmID, authorityContainerID, bindingID, sourceWorkspacePath, runtimeWorkspacePath string) {
	t.Helper()
	seedSessionsV2LocalContainerAuthority(t, server, swarmStore, primarySwarmID, containerSwarmID, authorityContainerID, bindingID, sourceWorkspacePath, runtimeWorkspacePath)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: containerSwarmID, Name: "container", Role: "child"}); err != nil {
		t.Fatalf("put runtime local node: %v", err)
	}
	runtimeRecord, ok, err := server.topology.GetRuntimeForAccount(testPrincipal().AccountScopeID, containerSwarmID)
	if err != nil || !ok {
		t.Fatalf("get container runtime ok=%t err=%v", ok, err)
	}
	runtimeRecord.OwnerHostSwarmID = primarySwarmID
	runtimeRecord.OwnerHostContainerID = authorityContainerID
	if _, err := server.topology.PutRuntimeForAccount(testPrincipal().AccountScopeID, runtimeRecord); err != nil {
		t.Fatalf("put runtime owner identity: %v", err)
	}
}

func TestRuntimeSessionsV2MirrorBatchRejectsAuthorityMismatches(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*sessionruntime.RuntimeSessionMirrorBatchRequest)
		wantStatus int
		wantBody   string
	}{
		{name: "wrong session id", mutate: func(batch *sessionruntime.RuntimeSessionMirrorBatchRequest) {
			batch.SessionID = "other-session"
		}, wantStatus: http.StatusConflict, wantBody: "batch session id mismatch"},
		{name: "wrong runtime swarm id", mutate: func(batch *sessionruntime.RuntimeSessionMirrorBatchRequest) {
			batch.Authority.RuntimeSwarmID = "other-runtime"
			batch.Authority.DestinationRuntimeSwarmID = "other-runtime"
		}, wantStatus: http.StatusConflict, wantBody: "runtime swarm id mismatch"},
		{name: "wrong authority container id", mutate: func(batch *sessionruntime.RuntimeSessionMirrorBatchRequest) {
			batch.Authority.AuthorityContainerID = "other-container"
		}, wantStatus: http.StatusConflict, wantBody: "authority container id mismatch"},
		{name: "source workspace overwrite", mutate: func(batch *sessionruntime.RuntimeSessionMirrorBatchRequest) {
			var payload sessionruntime.RuntimeSessionSnapshotMirrorItem
			mustDecodeMirrorPayloadForTest(t, batch.Items[0].Payload, &payload)
			payload.Session.Metadata["swarm_v2_source_workspace_id"] = "other-workspace"
			batch.Items[0].Payload = mirrorPayloadForTest(t, payload)
		}, wantStatus: http.StatusConflict, wantBody: "swarm_v2_source_workspace_id"},
		{name: "workspace binding overwrite", mutate: func(batch *sessionruntime.RuntimeSessionMirrorBatchRequest) {
			var payload sessionruntime.RuntimeSessionSnapshotMirrorItem
			mustDecodeMirrorPayloadForTest(t, batch.Items[0].Payload, &payload)
			payload.Session.Metadata["local_workspace_binding_id"] = "other-binding"
			batch.Items[0].Payload = mirrorPayloadForTest(t, payload)
		}, wantStatus: http.StatusConflict, wantBody: "local_workspace_binding_id"},
		{name: "runtime path as source path", mutate: func(batch *sessionruntime.RuntimeSessionMirrorBatchRequest) {
			var payload sessionruntime.RuntimeSessionSnapshotMirrorItem
			mustDecodeMirrorPayloadForTest(t, batch.Items[0].Payload, &payload)
			payload.Session.WorkspacePath = "/host/swarm-go"
			batch.Items[0].Payload = mirrorPayloadForTest(t, payload)
		}, wantStatus: http.StatusConflict, wantBody: "snapshot runtime workspace path mismatch"},
		{name: "malformed payload", mutate: func(batch *sessionruntime.RuntimeSessionMirrorBatchRequest) {
			batch.Items[0].Payload = json.RawMessage(`[]`)
		}, wantStatus: http.StatusBadRequest, wantBody: "invalid runtime session snapshot mirror item"},
		{name: "unknown type", mutate: func(batch *sessionruntime.RuntimeSessionMirrorBatchRequest) {
			batch.Items[0].Type = "authority.overwrite"
		}, wantStatus: http.StatusBadRequest, wantBody: "unknown runtime session mirror item type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
			sessionID := createRuntimeSessionsV2MirrorBatchTarget(t, server, swarmStore)
			execution, ok, err := server.sessions.Store().GetSessionExecutionV2(sessionID)
			if err != nil || !ok {
				t.Fatalf("get execution ok=%t err=%v", ok, err)
			}
			batch := runtimeSessionsV2MirrorBatchForTest(t, execution, true, false, false)
			tt.mutate(&batch)
			rec := postRuntimeSessionsV2MirrorBatch(t, server, sessionID, batch)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %s, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestRuntimeSessionsV2MirrorBatchStoresValidLifecycleMessageAndSnapshot(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createRuntimeSessionsV2MirrorBatchTarget(t, server, swarmStore)
	execution, ok, err := sessionSvc.Store().GetSessionExecutionV2(sessionID)
	if err != nil || !ok {
		t.Fatalf("get execution ok=%t err=%v", ok, err)
	}
	batch := runtimeSessionsV2MirrorBatchForTest(t, execution, true, true, true)
	rec := postRuntimeSessionsV2MirrorBatch(t, server, sessionID, batch)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response sessionruntime.RuntimeSessionMirrorBatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode mirror response: %v", err)
	}
	if !response.OK || response.SessionID != sessionID || response.Accepted != 3 {
		t.Fatalf("unexpected mirror response: %+v", response)
	}
	lifecycle, ok, err := sessionSvc.GetLifecycle(sessionID)
	if err != nil || !ok {
		t.Fatalf("get lifecycle ok=%t err=%v", ok, err)
	}
	if lifecycle.Phase != "active" || !lifecycle.Active {
		t.Fatalf("unexpected lifecycle: %+v", lifecycle)
	}
	messages, err := sessionSvc.ListMessages(sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	found := false
	for _, message := range messages {
		if message.GlobalSeq == 7 && message.Content == "mirrored message" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mirrored message not found: %+v", messages)
	}
	snapshot, ok, err := sessionSvc.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if snapshot.WorkspacePath != execution.RuntimeWorkspacePath || snapshot.Metadata["swarm_v2_runtime_workspace_path"] != execution.RuntimeWorkspacePath {
		t.Fatalf("unexpected mirrored snapshot: %+v", snapshot)
	}
}

func createRuntimeSessionsV2MirrorBatchTarget(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore) string {
	t.Helper()
	seedRuntimeSessionsV2OpenContainerAuthority(t, server, swarmStore, "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	reqBody := runtimeSessionsV2OpenTestRequest("session-runtime-mirror", "host-swarm-id", "container-swarm", "host-container-1", "binding-container-v2", "/host/swarm-go", "/workspaces/swarm-go")
	rec := postRuntimeSessionsV2Open(t, server, reqBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("open status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	return reqBody.SessionID
}

func runtimeSessionsV2MirrorBatchForTest(t *testing.T, execution pebblestore.SessionExecutionV2Record, includeSnapshot, includeLifecycle, includeMessage bool) sessionruntime.RuntimeSessionMirrorBatchRequest {
	t.Helper()
	exec := runtimeSessionsV2ExecutionFromRecord(execution)
	batch := sessionruntime.RuntimeSessionMirrorBatchRequest{
		SessionID:        execution.SessionID,
		Authority:        runtimeSessionOpenRequestFromFrozenExecution(execution, sessionruntime.SessionsV2CreateRequest{}).Authority,
		SessionExecution: execution,
	}
	if includeSnapshot {
		snapshot := pebblestore.SessionSnapshot{
			ID:             execution.SessionID,
			UserID:         execution.UserID,
			AccountScopeID: execution.AccountScopeID,
			WorkspacePath:  execution.RuntimeWorkspacePath,
			WorkspaceName:  execution.SourceWorkspaceName,
			Title:          "mirrored snapshot",
			Mode:           sessionruntime.ModeAuto,
			Metadata:       sessionruntime.RuntimeSessionV2Metadata(map[string]any{"mirror_safe": "ok"}, exec),
			CreatedAt:      100,
			UpdatedAt:      200,
		}
		batch.Items = append(batch.Items, sessionruntime.RuntimeSessionMirrorItem{Type: sessionruntime.RuntimeSessionMirrorTypeSessionSnapshot, SessionID: execution.SessionID, Payload: mirrorPayloadForTest(t, sessionruntime.RuntimeSessionSnapshotMirrorItem{Session: snapshot})})
	}
	if includeLifecycle {
		lifecycle := pebblestore.SessionLifecycleSnapshot{SessionID: execution.SessionID, UserID: execution.UserID, AccountScopeID: execution.AccountScopeID, RunID: "run-mirror", Active: true, Phase: "active", Generation: 1}
		batch.Items = append(batch.Items, sessionruntime.RuntimeSessionMirrorItem{Type: sessionruntime.RuntimeSessionMirrorTypeSessionLifecycle, SessionID: execution.SessionID, Payload: mirrorPayloadForTest(t, sessionruntime.RuntimeSessionLifecycleMirrorItem{Lifecycle: lifecycle})})
	}
	if includeMessage {
		message := pebblestore.MessageSnapshot{SessionID: execution.SessionID, UserID: execution.UserID, AccountScopeID: execution.AccountScopeID, GlobalSeq: 7, Role: "assistant", Content: "mirrored message", CreatedAt: 300}
		batch.Items = append(batch.Items, sessionruntime.RuntimeSessionMirrorItem{Type: sessionruntime.RuntimeSessionMirrorTypeMessageStored, SessionID: execution.SessionID, Payload: mirrorPayloadForTest(t, sessionruntime.RuntimeSessionMessageStoredMirrorItem{Message: message})})
	}
	return batch
}

func postRuntimeSessionsV2MirrorBatch(t *testing.T, server *Server, sessionID string, batch sessionruntime.RuntimeSessionMirrorBatchRequest) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal mirror batch: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, runtimeSessionsV2Prefix+sessionID+"/mirror/batch", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), localTransportAuthEnabledKey, true))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: batch.SessionExecution.RuntimeSwarmID}))
	req.Header.Set(peerAuthSwarmIDHeader, batch.SessionExecution.RuntimeSwarmID)
	if principal, ok := server.trustedRuntimeSessionV2PrincipalForPeerRequest(req, sessionID); ok {
		req = withSessionsV2TestPrincipal(req, principal)
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func mirrorPayloadForTest(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal mirror payload: %v", err)
	}
	return raw
}

func mustDecodeMirrorPayloadForTest(t *testing.T, raw json.RawMessage, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode mirror payload: %v", err)
	}
}

func TestRuntimeSessionsV2MirrorBatchRequiresRuntimePeerTransport(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	sessionID := createRuntimeSessionsV2MirrorBatchTarget(t, server, swarmStore)
	execution, ok, err := server.sessions.Store().GetSessionExecutionV2(sessionID)
	if err != nil || !ok {
		t.Fatalf("get execution ok=%t err=%v", ok, err)
	}
	batch := runtimeSessionsV2MirrorBatchForTest(t, execution, true, false, false)
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal mirror batch: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, runtimeSessionsV2Prefix+sessionID+"/mirror/batch", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: execution.AuthorityHostSwarmID}))
	req.Header.Set(peerAuthSwarmIDHeader, execution.AuthorityHostSwarmID)
	if principal, ok := server.trustedRuntimeSessionV2PrincipalForPeerRequest(req, sessionID); ok {
		req = withSessionsV2TestPrincipal(req, principal)
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "trusted runtime peer transport") {
		t.Fatalf("body = %s, want runtime peer transport rejection", rec.Body.String())
	}
}
