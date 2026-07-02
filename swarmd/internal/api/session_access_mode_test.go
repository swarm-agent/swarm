package api

import (
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestSessionBindingWriteAccessRejectsReadOnlyBinding(t *testing.T) {
	server, sessionSvc := newSessionAccessModeTestServer(t, pebblestore.TopologyWorkspaceBindingAccessModeReadOnly)
	if _, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-read-only",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/runtime/workspace",
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5", Thinking: "medium"},
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := server.enforceSessionBindingWriteAccess(testPrincipal(), "session-read-only", "append message"); err == nil {
		t.Fatal("expected read-only binding to reject write access")
	}
}

func TestSessionBindingWriteAccessAllowsReadWriteBinding(t *testing.T) {
	server, sessionSvc := newSessionAccessModeTestServer(t, pebblestore.TopologyWorkspaceBindingAccessModeReadWrite)
	if _, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-read-write",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/runtime/workspace",
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference:     &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5", Thinking: "medium"},
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := server.enforceSessionBindingWriteAccess(testPrincipal(), "session-read-write", "append message"); err != nil {
		t.Fatalf("read-write binding rejected: %v", err)
	}
}

func newSessionAccessModeTestServer(t *testing.T, accessMode string) (*Server, *sessionruntime.Service) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "session-access-mode.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	server := NewServer(nil, nil, nil, nil, sessionSvc, nil, nil, nil, nil, nil, nil, events, stream.NewHub(events))
	topologyStore := pebblestore.NewTopologyStore(store)
	server.SetTopologyService(topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil))
	server.SetSessionRouteStore(pebblestore.NewSessionRouteStore(store))
	accountID := testPrincipal().AccountScopeID
	userID := testPrincipal().UserID
	if _, err := topologyStore.PutRuntimeForAccount(accountID, pebblestore.TopologyRuntimeRecord{SwarmID: "local-swarm", AccountScopeID: accountID, UserID: userID, Name: "local", Status: "online"}); err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(accountID, pebblestore.TopologyRuntimePlacementRecord{
		PlacementID:          "placement-local",
		RuntimeSwarmID:       "local-swarm",
		AccountScopeID:       accountID,
		AuthorityHostSwarmID: "local-swarm",
		RuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
	}); err != nil {
		t.Fatalf("put placement: %v", err)
	}
	binding, err := topologyStore.PutWorkspaceBindingForAccount(accountID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-access-mode",
		UserID:                          userID,
		AccountScopeID:                  accountID,
		SourceWorkspaceID:               "workspace-id",
		SourceWorkspaceGeneration:       1,
		SourceWorkspacePath:             "/source/workspace",
		SourceWorkspaceName:             "workspace",
		DestinationRuntimeSwarmID:       "local-swarm",
		DestinationAuthorityHostSwarmID: "local-swarm",
		DestinationHostSwarmID:          "local-swarm",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		DestinationWorkspacePath:        "/runtime/workspace",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      accessMode,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           "local-swarm",
		Writable:                        accessMode == pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
	})
	if err != nil {
		t.Fatalf("put binding: %v", err)
	}
	if _, err := server.sessionRoutes.Put(pebblestore.SessionRouteRecord{
		SessionID:            "session-read-only",
		UserID:               userID,
		AccountScopeID:       accountID,
		ChildSwarmID:         "local-swarm",
		HostSwarmID:          "local-swarm",
		RuntimeWorkspacePath: "/runtime/workspace",
		WorkspaceBindingID:   binding.BindingID,
		PlacementGeneration:  1,
		BindingGeneration:    1,
	}); err != nil {
		t.Fatalf("put read-only route: %v", err)
	}
	if _, err := server.sessionRoutes.Put(pebblestore.SessionRouteRecord{
		SessionID:            "session-read-write",
		UserID:               userID,
		AccountScopeID:       accountID,
		ChildSwarmID:         "local-swarm",
		HostSwarmID:          "local-swarm",
		RuntimeWorkspacePath: "/runtime/workspace",
		WorkspaceBindingID:   binding.BindingID,
		PlacementGeneration:  1,
		BindingGeneration:    1,
	}); err != nil {
		t.Fatalf("put read-write route: %v", err)
	}
	return server, sessionSvc
}
