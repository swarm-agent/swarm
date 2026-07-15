package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV2PrimaryCreatesLocalSessionFromBindingAuthority(t *testing.T) {
	server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")

	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"},"metadata":{"purpose":"test"}}`)
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
	if !payload.OK || strings.TrimSpace(payload.Session.ID) == "" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Session.WorkspacePath != "/host/swarm-go" || payload.Session.UserID != testPrincipal().UserID || payload.Session.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("session = %+v", payload.Session)
	}
	if payload.SessionExecution.ExecutionClass != sessionruntime.SessionExecutionClassPrimary || payload.SessionExecution.RuntimeSwarmID != "host-swarm-id" || payload.SessionExecution.AuthorityHostSwarmID != "host-swarm-id" || payload.SessionExecution.WorkspaceBindingID != "binding-primary-v2" {
		t.Fatalf("execution = %+v", payload.SessionExecution)
	}
	if _, ok, err := sessionSvc.Store().GetSessionExecutionV2(payload.Session.ID); err != nil || !ok {
		t.Fatalf("stored execution ok=%t err=%v", ok, err)
	}
	if routes, err := routeStore.List(10); err != nil || len(routes) != 0 {
		t.Fatalf("routes = %+v err=%v, want no routed record", routes, err)
	}
}

func TestSessionsV2PrimaryRejectsNonSelfRuntime(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "other-runtime", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Relationship: "child", Status: "online"}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := server.topology.PutRuntimePlacementForAccount(testPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "other-runtime", AccountScopeID: testPrincipal().AccountScopeID, AuthorityHostSwarmID: "host-swarm-id", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put placement: %v", err)
	}
	rec := postSessionsV2Primary(t, server, `{"swarm_id":"other-runtime","workspace_binding_id":"binding-primary-v2","title":"bad","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "is not the primary runtime") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsStaleBindingPlacementGeneration(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthorityWithBindingGeneration(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go", 1, 2)
	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"stale","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "generation does not match placement") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func TestSessionsV2PrimaryRejectsRoutingAuthorityFields(t *testing.T) {
	server, _, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	rec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","backend_url":"http://127.0.0.1:9","title":"bad","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "routing authority field") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertNoPrimaryCreateResidue(t, server, routeStore)
}

func postSessionsV2Primary(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v2/sessions/primary", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	return rec
}

func seedSessionsV2PrimaryAuthority(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, swarmID, bindingID, workspacePath string) {
	t.Helper()
	seedSessionsV2PrimaryAuthorityWithBindingGeneration(t, server, swarmStore, swarmID, bindingID, workspacePath, 1, 1)
}

func seedSessionsV2PrimaryAuthorityWithBindingGeneration(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, swarmID, bindingID, workspacePath string, placementGeneration, bindingPlacementGeneration int) {
	t.Helper()
	if server == nil || server.topology == nil || swarmStore == nil {
		t.Fatal("server topology and swarm store are required")
	}
	now := time.Now().UnixMilli()
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: swarmID, Name: "host-swarm", Role: "primary", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: swarmID, UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "host-swarm", Role: "primary", Relationship: "self", Status: "online", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert local runtime: %v", err)
	}
	if _, err := server.topology.EnsureLocalSelfPlacementForPrincipal(testPrincipal().AccountScopeID, testPrincipal().UserID); err != nil {
		t.Fatalf("ensure self placement: %v", err)
	}
	if placementGeneration != 1 {
		t.Fatalf("test helper only supports self placement generation 1, got %d", placementGeneration)
	}
	legacyTargetKind := ""
	if bindingPlacementGeneration != placementGeneration {
		legacyTargetKind = "stale-primary-v2-test"
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       bindingID,
		UserID:                          testPrincipal().UserID,
		AccountScopeID:                  testPrincipal().AccountScopeID,
		SourceWorkspaceID:               "workspace-primary-v2",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             workspacePath,
		SourceWorkspaceName:             "swarm-go",
		DestinationRuntimeSwarmID:       swarmID,
		DestinationAuthorityHostSwarmID: swarmID,
		DestinationHostSwarmID:          swarmID,
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationWorkspacePath:        workspacePath,
		PlacementGeneration:             bindingPlacementGeneration,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           swarmID,
		Writable:                        true,
		LegacyTargetKind:                legacyTargetKind,
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}
}
