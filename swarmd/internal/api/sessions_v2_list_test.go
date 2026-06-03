package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionsV2ListRejectsInvalidQueryModes(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "missing mode", path: "/v2/sessions"},
		{name: "mixed modes", path: "/v2/sessions?cwd=/tmp/workspace&workspace_binding_id=binding-primary-v2"},
		{name: "invalid limit", path: "/v2/sessions?cwd=/tmp/workspace&limit=0"},
		{name: "unknown parameter", path: "/v2/sessions?cwd=/tmp/workspace&exact_path=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, withTestPrincipal(req))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestSessionsV2ListCWDUsesExactAccountPathOnly(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	principal := testPrincipal()

	exact := createSessionForListScopeTest(t, sessionSvc, principal, "/tmp/cwd-only")
	_ = createSessionForListScopeTest(t, sessionSvc, principal, "/tmp/cwd-only/nested")
	_ = createSessionForListScopeTest(t, sessionSvc, principal, "/host/swarm-go")

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions?cwd=/tmp/cwd-only", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload := decodeSessionsV2ListTestResponse(t, rec)
	if len(payload.Sessions) != 1 || payload.Sessions[0].ID != exact.ID {
		t.Fatalf("sessions = %+v, want only %q", payload.Sessions, exact.ID)
	}
}

func TestSessionsV2ListWorkspaceBindingReturnsWorkspaceBindingSet(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	principal := testPrincipal()

	primarySession := pebblestore.SessionSnapshot{
		ID:             "session-primary-v2-list",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  "/host/swarm-go",
		WorkspaceName:  "swarm-go",
		Title:          "Primary v2",
		Mode:           sessionruntime.ModeAuto,
		Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "medium"},
		CreatedAt:      10,
		UpdatedAt:      10,
	}
	if err := sessionSvc.Store().CreateSessionWithExecutionV2(primarySession, pebblestore.SessionExecutionV2Record{
		SessionID:                 primarySession.ID,
		UserID:                    principal.UserID,
		AccountScopeID:            principal.AccountScopeID,
		ExecutionClass:            sessionruntime.SessionExecutionClassPrimary,
		RuntimeSwarmID:            "host-swarm-id",
		RuntimeKind:               pebblestore.TopologyRuntimeKindHost,
		AuthorityHostSwarmID:      "host-swarm-id",
		WorkspaceBindingID:        "binding-primary-v2",
		SourceWorkspaceID:         "workspace-primary-v2",
		SourceWorkspaceGeneration: 1,
		SourceWorkspaceName:       "swarm-go",
		SourceWorkspacePath:       "/host/swarm-go",
		RuntimeWorkspacePath:      "/host/swarm-go",
		PlacementGeneration:       1,
		BindingGeneration:         1,
		CreatedAt:                 primarySession.CreatedAt,
		UpdatedAt:                 primarySession.UpdatedAt,
	}); err != nil {
		t.Fatalf("create primary execution: %v", err)
	}

	now := time.Now().UnixMilli()
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "container-swarm",
		UserID:               principal.UserID,
		AccountScopeID:       principal.AccountScopeID,
		Name:                 "container",
		Relationship:         "child",
		Status:               "online",
		OwnerHostSwarmID:     "host-swarm-id",
		OwnerHostContainerID: "container-1",
		CreatedAt:            now,
		UpdatedAt:            now,
	}); err != nil {
		t.Fatalf("upsert container runtime: %v", err)
	}
	if _, err := server.topology.PutRuntimePlacementForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "container-swarm",
		AccountScopeID:       principal.AccountScopeID,
		AuthorityHostSwarmID: "host-swarm-id",
		AuthorityContainerID: "container-1",
		RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
		CreatedAt:            now,
		UpdatedAt:            now,
	}); err != nil {
		t.Fatalf("put container placement: %v", err)
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-container-v2",
		UserID:                          principal.UserID,
		AccountScopeID:                  principal.AccountScopeID,
		SourceWorkspaceID:               "workspace-primary-v2",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             "/host/swarm-go",
		SourceWorkspaceName:             "swarm-go",
		DestinationRuntimeSwarmID:       "container-swarm",
		DestinationAuthorityHostSwarmID: "host-swarm-id",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationContainerID:          "container-1",
		DestinationWorkspacePath:        "/workspaces/swarm-go",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           "host-swarm-id",
		Writable:                        true,
	}); err != nil {
		t.Fatalf("upsert container binding: %v", err)
	}
	containerSession := pebblestore.SessionSnapshot{
		ID:             "session-container-v2-list",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  "/workspaces/swarm-go",
		WorkspaceName:  "swarm-go",
		Title:          "Container v2",
		Mode:           sessionruntime.ModeAuto,
		Preference:     pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "medium"},
		CreatedAt:      11,
		UpdatedAt:      11,
	}
	if err := sessionSvc.Store().CreateSessionWithExecutionV2(containerSession, pebblestore.SessionExecutionV2Record{
		SessionID:                 containerSession.ID,
		UserID:                    principal.UserID,
		AccountScopeID:            principal.AccountScopeID,
		ExecutionClass:            sessionruntime.SessionExecutionClassLocalContainer,
		RuntimeSwarmID:            "container-swarm",
		RuntimeKind:               pebblestore.TopologyRuntimeKindContainer,
		AuthorityHostSwarmID:      "host-swarm-id",
		AuthorityContainerID:      "container-1",
		WorkspaceBindingID:        "binding-container-v2",
		SourceWorkspaceID:         "workspace-primary-v2",
		SourceWorkspaceGeneration: 1,
		SourceWorkspaceName:       "swarm-go",
		SourceWorkspacePath:       "/host/swarm-go",
		RuntimeWorkspacePath:      "/workspaces/swarm-go",
		PlacementGeneration:       1,
		BindingGeneration:         1,
		CreatedAt:                 containerSession.CreatedAt,
		UpdatedAt:                 containerSession.UpdatedAt,
	}); err != nil {
		t.Fatalf("create container execution: %v", err)
	}
	fallbackOnly := createSessionForListScopeTest(t, sessionSvc, principal, "/host/swarm-go/nested")

	req := httptest.NewRequest(http.MethodGet, "/v2/sessions?workspace_binding_id=binding-primary-v2", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload := decodeSessionsV2ListTestResponse(t, rec)
	ids := sessionsV2ListTestIDs(payload.Sessions)
	if !ids[primarySession.ID] || !ids[containerSession.ID] {
		t.Fatalf("sessions = %+v, want primary and container", payload.Sessions)
	}
	if ids[fallbackOnly.ID] {
		t.Fatalf("sessions included fallback scope-only session %q: %+v", fallbackOnly.ID, payload.Sessions)
	}
}

func TestSessionsV2ListWorkspaceBindingMissingTopologyFailsClosed(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	req := httptest.NewRequest(http.MethodGet, "/v2/sessions?workspace_binding_id=missing-binding", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "was not found") {
		t.Fatalf("body = %s, want not found error", rec.Body.String())
	}
}

func decodeSessionsV2ListTestResponse(t *testing.T, rec *httptest.ResponseRecorder) struct {
	OK       bool                          `json:"ok"`
	Sessions []pebblestore.SessionSnapshot `json:"sessions"`
} {
	t.Helper()
	var payload struct {
		OK       bool                          `json:"ok"`
		Sessions []pebblestore.SessionSnapshot `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return payload
}

func sessionsV2ListTestIDs(sessions []pebblestore.SessionSnapshot) map[string]bool {
	ids := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		ids[session.ID] = true
	}
	return ids
}
