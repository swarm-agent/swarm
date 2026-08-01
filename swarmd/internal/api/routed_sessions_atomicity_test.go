package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/modelprofile"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/tool"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/workspace"
)

func TestRoutedSessionStartFailuresLeaveNoDurableAuthority(t *testing.T) {
	tests := []struct {
		name              string
		planEnabled       bool
		workspaceEnabled  bool
		routerResponse    string
		routerErr         error
		breakCapabilities bool
		wantRouterCalls   int
	}{
		{name: "workspace failure", workspaceEnabled: false, routerResponse: `{"title":"unused","mode":"auto","worktree":false}`, wantRouterCalls: 0},
		{name: "Router failure", workspaceEnabled: true, routerErr: errors.New("Router unavailable"), wantRouterCalls: 1},
		{name: "mode failure", workspaceEnabled: true, routerResponse: `{"title":"Plan is disabled","mode":"plan","worktree":false}`, wantRouterCalls: 1},
		{name: "capability failure", workspaceEnabled: true, routerResponse: `{"title":"Capability check","mode":"auto","worktree":false}`, breakCapabilities: true, wantRouterCalls: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: test.routerResponse}, err: test.routerErr}
			server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, test.planEnabled, test.workspaceEnabled)
			if test.breakCapabilities {
				// Agent resolution must compile the stored V3 tool contract before any
				// session authority is committed.
				server.runner = nil
			}

			const requestID = "failed-routed-start"
			recorder := postRoutedSessionAtomicityRequest(t, server, principal, map[string]any{
				"input": "route this request", "client_request_id": requestID,
			})
			if recorder.Code == http.StatusOK {
				t.Fatalf("failure returned success: %s", recorder.Body.String())
			}
			if runner.createCalls != test.wantRouterCalls || runner.streamingCalls != 0 {
				t.Fatalf("Router calls create=%d streaming=%d, want %d/0", runner.createCalls, runner.streamingCalls, test.wantRouterCalls)
			}

			sessionID := stableSessionsV3PrimarySessionID(principal, "routed:"+requestID)
			assertNoRoutedSessionDurableAuthority(t, sessions, principal, sessionID, requestID)
		})
	}
}

func TestRoutedSessionStartCommitsAndReplaysOneAtomicMutation(t *testing.T) {
	runner := &sessionRouterRecordingRunner{id: "recording", response: provideriface.Response{Text: `{"title":"Router Owned Title","mode":"auto","worktree":false}`}}
	server, sessions, principal := newRoutedSessionAtomicityServer(t, runner, false, true)
	const requestID = "atomic-routed-start"
	requestBody := map[string]any{
		"input": "create the routed session", "client_request_id": requestID,
		"metadata": map[string]any{"source": "desktop"},
	}

	first := postRoutedSessionAtomicityRequest(t, server, principal, requestBody)
	if first.Code != http.StatusOK {
		t.Fatalf("first routed start status=%d body=%s", first.Code, first.Body.String())
	}
	firstResponse := decodeRoutedSessionAtomicityResponse(t, first)
	if !firstResponse.OK || firstResponse.Replayed || firstResponse.SessionID == "" {
		t.Fatalf("first routed response = %+v", firstResponse)
	}
	if runner.createCalls != 1 || runner.streamingCalls != 0 {
		t.Fatalf("first Router calls create=%d streaming=%d", runner.createCalls, runner.streamingCalls)
	}

	stored, ok, err := sessions.GetSession(firstResponse.SessionID)
	if err != nil || !ok {
		t.Fatalf("durable session exists=%t err=%v", ok, err)
	}
	if stored.Title != "Router Owned Title" || stored.Mode != sessionruntime.ModeAuto || stored.WorkspacePath == "" {
		t.Fatalf("durable routed session = %+v", stored)
	}
	if stored.Metadata["title_source"] != routedSessionTitleSourceRouter || stored.Metadata["title_locked"] != true || stored.Metadata["title_pending"] != false {
		t.Fatalf("Router title ownership metadata = %+v", stored.Metadata)
	}
	if firstResponse.Session.ID != stored.ID || firstResponse.Session.Title != stored.Title || firstResponse.Session.Metadata["title_source"] != routedSessionTitleSourceRouter {
		t.Fatalf("response session is not canonical: response=%+v durable=%+v", firstResponse.Session, stored)
	}

	messages, err := sessions.ListSessionMessages(stored.ID, 0, 10)
	if err != nil {
		t.Fatalf("list first messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "create the routed session" {
		t.Fatalf("durable first messages = %+v", messages)
	}
	if firstResponse.Message.ID != messages[0].ID || firstResponse.Message.SessionID != stored.ID {
		t.Fatalf("response first message = %+v, durable=%+v", firstResponse.Message, messages[0])
	}
	projection, projectionOK, err := sessions.GetSessionProjection(stored.ID)
	if err != nil || !projectionOK || projection.LastEventSeq != 1 || projection.ProjectionHighWatermarkSeq != 1 {
		t.Fatalf("projection exists=%t projection=%+v err=%v", projectionOK, projection, err)
	}
	if firstResponse.Projection.SessionID != stored.ID || firstResponse.Projection.LastEventSeq != projection.LastEventSeq {
		t.Fatalf("response projection=%+v durable=%+v", firstResponse.Projection, projection)
	}

	runID := stableSessionsV3PrimaryRunID(stored.ID, "routed-message:"+requestID)
	intent, intentOK, err := sessions.Store().GetV3SessionRunIntent(stored.ID, runID)
	if err != nil || !intentOK || intent.RunID != runID {
		t.Fatalf("run intent exists=%t intent=%+v err=%v", intentOK, intent, err)
	}
	idempotency, idempotencyOK, err := sessions.Store().GetV3SessionOperationIdempotencyRecord(principal.AccountScopeID, stored.ID, sessionruntime.SessionMutationCreateSession, requestID)
	if err != nil || !idempotencyOK || idempotency.Result.MessageID != messages[0].ID || idempotency.Result.RunID != runID {
		t.Fatalf("idempotency exists=%t record=%+v err=%v", idempotencyOK, idempotency, err)
	}
	events, err := sessions.ListSessionEvents(stored.ID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("atomic create events=%+v err=%v", events, err)
	}
	outbox, err := sessions.Store().ListV3RealtimeOutboxForSessionAfterEndpoint(stored.ID, 0, 10)
	if err != nil || len(outbox) != 1 || outbox[0].Projection.LastEventSeq != 1 {
		t.Fatalf("atomic create outbox=%+v err=%v", outbox, err)
	}

	replay := postRoutedSessionAtomicityRequest(t, server, principal, requestBody)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	replayResponse := decodeRoutedSessionAtomicityResponse(t, replay)
	if !replayResponse.Replayed || replayResponse.SessionID != stored.ID || replayResponse.Message.ID != messages[0].ID {
		t.Fatalf("replay response = %+v", replayResponse)
	}
	if runner.createCalls != 1 {
		t.Fatalf("exact replay called Router %d times, want once", runner.createCalls)
	}
	assertRoutedSessionAtomicityCardinality(t, sessions, stored.ID, 1, 1, 1)

	conflictBody := map[string]any{
		"input": "different payload", "client_request_id": requestID,
		"metadata": map[string]any{"source": "desktop"},
	}
	conflict := postRoutedSessionAtomicityRequest(t, server, principal, conflictBody)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("payload conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if runner.createCalls != 1 {
		t.Fatalf("payload conflict called Router %d times, want once", runner.createCalls)
	}
	assertRoutedSessionAtomicityCardinality(t, sessions, stored.ID, 1, 1, 1)
}

type routedSessionAtomicityResponse struct {
	OK         bool                             `json:"ok"`
	SessionID  string                           `json:"session_id"`
	Session    pebblestore.SessionSnapshot      `json:"session"`
	Projection sessionruntime.SessionProjection `json:"projection"`
	Message    pebblestore.MessageSnapshot      `json:"message"`
	Replayed   bool                             `json:"replayed"`
}

func decodeRoutedSessionAtomicityResponse(t *testing.T, recorder *httptest.ResponseRecorder) routedSessionAtomicityResponse {
	t.Helper()
	var response routedSessionAtomicityResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode routed response: %v body=%s", err, recorder.Body.String())
	}
	return response
}

func postRoutedSessionAtomicityRequest(t *testing.T, server *Server, principal identity.Principal, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode routed request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, RoutedSessionsPath, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(identity.ContextWithPrincipal(request.Context(), principal))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
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
	if outbox, err := sessions.Store().ListV3RealtimeOutboxAfter(0, 10); err != nil || len(outbox) != 0 {
		t.Fatalf("failed routed start outbox=%+v err=%v", outbox, err)
	}
}

func assertRoutedSessionAtomicityCardinality(t *testing.T, sessions *sessionruntime.Service, sessionID string, wantMessages, wantEvents, wantOutbox int) {
	t.Helper()
	messages, messageErr := sessions.ListSessionMessages(sessionID, 0, 10)
	events, eventErr := sessions.ListSessionEvents(sessionID, 0, 10)
	outbox, outboxErr := sessions.Store().ListV3RealtimeOutboxForSessionAfterEndpoint(sessionID, 0, 10)
	if messageErr != nil || eventErr != nil || outboxErr != nil || len(messages) != wantMessages || len(events) != wantEvents || len(outbox) != wantOutbox {
		t.Fatalf("durable cardinality messages=%d/%d events=%d/%d outbox=%d/%d errors=%v/%v/%v", len(messages), wantMessages, len(events), wantEvents, len(outbox), wantOutbox, messageErr, eventErr, outboxErr)
	}
}

func newRoutedSessionAtomicityServer(t *testing.T, routerRunner *sessionRouterRecordingRunner, planEnabled, workspaceEnabled bool) (*Server, *sessionruntime.Service, identity.Principal) {
	t.Helper()
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

	sessionService := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	modelService := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	permissionService := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissionService.SetSessionResolver(sessionService)
	agentService := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentService.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agent defaults: %v", err)
	}
	if _, _, _, err := agentService.UpsertForAccount(principal.AccountScopeID, agentruntime.UpsertInput{
		Name: "swarm", Mode: agentruntime.ModePrimary, Enabled: pebblestore.BoolPtr(true), Prompt: "Routed atomicity test agent",
		RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ToolContract: &pebblestore.AgentToolContract{Preset: "custom", Tools: map[string]pebblestore.AgentToolConfig{"read": {Enabled: pebblestore.BoolPtr(true)}}},
	}); err != nil {
		t.Fatalf("configure routed agent: %v", err)
	}

	providers := registry.New()
	providers.RegisterRunner(routerRunner)
	runService := runruntime.NewService(sessionService, modelService, providers, tool.NewRuntime(1), permissionService, agentService, nil, nil)
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceService := workspace.NewService(workspaceStore)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if workspaceEnabled {
		entry, err := workspaceStore.AddForAccount(principal.AccountScopeID, workspacePath, "Routed Workspace")
		if err != nil {
			t.Fatalf("add routed workspace: %v", err)
		}
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
	planFavoriteID := ""
	if planEnabled {
		planFavorite, err := favoriteService.Create(authorityContext, modelprofile.Input{Name: "Plan", Provider: "recording", Model: "plan-model", Thinking: "high"})
		if err != nil {
			t.Fatalf("create Plan favorite: %v", err)
		}
		planFavoriteID = planFavorite.ProfileID
	}
	swarmProfiles := modelprofile.NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store), favoriteStore)
	if _, err := swarmProfiles.Put(authorityContext, modelprofile.SwarmSettingsInput{ActionFavoriteID: actionFavorite.ProfileID, PlanEnabled: planEnabled, PlanFavoriteID: planFavoriteID}); err != nil {
		t.Fatalf("configure Swarm model settings: %v", err)
	}
	server.SetModelProfileService(favoriteService)
	server.SetSwarmProfileService(swarmProfiles)
	uiSettings := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	if _, err := uiSettings.SetForAccount(principal.AccountScopeID, uisettings.UISettings{Agents: uisettings.AgentSettings{Router: uisettings.CompactAgentSettings{Provider: "recording", Model: "router-model", Thinking: "medium"}}}); err != nil {
		t.Fatalf("configure Router model: %v", err)
	}
	server.SetUISettingsService(uiSettings)

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
		t.Fatalf("put local swarm node: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "local-swarm", AccountScopeID: principal.AccountScopeID, AuthorityHostSwarmID: "local-swarm", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put local runtime placement: %v", err)
	}
	if workspaceEnabled {
		if _, err := topologyStore.PutWorkspaceBindingForAccount(principal.AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
			BindingID: "routed-binding", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
			SourceWorkspaceID: "routed-workspace", SourceWorkspaceGeneration: 1, SourceWorkspacePath: workspacePath, SourceWorkspaceName: "Routed Workspace",
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
	return server, sessionService, principal
}

