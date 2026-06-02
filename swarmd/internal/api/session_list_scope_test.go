package api

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestListSessionsForCWDUsesExactPathWhenNoWorkspaceMatches(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions-unmatched-cwd.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	savedWorkspace := t.TempDir()
	if _, err := workspaceSvc.AddForPrincipal(principal, savedWorkspace, "saved", "", true); err != nil {
		t.Fatalf("add saved workspace: %v", err)
	}

	nonWorkspaceCWD := t.TempDir()
	nestedCWD := filepath.Join(nonWorkspaceCWD, "nested")
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	nonWorkspaceSession := createSessionForListScopeTest(t, sessionSvc, principal, nonWorkspaceCWD)
	_ = createSessionForListScopeTest(t, sessionSvc, principal, nestedCWD)
	_ = createSessionForListScopeTest(t, sessionSvc, principal, savedWorkspace)

	sessions, err := listSessionsForCWD(sessionSvc, workspaceSvc, principal, nonWorkspaceCWD, 100, false)
	if err != nil {
		t.Fatalf("list sessions for cwd: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 exact-CWD session for unmatched cwd, got %d: %#v", len(sessions), sessions)
	}
	if sessions[0].ID != nonWorkspaceSession.ID {
		t.Fatalf("session id = %q, want %q", sessions[0].ID, nonWorkspaceSession.ID)
	}
}

func createSessionForListScopeTest(t *testing.T, svc *sessionruntime.Service, principal identity.Principal, workspacePath string) pebblestore.SessionSnapshot {
	t.Helper()
	session, _, err := svc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		Title:          "List scope",
		WorkspacePath:  workspacePath,
		WorkspaceName:  filepath.Base(workspacePath),
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session for %q: %v", workspacePath, err)
	}
	return session
}

func TestListSessionsForCWDUsesWorkspaceBindingAuthorityForContainerSession(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "sessions-container-cwd.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspaceruntime.NewService(workspaceStore)
	savedWorkspace := t.TempDir()
	workspaceResolution, err := workspaceSvc.AddForPrincipal(principal, savedWorkspace, "saved", "", true)
	if err != nil {
		t.Fatalf("add saved workspace: %v", err)
	}

	topologyStore := pebblestore.NewTopologyStore(store)
	topologySvc := topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil, workspaceStore)
	if _, err := topologyStore.PutRuntimePlacementForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "container-swarm",
		AccountScopeID:       principal.AccountScopeID,
		AuthorityHostSwarmID: "host-swarm",
		AuthorityContainerID: "container-1",
		RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
	}); err != nil {
		t.Fatalf("put runtime placement: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(principal.AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-container-v2",
		UserID:                          principal.UserID,
		AccountScopeID:                  principal.AccountScopeID,
		SourceWorkspaceID:               workspaceResolution.WorkspaceID,
		SourceWorkspaceGeneration:       workspaceResolution.WorkspaceGeneration,
		SourceWorkspacePath:             savedWorkspace,
		SourceWorkspaceName:             "saved",
		DestinationRuntimeSwarmID:       "container-swarm",
		DestinationAuthorityHostSwarmID: "host-swarm",
		DestinationRuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		DestinationContainerID:          "container-1",
		DestinationWorkspacePath:        "/workspaces/swarm-go",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID:           "host-swarm",
		Writable:                        true,
	}); err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	containerSession := pebblestore.SessionSnapshot{
		ID:             "session-container-v2",
		UserID:         principal.UserID,
		AccountScopeID: principal.AccountScopeID,
		WorkspacePath:  "/workspaces/swarm-go",
		WorkspaceName:  "saved",
		Title:          "Container v2",
		Mode:           sessionruntime.ModeAuto,
		Preference:     pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model", Thinking: "medium"},
		CreatedAt:      10,
		UpdatedAt:      10,
	}
	if err := sessionSvc.Store().CreateSessionWithExecutionV2(containerSession, pebblestore.SessionExecutionV2Record{
		SessionID:                 containerSession.ID,
		UserID:                    principal.UserID,
		AccountScopeID:            principal.AccountScopeID,
		ExecutionClass:            sessionruntime.SessionExecutionClassLocalContainer,
		RuntimeSwarmID:            "container-swarm",
		RuntimeKind:               pebblestore.TopologyRuntimeKindContainer,
		AuthorityHostSwarmID:      "host-swarm",
		AuthorityContainerID:      "container-1",
		WorkspaceBindingID:        "binding-container-v2",
		SourceWorkspaceID:         workspaceResolution.WorkspaceID,
		SourceWorkspaceGeneration: workspaceResolution.WorkspaceGeneration,
		SourceWorkspaceName:       "saved",
		SourceWorkspacePath:       savedWorkspace,
		RuntimeWorkspacePath:      "/workspaces/swarm-go",
		PlacementGeneration:       1,
		BindingGeneration:         1,
		CreatedAt:                 containerSession.CreatedAt,
		UpdatedAt:                 containerSession.UpdatedAt,
	}); err != nil {
		t.Fatalf("create container session execution: %v", err)
	}
	_ = createSessionForListScopeTest(t, sessionSvc, principal, savedWorkspace)

	sessions, err := listSessionsForCWDWithTopology(sessionSvc, workspaceSvc, topologySvc, principal, savedWorkspace, 100, false)
	if err != nil {
		t.Fatalf("list sessions for cwd: %v", err)
	}
	found := false
	for _, session := range sessions {
		if session.ID == containerSession.ID {
			found = true
			if session.WorkspacePath != "/workspaces/swarm-go" {
				t.Fatalf("container session workspace path = %q", session.WorkspacePath)
			}
		}
	}
	if !found {
		t.Fatalf("container session missing from CWD list: %#v", sessions)
	}

	exactSessions, err := listSessionsForCWDWithTopology(sessionSvc, workspaceSvc, topologySvc, principal, savedWorkspace, 100, true)
	if err != nil {
		t.Fatalf("exact list sessions for cwd: %v", err)
	}
	for _, session := range exactSessions {
		if session.ID == containerSession.ID {
			t.Fatalf("exact path list included runtime-path container session: %#v", exactSessions)
		}
	}
}
