package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
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
	"swarm/packages/swarmd/internal/uisettings"
	"swarm/packages/swarmd/internal/workspace"
)

type routedMediaTestRunner struct {
	*sessionRouterRecordingRunner
	declaration provideriface.MediaAdapterDeclaration
}

func (r *routedMediaTestRunner) MediaCapabilityDeclaration(context.Context) (provideriface.MediaAdapterDeclaration, error) {
	return r.declaration, nil
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
	action, err := favoriteService.Create(authorityContext, modelprofile.Input{Name: "Action", Provider: "openai", Model: "gpt-media", Thinking: "medium"})
	if err != nil {
		t.Fatalf("create Action favorite: %v", err)
	}
	swarmProfiles := modelprofile.NewSwarmService(pebblestore.NewSwarmModeSettingsStore(store), favoriteStore)
	if _, err := swarmProfiles.Put(authorityContext, modelprofile.SwarmSettingsInput{ActionFavoriteID: action.ProfileID}); err != nil {
		t.Fatalf("put Swarm settings: %v", err)
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
	if err := catalogStore.SetMeta(catalogMeta); err != nil {
		t.Fatalf("seed media catalog meta: %v", err)
	}
	modelService := modelruntime.NewService(pebblestore.NewModelStore(store), events, modelruntime.NewCatalogService(catalogStore))

	router := &sessionRouterRecordingRunner{id: "openai", response: provideriface.Response{Text: `{"title":"Inspect staged image","mode":"auto","worktree":false}`}}
	runner := &routedMediaTestRunner{sessionRouterRecordingRunner: router, declaration: provideriface.MediaAdapterDeclaration{
		AdapterID: provideriface.MediaAdapterIDOpenAIResponsesV1, ProviderID: "openai", ProviderSurface: provideriface.MediaProviderSurfaceOpenAIResponses,
		CredentialSurface: provideriface.MediaCredentialSurfaceOpenAIAPIKey, CredentialFingerprint: "credential-fingerprint",
		Inputs: []provideriface.MediaAdapterCapability{{Modality: "image", Semantics: pebblestore.ModelCatalogMediaSemanticsNative, MIMETypes: []string{"image/png"}, ContentTypes: []string{"input_image"}, MaxBytes: 1024, MaxCount: 2}},
	}}
	providers := registry.New()
	providers.RegisterRunner(runner)

	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	runService := runruntime.NewService(sessions, modelService, providers, tool.NewRuntime(1), nil, agentService, nil, events)
	server := NewServer(nil, agentService, modelService, runService, sessions, workspace.NewService(workspaceStore), nil, nil, providers, nil, nil, events, stream.NewHub(events))
	server.SetModelProfileService(favoriteService)
	server.SetSwarmProfileService(swarmProfiles)
	uiSettings := uisettings.NewService(pebblestore.NewUISettingsStore(store))
	if _, err := uiSettings.SetForAccount(principal.AccountScopeID, uisettings.UISettings{Agents: uisettings.AgentSettings{Router: uisettings.CompactAgentSettings{Provider: "openai", Model: "router-model", Thinking: "low"}}}); err != nil {
		t.Fatalf("configure Router: %v", err)
	}
	server.SetUISettingsService(uiSettings)

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

func (f *routedMediaTestFixture) postWithWorktreeIntent(t *testing.T, account, requestID, stagingID string, media map[string]string, managedWorktreeRequested bool) *httptest.ResponseRecorder {
	t.Helper()
	item := map[string]string{"staging_id": stagingID}
	for key, value := range media {
		item[key] = value
	}
	body, err := json.Marshal(map[string]any{"input": "inspect this image", "client_request_id": requestID, "managed_worktree_requested": managedWorktreeRequested, "media": []any{item}})
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

func (f *routedMediaTestFixture) assertNoRoutedSession(t *testing.T, requestID string) {
	t.Helper()
	sessionID := stableSessionsV3PrimarySessionID(f.principal, "routed:"+requestID)
	if _, ok, err := f.sessions.GetSession(sessionID); err != nil || ok {
		t.Fatalf("pre-mutation failure left session %q exists=%t err=%v", sessionID, ok, err)
	}
	assets := 0
	if err := f.store.IteratePrefix(pebblestore.SessionMediaAssetPrefix(f.principal.AccountScopeID, sessionID), 10, func(string, []byte) error { assets++; return nil }); err != nil || assets != 0 {
		t.Fatalf("pre-mutation failure left assets=%d err=%v", assets, err)
	}
	messages, err := f.sessions.ListSessionMessages(sessionID, 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("pre-mutation failure left messages=%d err=%v", len(messages), err)
	}
}

func TestRoutedSessionStagedMediaBindsDurablyAndReplayIsStable(t *testing.T) {
	fixture := newRoutedMediaTestFixture(t)
	staged := fixture.stage(t, fixture.principal.AccountScopeID, "success")
	response := fixture.post(t, fixture.principal.AccountScopeID, "media-success", staged.ID, map[string]string{"modality": "image", "file_type": "png"})
	if response.Code != http.StatusOK {
		t.Fatalf("routed media status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		SessionID    string                          `json:"session_id"`
		FirstMessage pebblestore.MessageSnapshot     `json:"first_message"`
		Projection   pebblestore.V3SessionProjection `json:"projection"`
		Replayed     bool                            `json:"replayed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode routed response: %v", err)
	}
	if body.Replayed || body.SessionID == "" || body.FirstMessage.SessionID != body.SessionID || body.Projection.SessionID != body.SessionID || len(body.FirstMessage.Media) != 1 {
		t.Fatalf("non-canonical routed response: %+v", body)
	}
	reference := body.FirstMessage.Media[0]
	bound, found, err := fixture.staging.Get(fixture.principal.AccountScopeID, staged.ID)
	if err != nil || !found || bound.State != pebblestore.MediaStagingStateBound || bound.BoundSessionID != body.SessionID || bound.AuthorityAssetID != reference.AssetID {
		t.Fatalf("staging bind found=%t err=%v record=%+v reference=%+v", found, err, bound, reference)
	}
	asset, payload, err := fixture.sessions.ReadSessionMediaAsset(fixture.principal.AccountScopeID, body.SessionID, reference.AssetID)
	if err != nil || !bytes.Equal(payload, mediaStagingAPIPNG) || asset.ReferenceCount != 1 || asset.ContractHash != reference.ContractHash || asset.ProviderID != "openai" || asset.Model != "gpt-media" {
		t.Fatalf("durable asset err=%v asset=%+v payload=%x", err, asset, payload)
	}
	messages, err := fixture.sessions.ListSessionMessages(body.SessionID, 0, 10)
	if err != nil || len(messages) != 1 || len(messages[0].Media) != 1 || messages[0].Media[0].AssetID != reference.AssetID {
		t.Fatalf("durable first message err=%v messages=%+v", err, messages)
	}

	replay := fixture.post(t, fixture.principal.AccountScopeID, "media-success", staged.ID, map[string]string{"modality": "image", "file_type": "png"})
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	messages, err = fixture.sessions.ListSessionMessages(body.SessionID, 0, 10)
	asset, _, assetErr := fixture.sessions.ReadSessionMediaAsset(fixture.principal.AccountScopeID, body.SessionID, reference.AssetID)
	assetCount := 0
	countErr := fixture.store.IteratePrefix(pebblestore.SessionMediaAssetPrefix(fixture.principal.AccountScopeID, body.SessionID), 10, func(string, []byte) error { assetCount++; return nil })
	if err != nil || assetErr != nil || countErr != nil || len(messages) != 1 || assetCount != 1 || asset.ReferenceCount != 1 || fixture.runner.createCalls != 1 {
		t.Fatalf("replay duplicated authority messages=%d assets=%d asset=%+v router_calls=%d errors=%v/%v/%v", len(messages), assetCount, asset, fixture.runner.createCalls, err, assetErr, countErr)
	}
}

func TestRoutedSessionStagedMediaFailuresArePreMutationAndSafelyCleaned(t *testing.T) {
	t.Run("router failure abandons staging", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		staged := fixture.stage(t, fixture.principal.AccountScopeID, "router-failure")
		fixture.runner.err = errors.New("router unavailable")
		response := fixture.post(t, fixture.principal.AccountScopeID, "router-failure", staged.ID, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertRoutedMediaStagingState(t, fixture, fixture.principal.AccountScopeID, staged.ID, pebblestore.MediaStagingStateDeleted)
		fixture.assertNoRoutedSession(t, "router-failure")
	})

	t.Run("workspace failure abandons staging", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		staged := fixture.stage(t, fixture.principal.AccountScopeID, "workspace-failure")
		fixture.server.workspace = nil
		response := fixture.post(t, fixture.principal.AccountScopeID, "workspace-failure", staged.ID, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertRoutedMediaStagingState(t, fixture, fixture.principal.AccountScopeID, staged.ID, pebblestore.MediaStagingStateDeleted)
		fixture.assertNoRoutedSession(t, "workspace-failure")
	})

	t.Run("mode failure abandons staging", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		staged := fixture.stage(t, fixture.principal.AccountScopeID, "mode-failure")
		fixture.runner.response.Text = `{"title":"Plan denied","mode":"plan","worktree":false}`
		response := fixture.post(t, fixture.principal.AccountScopeID, "mode-failure", staged.ID, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertRoutedMediaStagingState(t, fixture, fixture.principal.AccountScopeID, staged.ID, pebblestore.MediaStagingStateDeleted)
		fixture.assertNoRoutedSession(t, "mode-failure")
	})

	t.Run("capability failure abandons staging before materialization", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		staged := fixture.stage(t, fixture.principal.AccountScopeID, "capability-failure")
		response := fixture.post(t, fixture.principal.AccountScopeID, "capability-failure", staged.ID, map[string]string{"modality": "audio"})
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "not admitted") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertRoutedMediaStagingState(t, fixture, fixture.principal.AccountScopeID, staged.ID, pebblestore.MediaStagingStateDeleted)
		fixture.assertNoRoutedSession(t, "capability-failure")
	})

	t.Run("integrity failure abandons staging before materialization", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		staged := fixture.stage(t, fixture.principal.AccountScopeID, "integrity-failure")
		if err := fixture.store.PutBytes(pebblestore.KeyMediaStagingBlob(fixture.principal.AccountScopeID, staged.ID), []byte("corrupt")); err != nil {
			t.Fatalf("corrupt staging blob: %v", err)
		}
		response := fixture.post(t, fixture.principal.AccountScopeID, "integrity-failure", staged.ID, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertRoutedMediaStagingState(t, fixture, fixture.principal.AccountScopeID, staged.ID, pebblestore.MediaStagingStateDeleted)
		fixture.assertNoRoutedSession(t, "integrity-failure")
		if _, _, err := fixture.staging.Read(fixture.principal.AccountScopeID, staged.ID, time.Now().UnixMilli()); err == nil {
			t.Fatal("integrity failure left staged bytes consumable")
		}
	})
}

func TestRoutedSessionStagedMediaForeignExpiredAndBoundIDsFailClosed(t *testing.T) {
	t.Run("foreign", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		foreign := fixture.stage(t, "foreign-account", "foreign")
		response := fixture.post(t, fixture.principal.AccountScopeID, "foreign", foreign.ID, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertRoutedMediaStagingState(t, fixture, "foreign-account", foreign.ID, pebblestore.MediaStagingStateStaged)
		fixture.assertNoRoutedSession(t, "foreign")
	})

	t.Run("expired", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		expired := fixture.stage(t, fixture.principal.AccountScopeID, "expired", func(input *pebblestore.PutMediaStagingInput) { input.NowUnixMs = 1; input.TTL = time.Second })
		response := fixture.post(t, fixture.principal.AccountScopeID, "expired", expired.ID, nil)
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertRoutedMediaStagingState(t, fixture, fixture.principal.AccountScopeID, expired.ID, pebblestore.MediaStagingStateDeleted)
		fixture.assertNoRoutedSession(t, "expired")
	})

	t.Run("already bound", func(t *testing.T) {
		fixture := newRoutedMediaTestFixture(t)
		bound := fixture.stage(t, fixture.principal.AccountScopeID, "bound")
		if _, _, err := fixture.staging.Bind(pebblestore.BindMediaStagingInput{AccountScopeID: fixture.principal.AccountScopeID, SessionID: "other-session", Bindings: []pebblestore.MediaStagingBinding{{StagingID: bound.ID, AuthorityAssetID: "other-asset", DigestSHA256: bound.DigestSHA256}}}); err != nil {
			t.Fatalf("pre-bind staging: %v", err)
		}
		response := fixture.post(t, fixture.principal.AccountScopeID, "bound", bound.ID, nil)
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		stored, found, err := fixture.staging.Get(fixture.principal.AccountScopeID, bound.ID)
		if err != nil || !found || stored.State != pebblestore.MediaStagingStateBound || stored.BoundSessionID != "other-session" || stored.AuthorityAssetID != "other-asset" {
			t.Fatalf("bound record changed found=%t err=%v record=%+v", found, err, stored)
		}
		fixture.assertNoRoutedSession(t, "bound")
	})
}

func assertRoutedMediaStagingState(t *testing.T, fixture *routedMediaTestFixture, account, stagingID string, want pebblestore.MediaStagingState) {
	t.Helper()
	record, found, err := fixture.staging.Get(account, stagingID)
	if err != nil || !found || record.State != want {
		t.Fatalf("staging state found=%t err=%v got=%q want=%q record=%+v", found, err, record.State, want, record)
	}
}
