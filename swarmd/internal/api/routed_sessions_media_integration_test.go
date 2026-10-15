package api

import (
	"bytes"
	"context"
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"path/filepath"

	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/mediastaging"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/modelprofile"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
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

type routedMediaTestRunner struct {
	*sessionRouterRecordingRunner
	declaration provideriface.MediaAdapterDeclaration
}

type routedMediaTestFixture struct {
	server    *Server
	sessions  *sessionruntime.Service
	staging   *mediastaging.Service
	store     *pebblestore.Store
	runner    *routedMediaTestRunner
	principal identity.Principal
}

func newRoutedMediaTestFixture(t *testing.T) *routedMediaTestFixture {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "routed-media.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "routed-user", AccountScopeID: "routed-account", AccountScopeSource: identity.AccountScopeSourceServerState}
	workspacePath := t.TempDir()
	// The deployment bridge verifies source Git identity before retaining media.
	// Use a real committed source, registered below for this account, so tests
	// reach the durable media boundary without bypassing repository authority.
	runGitCommitTestCommand(t, workspacePath, "init", "-b", "dev")
	runGitCommitTestCommand(t, workspacePath, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@example.invalid", "commit", "--allow-empty", "-m", "fixture")

	workspaceStore := pebblestore.NewWorkspaceStore(store)
	entry, err := workspaceStore.AddForAccount(principal.AccountScopeID, workspacePath, "routed-workspace")
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	pending, err := workspaceStore.MarkDefinitionPendingForAccount(principal.AccountScopeID, entry.Path)
	if err != nil {
		t.Fatalf("mark workspace definition pending: %v", err)
	}
	entry, current, err := workspaceStore.CompleteDefinitionForAccount(principal.AccountScopeID, entry.Path, pending.DefinitionGeneration, "Routed media integration workspace", 1)
	if err != nil || !current {
		t.Fatalf("complete workspace definition current=%t err=%v", current, err)
	}

	favoriteStore := pebblestore.NewModelProfileStore(store)
	favoriteService := modelprofile.NewService(favoriteStore)
	authorityContext := identity.ContextWithPrincipal(context.Background(), principal)
	_, err = favoriteService.Create(authorityContext, modelprofile.Input{Name: "Action", Provider: "openai", Model: "gpt-media", Thinking: "medium"})
	if err != nil {
		t.Fatalf("create Action favorite: %v", err)
	}
	agentService := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agentService.EnsureDefaultsForAccount(principal.AccountScopeID); err != nil {
		t.Fatalf("ensure account agent defaults: %v", err)
	}

	catalogStore := pebblestore.NewModelCatalogStore(store)
	catalogMeta := pebblestore.ModelCatalogMeta{Source: "test", SnapshotID: "media-snapshot", SnapshotVersion: "v1", RecordCount: 1, ModelCount: 1, ProviderCount: 1}
	catalogRecord := pebblestore.ModelCatalogRecord{
		Provider: "openai", Model: "gpt-media", Source: "test", SourceSnapshotID: catalogMeta.SnapshotID, SourceSnapshotVersion: catalogMeta.SnapshotVersion,
		Media: &pebblestore.ModelCatalogMediaCapabilities{State: pebblestore.ModelCatalogMediaStateSupported, ProviderSurface: provideriface.MediaProviderSurfaceOpenAIResponses, CredentialSurface: provideriface.MediaCredentialSurfaceOpenAIAPIKey,
			Inputs: []pebblestore.ModelCatalogMediaDirection{{Modality: "image", State: pebblestore.ModelCatalogMediaStateSupported, Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}}}},
	}
	if err := catalogStore.SetRecord(catalogRecord); err != nil {
		t.Fatalf("seed media catalog record: %v", err)
	}
	if err := catalogStore.SetRecord(pebblestore.ModelCatalogRecord{Provider: "openai", Model: "router-model", ThinkingOptions: []string{"low"}}); err != nil {
		t.Fatalf("seed Router model catalog record: %v", err)
	}
	if err := catalogStore.SetMeta(catalogMeta); err != nil {
		t.Fatalf("seed media catalog meta: %v", err)
	}
	modelService := modelruntime.NewService(pebblestore.NewModelStore(store), events, modelruntime.NewCatalogService(catalogStore))

	router := &sessionRouterRecordingRunner{id: "openai", response: provideriface.Response{Text: `{"title":"Inspect staged image","worktree_name":"inspect-staged-image"}`}}
	runner := &routedMediaTestRunner{sessionRouterRecordingRunner: router, declaration: provideriface.MediaAdapterDeclaration{
		AdapterID: provideriface.MediaAdapterIDOpenAIResponsesV1, ProviderID: "openai", ProviderSurface: provideriface.MediaProviderSurfaceOpenAIResponses,
		CredentialSurface: provideriface.MediaCredentialSurfaceOpenAIAPIKey, CredentialFingerprint: "credential-fingerprint",
		Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 2}},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)

	sessionStore := pebblestore.NewSessionStore(store)
	// Requirement: allocated-lane admission must reject unknown historical
	// ownership. Complete the canonical bounded backfill before POST;
	// never seed a ready flag or skip history validation.
	// This fixture setup is the narrowest layer that enables the existing
	// routed media ownership, replay, and atomicity assertions to reach admission.
	requireMatrixRepositoryHistoryReady(t, sessionStore)
	sessions := sessionruntime.NewService(sessionStore, events)
	runService := runruntime.NewService(sessions, modelService, providers, tool.NewRuntime(1), nil, agentService, nil, events)
	server := NewServer(nil, agentService, modelService, runService, sessions, workspace.NewService(workspaceStore), nil, nil, providers, nil, nil, events, stream.NewHub(events))
	server.SetModelProfileService(favoriteService)
	agentSettingsStore := pebblestore.NewAgentModelSettingsStore(store)
	agentSettings := testAgentModelSettingsRecord(principal.AccountScopeID)
	agentSettings.Swarm.Action = pebblestore.AgentModelAssignment{Provider: "openai", Model: "gpt-media", Thinking: "medium"}
	agentSettings.Swarm.Plan = agentSettings.Swarm.Action
	agentSettings.SystemAgents.Router = pebblestore.AgentModelAssignment{Provider: "openai", Model: "router-model", Thinking: "low"}
	if _, err := agentSettingsStore.PutForAccount(agentSettings); err != nil {
		t.Fatalf("configure canonical agent model settings: %v", err)
	}
	server.SetAgentModelSettingsService(agentmodelsettings.NewService(agentSettingsStore))

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := topologyStore.PutRuntimeForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "local-swarm", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Name: "Local"}); err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "local-swarm", AccountScopeID: principal.AccountScopeID, AuthorityHostSwarmID: "local-swarm", RuntimeKind: pebblestore.TopologyRuntimeKindHost, State: pebblestore.TopologyRuntimePlacementStateActive, PlacementGeneration: 1}); err != nil {
		t.Fatalf("put placement: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount(principal.AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID: "binding-routed", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		SourceWorkspaceID: entry.WorkspaceID, SourceWorkspaceGeneration: entry.WorkspaceGeneration, SourceWorkspacePath: workspacePath, SourceWorkspaceName: "routed-workspace",
		DestinationRuntimeSwarmID: "local-swarm", DestinationAuthorityHostSwarmID: "local-swarm", DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost,
		DestinationWorkspacePath: workspacePath, PlacementGeneration: 1, BindingGeneration: 1, State: pebblestore.TopologyWorkspaceBindingStateBound,
		AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite, MaterializationKind: pebblestore.TopologyWorkspaceBindingMaterializationSource,
		AttestedByHostSwarmID: "local-swarm", Writable: true,
	}); err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}
	server.SetTopologyService(topologyruntime.NewService(topologyStore, swarmStore))
	server.SetSwarmStore(swarmStore)
	managedWorktreePath := filepath.Join(t.TempDir(), "managed-worktree")
	// Let the existing provisioner create a source-linked Git worktree on the
	// requested branch and capture HEAD as BaseCommit. A plain directory (or
	// independent repository) cannot satisfy ValidateOwnedIdentity admission.
	server.SetWorktreeService(&routedWorktreeServiceStub{repositoryFixture: t, fakeWorktreeService: fakeWorktreeService{
		config:     worktreeruntime.Config{UseCurrentBranch: true, BranchName: "agent/<id>"},
		allocation: worktreeruntime.Allocation{WorkspacePath: managedWorktreePath, RepoRoot: workspacePath, BranchName: "agent/session", WorkspaceID: "session-worktree"},
	}})

	staging := mediastaging.NewService(pebblestore.NewMediaStagingStore(store))
	server.SetMediaStagingService(staging)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	server.v3SessionExecutor = &sessionV3Executor{server: server, ctx: canceled}
	return &routedMediaTestFixture{server: server, sessions: sessions, staging: staging, store: store, runner: runner, principal: principal}
}

func (f *routedMediaTestFixture) stage(t *testing.T, account, key string, options ...func(*pebblestore.PutMediaStagingInput)) pebblestore.MediaStagingRecord {
	t.Helper()
	input := pebblestore.PutMediaStagingInput{AccountScopeID: account, IdempotencyKey: key, DeclaredMIMEType: "image/png", Reader: bytes.NewReader(mediaStagingAPIPNG)}
	for _, option := range options {
		option(&input)
	}
	record, _, err := f.staging.Put(input)
	if err != nil {
		t.Fatalf("stage media: %v", err)
	}
	return record
}

func (f *routedMediaTestFixture) post(t *testing.T, account, requestID, stagingID string, media map[string]string) *httptest.ResponseRecorder {
	return f.postWithWorktreeIntent(t, account, requestID, stagingID, media, false)
}

func (f *routedMediaTestFixture) postWithWorktreeIntent(t *testing.T, account, requestID, stagingID string, media map[string]string, _ bool) *httptest.ResponseRecorder {
	t.Helper()
	item := map[string]string{"staging_id": stagingID}
	for key, value := range media {
		item[key] = value
	}
	payload := map[string]any{"input": "inspect this image", "client_request_id": requestID, "plan_mode_requested": false, "media": []any{item}, "workspace_binding_id": "binding-routed", "swarm_id": "local-swarm", "target_kind": "host", "target_relationship": "self"}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal routed request: %v", err)
	}
	userID := "user-" + account
	if account == f.principal.AccountScopeID {
		userID = f.principal.UserID
	}
	request := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, RoutedSessionsPath, bytes.NewReader(body)), userID, account)
	response := httptest.NewRecorder()
	f.server.handleRoutedSessionStart(response, request)
	return response
}
