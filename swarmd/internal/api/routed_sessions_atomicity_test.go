package api

import (
	"context"
	"encoding/json"

	"net/http/httptest"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/modelprofile"
	"swarm/packages/swarmd/internal/permission"

	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/tool"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type routedSessionAtomicityResponse struct {
	OK           bool                                 `json:"ok"`
	SessionID    string                               `json:"session_id"`
	Session      pebblestore.SessionSnapshot          `json:"session"`
	SessionView  sessionsV3SessionView                `json:"session_view"`
	Projection   sessionruntime.SessionProjection     `json:"projection"`
	FirstMessage pebblestore.MessageSnapshot          `json:"first_message"`
	Mutation     sessionruntime.SessionMutationResult `json:"mutation"`
	StartingMode string                               `json:"starting_mode"`
	Replayed     bool                                 `json:"replayed"`
}

func decodeRoutedSessionAtomicityResponse(t *testing.T, recorder *httptest.ResponseRecorder) routedSessionAtomicityResponse {
	t.Helper()
	var response routedSessionAtomicityResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode routed response: %v body=%s", err, recorder.Body.String())
	}
	return response
}

func addRoutedSessionTestAuthority(body map[string]any) {
	if _, ok := body["workspace_binding_id"]; !ok {
		body["workspace_binding_id"] = "routed-binding"
	}
	if _, ok := body["swarm_id"]; !ok {
		body["swarm_id"] = "local-swarm"
	}
	if _, ok := body["target_kind"]; !ok {
		body["target_kind"] = "host"
	}
	if _, ok := body["target_relationship"]; !ok {
		body["target_relationship"] = "self"
	}
}

func assertNoRoutedSessionDurableAuthority(t *testing.T, sessions *sessionruntime.Service, principal identity.Principal, sessionID, requestID string) {
	t.Helper()
	if stored, ok, err := sessions.GetSession(sessionID); err != nil || ok {
		t.Fatalf("failed routed start durable session exists=%t session=%+v err=%v", ok, stored, err)
	}
	listed, err := sessions.ListSessionsForAccount(principal.AccountScopeID, 10)
	if err != nil || len(listed) != 0 {
		t.Fatalf("failed routed start sessions=%+v err=%v", listed, err)
	}
	messages, err := sessions.ListSessionMessages(sessionID, 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("failed routed start messages=%+v err=%v", messages, err)
	}
	if projection, ok, err := sessions.GetSessionProjection(sessionID); err != nil || ok {
		t.Fatalf("failed routed start projection exists=%t projection=%+v err=%v", ok, projection, err)
	}
	runID := stableSessionsV3PrimaryRunID(sessionID, "routed-message:"+requestID)
	if intent, ok, err := sessions.Store().GetV3SessionRunIntent(sessionID, runID); err != nil || ok {
		t.Fatalf("failed routed start run intent exists=%t intent=%+v err=%v", ok, intent, err)
	}
	if record, ok, err := sessions.Store().GetV3SessionOperationIdempotencyRecord(principal.AccountScopeID, sessionID, sessionruntime.SessionMutationCreateSession, requestID); err != nil || ok {
		t.Fatalf("failed routed start idempotency exists=%t record=%+v err=%v", ok, record, err)
	}
	// The fixture registers workspace catalog entries before the request, which
	// legitimately emit account outbox records. The failed session must publish
	// neither events nor its own realtime authority at the mutation boundary.
	if events, err := sessions.ListSessionEvents(sessionID, 0, 10); err != nil || len(events) != 0 {
		t.Fatalf("failed routed start events=%+v err=%v", events, err)
	}
	if outbox, err := sessions.Store().ListV3RealtimeOutboxForSessionAfterEndpoint(sessionID, 0, 10); err != nil || len(outbox) != 0 {
		t.Fatalf("failed routed start outbox=%+v err=%v", outbox, err)
	}
}

func newRoutedSessionAtomicityServer(t *testing.T, routerRunner *sessionRouterRecordingRunner, planEnabled, workspaceEnabled bool) (*Server, *sessionruntime.Service, identity.Principal) {
	t.Helper()
	t.Setenv("SWARM_V3_DIAGNOSTICS", "0")
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "routed-session-atomicity.pebble"))
	if err != nil {
		t.Fatalf("open routed atomicity store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("create routed atomicity event log: %v", err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "routed-user", AccountScopeID: "routed-account", AccountScopeSource: identity.AccountScopeSourceServerState}

	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatalf("initialize empty repository history: %v", err)
	}
	sessionService := sessionruntime.NewService(sessionStore, eventLog)
	catalogStore := pebblestore.NewModelCatalogStore(store)
	for _, record := range []pebblestore.ModelCatalogRecord{
		{Provider: routerRunner.id, Model: "router-model", ThinkingOptions: []string{"high"}, ServiceTiers: []string{"priority"}},
		{Provider: "recording", Model: "action-model", ThinkingOptions: []string{"low", "medium", "high"}},
		{Provider: "recording", Model: "plan-model", ThinkingOptions: []string{"high"}},
		{Provider: "compact", Model: "compact-model", ThinkingOptions: []string{"low"}},
	} {
		if err := catalogStore.SetRecord(record); err != nil {
			t.Fatalf("seed routed model catalog: %v", err)
		}
	}
	modelService := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, modelruntime.NewCatalogService(catalogStore))
	permissionService := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissionService.SetSessionResolver(sessionService)
	agentService := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentService.EnsureDefaultsForAccount(principal.AccountScopeID); err != nil {
		t.Fatalf("ensure account agent defaults: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(routerRunner)
	runService := runruntime.NewService(sessionService, modelService, providers, tool.NewRuntime(1), permissionService, agentService, nil, nil)
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceService := workspace.NewService(workspaceStore)
	// The deployment bridge resolves source repository identity before publishing
	// a session. Use a real committed repository so routing/rollback assertions
	// reach that boundary instead of failing on a nonexistent catalog path.
	workspacePath := t.TempDir()
	runGitCommitTestCommand(t, workspacePath, "init", "-b", "dev")
	runGitCommitTestCommand(t, workspacePath, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	workspaceID := ""
	workspaceGeneration := int64(0)
	if workspaceEnabled {
		entry, err := workspaceStore.AddForAccount(principal.AccountScopeID, workspacePath, "Routed Workspace")
		if err != nil {
			t.Fatalf("add routed workspace: %v", err)
		}
		workspaceID = entry.WorkspaceID
		workspaceGeneration = entry.WorkspaceGeneration
		pending, err := workspaceStore.MarkDefinitionPendingForAccount(principal.AccountScopeID, entry.Path)
		if err != nil {
			t.Fatalf("mark routed workspace definition pending: %v", err)
		}
		if _, current, err := workspaceStore.CompleteDefinitionForAccount(principal.AccountScopeID, entry.Path, pending.DefinitionGeneration, "Routed atomicity workspace", 1); err != nil || !current {
			t.Fatalf("complete routed workspace definition current=%t err=%v", current, err)
		}
	}

	server := NewServer(nil, agentService, modelService, runService, sessionService, workspaceService, nil, nil, providers, permissionService, nil, eventLog, stream.NewHub(eventLog))
	server.v3SessionExecutor = nil
	favoriteStore := pebblestore.NewModelProfileStore(store)
	favoriteService := modelprofile.NewService(favoriteStore)
	authorityContext := identity.ContextWithPrincipal(context.Background(), principal)
	actionFavorite, err := favoriteService.Create(authorityContext, modelprofile.Input{Name: "Action", Provider: "recording", Model: "action-model", Thinking: "medium"})
	if err != nil {
		t.Fatalf("create Action favorite: %v", err)
	}
	planSelection := pebblestore.ModelProfileSelection{Provider: actionFavorite.Provider, Model: actionFavorite.Model, Thinking: actionFavorite.Thinking}
	if planEnabled {
		planFavorite, err := favoriteService.Create(authorityContext, modelprofile.Input{Name: "Plan", Provider: "recording", Model: "plan-model", Thinking: "high"})
		if err != nil {
			t.Fatalf("create Plan favorite: %v", err)
		}
		planSelection = pebblestore.ModelProfileSelection{Provider: planFavorite.Provider, Model: planFavorite.Model, Thinking: planFavorite.Thinking}
	}
	server.SetModelProfileService(favoriteService)
	agentSettingsStore := pebblestore.NewAgentModelSettingsStore(store)
	agentSettings := testAgentModelSettingsRecord(principal.AccountScopeID)
	agentSettings.Swarm.Action = pebblestore.AgentModelAssignment{Provider: actionFavorite.Provider, Model: actionFavorite.Model, Thinking: actionFavorite.Thinking}
	agentSettings.Swarm.Plan = pebblestore.AgentModelAssignment{Provider: planSelection.Provider, Model: planSelection.Model, Thinking: planSelection.Thinking}
	agentSettings.SystemAgents.Router = pebblestore.AgentModelAssignment{Provider: routerRunner.id, Model: "router-model", Thinking: "high", ServiceTier: "priority"}
	if _, err := agentSettingsStore.PutForAccount(agentSettings); err != nil {
		t.Fatalf("configure canonical agent model settings: %v", err)
	}
	server.SetAgentModelSettingsService(agentmodelsettings.NewService(agentSettingsStore))

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
		t.Fatalf("put local swarm node: %v", err)
	}
	if _, err := topologyStore.PutRuntimeForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "local-swarm", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Name: "Local"}); err != nil {
		t.Fatalf("put local runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "local-swarm", AccountScopeID: principal.AccountScopeID, AuthorityHostSwarmID: "local-swarm", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put local runtime placement: %v", err)
	}
	if workspaceEnabled {
		if _, err := topologyStore.PutWorkspaceBindingForAccount(principal.AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
			BindingID: "routed-binding", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
			SourceWorkspaceID: workspaceID, SourceWorkspaceGeneration: workspaceGeneration, SourceWorkspacePath: workspacePath, SourceWorkspaceName: "Routed Workspace",
			DestinationRuntimeSwarmID: "local-swarm", DestinationAuthorityHostSwarmID: "local-swarm", DestinationHostSwarmID: "local-swarm", DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost,
			DestinationWorkspacePath: workspacePath, PlacementGeneration: 1, BindingGeneration: 1, State: pebblestore.TopologyWorkspaceBindingStateBound,
			AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, MaterializationKind: pebblestore.TopologyWorkspaceBindingMaterializationSource,
			AttestedByHostSwarmID: "local-swarm", Writable: true,
		}); err != nil {
			t.Fatalf("put routed workspace binding: %v", err)
		}
	}
	server.SetTopologyService(topologyruntime.NewService(topologyStore, swarmStore))
	server.SetSwarmStore(swarmStore)
	if workspaceEnabled {
		managedPath := filepath.Join(t.TempDir(), "managed-worktree")
		server.SetWorktreeService(&routedWorktreeServiceStub{fakeWorktreeService: fakeWorktreeService{
			config:     worktreeruntime.Config{UseCurrentBranch: true, BranchName: "agent/<id>"},
			allocation: worktreeruntime.Allocation{WorkspacePath: managedPath, RepoRoot: workspacePath, BranchName: "agent/session", WorkspaceID: "session-worktree"},
		}})
	}
	return server, sessionService, principal
}
