package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gorillaws "github.com/gorilla/websocket"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func TestManagedHostSessionMessageUsesNewPeerAPIWithAuthAndMirrors(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	var oldPeerHits atomic.Int32
	var messagePeerHits atomic.Int32
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/swarm/peer/sessions/") {
			oldPeerHits.Add(1)
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != peerManagedHostSessionMessagePath {
			http.NotFound(w, r)
			return
		}
		messagePeerHits.Add(1)
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		var req managedHostSessionMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.SessionID != "managed-session" || req.Content != "hello managed" {
			t.Fatalf("request = %+v", req)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": pebblestore.MessageSnapshot{ID: "msg_00000000000000000011", SessionID: "managed-session", GlobalSeq: 11, Role: "user", Content: "hello managed", CreatedAt: 123},
			"session": pebblestore.SessionSnapshot{ID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: map[string]any{"swarm_managed_host_session": true}, CreatedAt: 1, UpdatedAt: 123, MessageCount: 1, LastMessageAt: 123},
		})
	}))
	defer managed.Close()

	seedManagedHostTarget(t, server, managed.URL)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:             "managed-session",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/managed/workspace",
		WorkspaceName:  "workspace",
		Title:          "Managed",
		Mode:           "auto",
		Metadata: map[string]any{
			"swarm_managed_host_session":  true,
			"swarm_managed_host_swarm_id": "managed-swarm",
		},
		CreatedAt: 1,
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "managed-swarm", ChildBackendURL: managed.URL, HostSwarmID: "managed-swarm", HostWorkspacePath: "/host/workspace", RuntimeWorkspacePath: "/managed/workspace"}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, managedHostSessionMessagePath, bytes.NewBufferString(`{"session_id":"managed-session","role":"user","content":"hello managed"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if messagePeerHits.Load() != 1 {
		t.Fatalf("new peer message hits = %d, want 1", messagePeerHits.Load())
	}
	if oldPeerHits.Load() != 0 {
		t.Fatalf("old peer sessions API was used %d times", oldPeerHits.Load())
	}
	messages, err := sessionSvc.ListMessages("managed-session", 0, 20)
	if err != nil {
		t.Fatalf("list mirrored messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "hello managed" || messages[0].GlobalSeq != 11 {
		t.Fatalf("mirrored messages = %+v", messages)
	}
}

func TestManagedHostSessionOpenSendsRuntimeWorkspacePathToPeer(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	var openedPrimaryBackendURL atomic.Value
	var openedWorkspacePath atomic.Value
	var openedHostWorkspacePath atomic.Value
	var openedRuntimeWorkspacePath atomic.Value
	var openedMetadata atomic.Value
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "managed-binding",
		UserID:                    testPrincipal().UserID,
		AccountScopeID:            testPrincipal().AccountScopeID,
		SourceWorkspacePath:       "/host/workspace",
		SourceWorkspaceName:       "workspace",
		DestinationRuntimeSwarmID: "managed-swarm",
		DestinationHostSwarmID:    "managed-swarm",
		DestinationWorkspacePath:  "/managed/workspace",
		LegacyTargetKind:          "managed_host",
	}); err != nil {
		t.Fatalf("put managed workspace binding: %v", err)
	}
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != peerManagedHostSessionOpenPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		var req peerManagedHostSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		openedPrimaryBackendURL.Store(req.Route.PrimaryBackendURL)
		openedWorkspacePath.Store(req.Request.WorkspacePath)
		openedHostWorkspacePath.Store(req.Request.HostWorkspacePath)
		openedRuntimeWorkspacePath.Store(req.Request.RuntimeWorkspacePath)
		openedMetadata.Store(req.Request.Metadata)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"session": pebblestore.SessionSnapshot{ID: req.SessionID, UserID: req.Route.UserID, AccountScopeID: req.Route.AccountScopeID, WorkspacePath: req.Request.WorkspacePath, WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: req.Request.Metadata, CreatedAt: 1, UpdatedAt: 2},
		})
	}))
	defer managed.Close()

	startupPath := t.TempDir() + "/swarm.conf"
	cfg := startupconfig.Default(startupPath)
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	cfg.SwarmName = "host-swarm"
	cfg.TailscaleURL = "https://primary.tailnet.test"
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	seedManagedHostTarget(t, server, managed.URL)
	req := httptest.NewRequest(http.MethodPost, managedHostSessionOpenPath, bytes.NewBufferString(`{"title":"managed","workspace_path":"/host/workspace","host_workspace_path":"/host/workspace","runtime_workspace_path":"/managed/workspace","workspace_name":"workspace","mode":"auto","agent_name":"swarm","preference":{"provider":"codex","model":"gpt-5.5","thinking":"high"},"target_swarm_id":"managed-swarm"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, _ := openedPrimaryBackendURL.Load().(string); got != "https://primary.tailnet.test" {
		t.Fatalf("primary backend url = %q, want tailscale endpoint", got)
	}
	if got, _ := openedWorkspacePath.Load().(string); got != "/managed/workspace" {
		t.Fatalf("peer workspace path = %q, want /managed/workspace", got)
	}
	if got, _ := openedHostWorkspacePath.Load().(string); got != "/managed/workspace" {
		t.Fatalf("peer host workspace path = %q, want /managed/workspace", got)
	}
	if got, _ := openedRuntimeWorkspacePath.Load().(string); got != "/managed/workspace" {
		t.Fatalf("peer runtime workspace path = %q, want /managed/workspace", got)
	}
	metadata, _ := openedMetadata.Load().(map[string]any)
	if metadata["swarm_route_id"] != "swarm:managed-swarm:/managed/workspace" || metadata["swarm_route_label"] != "Managed Host" || metadata["swarm_route_target_kind"] != "host" || metadata["swarm_route_target_relationship"] != swarmruntime.RelationshipManaged || metadata["owner_transport"] != "managed_host_peer" {
		t.Fatalf("route metadata = %+v", metadata)
	}
	if metadata[sessionruntime.HostedSessionMetadataEnabled] != true || metadata[sessionruntime.HostedSessionMetadataHostSwarmID] != "host-swarm-id" || metadata[sessionruntime.HostedSessionMetadataHostBackendURL] != "https://primary.tailnet.test" || metadata[sessionruntime.HostedSessionMetadataHostWorkspacePath] != "/host/workspace" || metadata[sessionruntime.HostedSessionMetadataRuntimeWorkspacePath] != "/managed/workspace" || metadata[sessionruntime.HostedSessionMetadataChildSwarmID] != "managed-swarm" {
		t.Fatalf("hosted metadata = %+v", metadata)
	}
	var payload struct {
		Session struct {
			ID            string `json:"id"`
			WorkspacePath string `json:"workspace_path"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(payload.Session.ID) == "" || payload.Session.WorkspacePath != "/managed/workspace" {
		t.Fatalf("mirrored session payload = %+v", payload.Session)
	}
	mirrored, ok, err := sessionSvc.GetSession(payload.Session.ID)
	if err != nil {
		t.Fatalf("get mirrored session: %v", err)
	}
	if !ok || mirrored.WorkspacePath != "/managed/workspace" {
		t.Fatalf("mirrored session = %+v ok=%v", mirrored, ok)
	}
	if mirrored.Metadata["swarm_route_id"] != "swarm:managed-swarm:/managed/workspace" || mirrored.Metadata[sessionruntime.HostedSessionMetadataHostWorkspacePath] != "/host/workspace" || mirrored.Metadata[sessionruntime.HostedSessionMetadataRuntimeWorkspacePath] != "/managed/workspace" {
		t.Fatalf("mirrored metadata = %+v", mirrored.Metadata)
	}
	routeRecord, ok, err := server.topology.GetSessionRouteForAccount(testPrincipal().AccountScopeID, payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("get topology session route ok=%t err=%v", ok, err)
	}
	if routeRecord.RuntimeSwarmID != "managed-swarm" || routeRecord.BackendURL != managed.URL || routeRecord.WorkspaceBindingID != "managed-binding" || routeRecord.UserID != testPrincipal().UserID || routeRecord.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("topology session route = %+v", routeRecord)
	}
	if routeRecord.HostSwarmID != "managed-swarm" || routeRecord.HostWorkspacePath != "/host/workspace" || routeRecord.RuntimeWorkspacePath != "/managed/workspace" {
		t.Fatalf("topology session route enrichment = %+v", routeRecord)
	}
	baseRoute, ok, err := routeStore.Get(payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("get base session route ok=%t err=%v", ok, err)
	}
	if baseRoute.ChildSwarmID != "managed-swarm" || baseRoute.ChildBackendURL != managed.URL || baseRoute.UserID != testPrincipal().UserID || baseRoute.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("base session route = %+v", baseRoute)
	}
}

func TestManagedHostMirroredRunStreamControlUsesPeerStreamAPIWithSessionID(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	server.SetSessionRouteStore(nil)
	var streamPeerHits atomic.Int32
	var received runStreamInboundMessage
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != peerManagedHostSessionRunStreamPath {
			http.NotFound(w, r)
			return
		}
		streamPeerHits.Add(1)
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeJSON(w, http.StatusAccepted, managedHostSessionRunAccepted{OK: true, SessionID: "managed-session", RunID: "managed-run", Status: "accepted", Background: true, OwnerTransport: "managed_host_peer"})
	}))
	defer managed.Close()

	if _, err := server.swarmNodes.Put(pebblestore.SwarmNodeRecord{SwarmID: "managed-swarm", Name: "Managed Host", Role: swarmruntime.RelationshipManaged, Kind: "manual", Transport: startupconfig.NetworkModeTailscale, BackendURL: managed.URL, Status: "online"}); err != nil {
		t.Fatalf("put managed node: %v", err)
	}
	server.SetSwarmNodeStore(server.swarmNodes)
	server.swarmTargetHealth.entries = map[string]swarmTargetHealthEntry{
		"manual|managed-swarm|" + managed.URL: {online: true, checkedAt: time.Now()},
	}
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: map[string]any{"swarm_managed_host_session": true, "swarm_managed_host_swarm_id": "managed-swarm"}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}

	req := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/sessions/managed-session/run/stream?swarm_id=managed-swarm", bytes.NewBufferString(`{"type":"run.start","prompt":"hello managed","background":true}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if streamPeerHits.Load() != 1 {
		t.Fatalf("peer stream hits = %d, want 1", streamPeerHits.Load())
	}
	if received.SessionID != "managed-session" || received.Type != "run.start" || received.Prompt != "hello managed" || !received.Background {
		t.Fatalf("peer stream request = %+v", received)
	}
}

func TestManagedHostRunStreamStartPayloadWithSession(t *testing.T) {
	raw, err := managedHostRunStreamStartPayloadWithSession([]byte(`{"type":"run.start","prompt":"hello"}`), "managed-session")
	if err != nil {
		t.Fatalf("patch start payload: %v", err)
	}
	var payload runStreamInboundMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode patched payload: %v", err)
	}
	if payload.SessionID != "managed-session" || payload.Type != "run.start" || payload.Prompt != "hello" {
		t.Fatalf("patched payload = %+v", payload)
	}
}

func TestManagedHostRunStreamWebsocketProxyMirrorsUpstreamFrames(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != peerManagedHostSessionRunStreamPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		conn, err := (&gorillaws.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade managed websocket: %v", err)
		}
		defer conn.Close()
		_, rawStart, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read start frame: %v", err)
		}
		var start runStreamInboundMessage
		if err := json.Unmarshal(rawStart, &start); err != nil {
			t.Fatalf("decode start frame: %v", err)
		}
		if start.SessionID != "managed-session" || start.Type != "run.start" || start.Prompt != "hello managed" {
			t.Fatalf("start frame = %+v", start)
		}
		frames := []runStreamWireEvent{
			{Type: runruntime.StreamEventSessionLifecycle, SessionID: "managed-session", RunID: "managed-run", Lifecycle: &pebblestore.SessionLifecycleSnapshot{SessionID: "managed-session", RunID: "managed-run", Phase: "running", Active: true, OwnerTransport: "managed_host_peer", StartedAt: 123, UpdatedAt: 124}},
			{Type: "assistant.message", SessionID: "managed-session", RunID: "managed-run", Message: &pebblestore.MessageSnapshot{ID: "msg_00000000000000000021", SessionID: "managed-session", GlobalSeq: 21, Role: "assistant", Content: "hello from managed", CreatedAt: 125}},
			{Type: "assistant.delta", SessionID: "managed-session", RunID: "managed-run", Delta: "hello"},
		}
		for _, frame := range frames {
			raw, err := json.Marshal(frame)
			if err != nil {
				t.Fatalf("marshal frame: %v", err)
			}
			if err := conn.WriteMessage(gorillaws.TextMessage, raw); err != nil {
				t.Fatalf("write frame: %v", err)
			}
		}
	}))
	defer managed.Close()
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: map[string]any{"swarm_managed_host_session": true, "swarm_managed_host_swarm_id": "managed-swarm", "swarm_managed_host_backend_url": managed.URL}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}
	seedManagedHostTarget(t, server, managed.URL)

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer primary.Close()
	wsURL := "ws" + strings.TrimPrefix(primary.URL, "http") + "/v1/sessions/managed-session/run/stream?swarm_id=managed-swarm"
	client, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial primary websocket: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial primary websocket: %v", err)
	}
	defer client.Close()
	if err := client.WriteMessage(gorillaws.TextMessage, []byte(`{"type":"run.start","prompt":"hello managed"}`)); err != nil {
		t.Fatalf("write start: %v", err)
	}
	for i := 0; i < 3; i++ {
		_, raw, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read proxied frame %d: %v", i, err)
		}
		var frame runStreamWireEvent
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode proxied frame %d: %v", i, err)
		}
		if frame.SessionID != "managed-session" || frame.RunID != "managed-run" {
			t.Fatalf("proxied frame %d = %+v", i, frame)
		}
	}

	messages, err := sessionSvc.ListMessages("managed-session", 0, 30)
	if err != nil {
		t.Fatalf("list mirrored messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "hello from managed" || messages[0].GlobalSeq != 21 {
		t.Fatalf("mirrored messages = %+v", messages)
	}
	lifecycle, ok, err := sessionSvc.GetLifecycle("managed-session")
	if err != nil {
		t.Fatalf("get mirrored lifecycle: %v", err)
	}
	if !ok || lifecycle.RunID != "managed-run" || !lifecycle.Active {
		t.Fatalf("mirrored lifecycle ok=%v lifecycle=%+v", ok, lifecycle)
	}
	state, sub, replay, err := server.runStreams.subscribe("managed-run", 0)
	if err != nil {
		t.Fatalf("subscribe mirrored run stream: %v", err)
	}
	defer server.runStreams.unsubscribe("managed-run", sub.id)
	if state.sessionID != "managed-session" {
		t.Fatalf("run stream session = %q, want managed-session", state.sessionID)
	}
	if len(replay) != 3 {
		t.Fatalf("replay len = %d, want 3", len(replay))
	}
}

func TestManagedHostRunStreamWebsocketProxyPersistsSessionTitle(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != peerManagedHostSessionRunStreamPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		conn, err := (&gorillaws.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade managed websocket: %v", err)
		}
		defer conn.Close()
		_, rawStart, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read start frame: %v", err)
		}
		var start runStreamInboundMessage
		if err := json.Unmarshal(rawStart, &start); err != nil {
			t.Fatalf("decode start frame: %v", err)
		}
		if start.SessionID != "managed-session" || start.Type != "run.start" {
			t.Fatalf("start frame = %+v", start)
		}
		titleFrame := runStreamWireEvent{Type: runruntime.StreamEventSessionTitle, SessionID: "managed-session", RunID: "managed-run", Title: "Managed title from host", TitleStage: "final", UpdatedAt: 456}
		rawTitle, err := json.Marshal(titleFrame)
		if err != nil {
			t.Fatalf("marshal title frame: %v", err)
		}
		if err := conn.WriteMessage(gorillaws.TextMessage, rawTitle); err != nil {
			t.Fatalf("write title frame: %v", err)
		}
	}))
	defer managed.Close()
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: map[string]any{"swarm_managed_host_session": true, "swarm_managed_host_swarm_id": "managed-swarm", "swarm_managed_host_backend_url": managed.URL}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}
	seedManagedHostTarget(t, server, managed.URL)

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, withTestPrincipal(r))
	}))
	defer primary.Close()
	wsURL := "ws" + strings.TrimPrefix(primary.URL, "http") + "/v1/sessions/managed-session/run/stream?swarm_id=managed-swarm"
	client, resp, err := gorillaws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial primary websocket: %v status=%d", err, resp.StatusCode)
		}
		t.Fatalf("dial primary websocket: %v", err)
	}
	defer client.Close()
	if err := client.WriteMessage(gorillaws.TextMessage, []byte(`{"type":"run.start","prompt":"title me"}`)); err != nil {
		t.Fatalf("write start: %v", err)
	}
	_, raw, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("read title frame: %v", err)
	}
	var frame runStreamWireEvent
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode title frame: %v", err)
	}
	if frame.Type != runruntime.StreamEventSessionTitle || frame.Title != "Managed title from host" || frame.TitleStage != "final" || frame.SessionID != "managed-session" {
		t.Fatalf("proxied title frame = %+v", frame)
	}
	mirrored, ok, err := sessionSvc.GetSession("managed-session")
	if err != nil {
		t.Fatalf("get mirrored session: %v", err)
	}
	if !ok || mirrored.Title != "Managed title from host" {
		t.Fatalf("mirrored session ok=%v title=%q", ok, mirrored.Title)
	}
}

func TestPeerManagedHostSessionEventPublishesToPrimaryRunStream(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "managed-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "managed-swarm", RuntimeWorkspacePath: "/managed/workspace"}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, peerManagedHostSessionEventPath, bytes.NewBufferString(`{"session_id":"managed-session","event_type":"assistant.delta","payload":{"type":"assistant.delta","session_id":"managed-session","run_id":"managed-run","delta":"hello"},"causation_id":"managed-run"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, "managed-swarm")
	req.Header.Set(peerAuthTokenHeader, "managed-token")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	state, sub, replay, err := server.runStreams.subscribe("managed-run", 0)
	if err != nil {
		t.Fatalf("subscribe mirrored run stream: %v", err)
	}
	defer server.runStreams.unsubscribe("managed-run", sub.id)
	if state.sessionID != "managed-session" {
		t.Fatalf("run stream session = %q, want managed-session", state.sessionID)
	}
	if len(replay) != 1 {
		t.Fatalf("replay len = %d, want 1", len(replay))
	}
	var frame runStreamWireEvent
	if err := json.Unmarshal(replay[0].payload, &frame); err != nil {
		t.Fatalf("decode replay frame: %v", err)
	}
	if frame.Type != "assistant.delta" || frame.RunID != "managed-run" || frame.SessionID != "managed-session" || frame.Delta != "hello" || frame.Seq == 0 {
		t.Fatalf("replay frame = %+v", frame)
	}
}

func TestPeerManagedHostSessionMessageRequiresPeerAuth(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	req := httptest.NewRequest(http.MethodPost, peerManagedHostSessionMessagePath, bytes.NewBufferString(`{"session_id":"managed-session","role":"user","content":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestPeerManagedHostSessionOpenPersistsTrustedRouteForFollowupMessage(t *testing.T) {
	server, sessionSvc, _, routeStore, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "managed-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)

	openPayload, err := json.Marshal(peerManagedHostSessionOpenRequest{
		SessionID: "managed-session",
		Request: func() managedHostSessionCreateRequest {
			req := managedHostSessionCreateRequest{
				Title:                "managed",
				WorkspacePath:        "/managed/workspace",
				HostWorkspacePath:    "/managed/workspace",
				RuntimeWorkspacePath: "/managed/workspace",
				WorkspaceName:        "workspace",
				Mode:                 sessionruntime.ModeAuto,
				AgentName:            "swarm",
			}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Route: managedHostSessionRoute{
			UserID:                testPrincipal().UserID,
			AccountScopeID:        testPrincipal().AccountScopeID,
			PrimarySwarmID:        "host-swarm-id",
			PrimaryBackendURL:     "http://primary.example.test",
			ManagedHostSwarmID:    "managed-swarm",
			ManagedHostBackendURL: "http://127.0.0.1:7782",
			HostWorkspacePath:     "/host/workspace",
			RuntimeWorkspacePath:  "/managed/workspace",
		},
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	openReq := httptest.NewRequest(http.MethodPost, peerManagedHostSessionOpenPath, bytes.NewReader(openPayload))
	openReq.Header.Set("Content-Type", "application/json")
	openReq.Header.Set(peerAuthSwarmIDHeader, "host-swarm-id")
	openReq.Header.Set(peerAuthTokenHeader, "peer-token")
	openRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open status = %d, want %d, body=%s", openRec.Code, http.StatusOK, openRec.Body.String())
	}

	route, ok, err := routeStore.Get("managed-session")
	if err != nil || !ok {
		t.Fatalf("route persisted ok=%t err=%v", ok, err)
	}
	if route.UserID != testPrincipal().UserID || route.AccountScopeID != testPrincipal().AccountScopeID || route.ChildSwarmID != "managed-swarm" || route.HostSwarmID != "host-swarm-id" {
		t.Fatalf("route = %+v", route)
	}

	messageReq := httptest.NewRequest(http.MethodPost, peerManagedHostSessionMessagePath, bytes.NewBufferString(`{"session_id":"managed-session","role":"user","content":"hello"}`))
	messageReq.Header.Set("Content-Type", "application/json")
	messageReq.Header.Set(peerAuthSwarmIDHeader, "host-swarm-id")
	messageReq.Header.Set(peerAuthTokenHeader, "peer-token")
	messageRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(messageRec, messageReq)
	if messageRec.Code != http.StatusOK {
		t.Fatalf("message status = %d, want %d, body=%s", messageRec.Code, http.StatusOK, messageRec.Body.String())
	}
	messages, err := sessionSvc.ListMessages("managed-session", 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "hello" {
		t.Fatalf("messages = %+v", messages)
	}
}

func seedManagedHostTarget(t *testing.T, server *Server, backendURL string) {
	t.Helper()
	nodes := server.swarmNodes
	if nodes == nil {
		t.Fatal("swarm node store not configured")
	}
	if _, err := nodes.Put(pebblestore.SwarmNodeRecord{SwarmID: "managed-swarm", Name: "Managed Host", Role: swarmruntime.RelationshipManaged, Kind: "manual", Transport: startupconfig.NetworkModeTailscale, BackendURL: backendURL, Status: "online"}); err != nil {
		t.Fatalf("put managed node: %v", err)
	}
	server.SetSwarmNodeStore(nodes)
	server.swarmTargetHealth.entries = map[string]swarmTargetHealthEntry{
		"host|managed-swarm|" + backendURL:   {online: true, checkedAt: time.Now()},
		"manual|managed-swarm|" + backendURL: {online: true, checkedAt: time.Now()},
	}
}

func TestHostedPermissionSyncUsesManagedHostRunStreamPath(t *testing.T) {
	server, sessionSvc, permissionSvc, _ := newRoutedSessionTestServer(t)
	permissionSvc.SetLocalSwarmIDResolver(func() string { return "managed-swarm" })
	server.SetSwarmService(fakeRoutedSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "managed-swarm", Name: "managed-swarm", Role: "worker"}}, token: "peer-token"})
	server.SetSessionRouteStore(nil)

	var streamPeerHits atomic.Int32
	var legacyPermissionHits atomic.Int32
	var received managedHostPermissionControlRequest
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/swarm/peer/permissions/") {
			legacyPermissionHits.Add(1)
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != peerManagedHostSessionRunStreamPath {
			http.NotFound(w, r)
			return
		}
		streamPeerHits.Add(1)
		if r.Header.Get(peerAuthSwarmIDHeader) != "managed-swarm" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth headers = %q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeJSON(w, http.StatusOK, managedHostPermissionControlResponse{OK: true, Type: managedHostPermissionControlResult, RequestID: received.RequestID, SessionID: received.SessionID, Permission: pebblestore.PermissionRecord{ID: "perm-managed", SessionID: received.SessionID, RunID: received.RunID, CallID: received.CallID, ToolName: "bash", Status: pebblestore.PermissionStatusPending}})
	}))
	defer primary.Close()

	if _, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "managed-session",
		Title:         "Managed Session",
		WorkspacePath: "/managed/workspace",
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModePlan,
		Preference:    &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "medium"},
		Metadata: sessionruntime.HostedSessionDescriptor{
			HostSwarmID:          "primary-swarm",
			HostBackendURL:       primary.URL,
			HostWorkspacePath:    "/host/workspace",
			RuntimeWorkspacePath: "/managed/workspace",
			ChildSwarmID:         "managed-swarm",
			OwnerTransport:       "routed_session_peer",
		}.WithMetadata(nil),
	}); err != nil {
		t.Fatalf("create hosted session: %v", err)
	}

	record, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: "managed-session", RunID: "run-1", CallID: "call-1", ToolName: "bash", ToolArguments: `{"cmd":"pwd"}`, Requirement: "tool", Mode: "plan"})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	if streamPeerHits.Load() != 1 {
		t.Fatalf("stream peer hits = %d, want 1", streamPeerHits.Load())
	}
	if legacyPermissionHits.Load() != 0 {
		t.Fatalf("legacy permission hits = %d, want 0", legacyPermissionHits.Load())
	}
	if received.Type != managedHostPermissionControlCreate || received.SessionID != "managed-session" || received.Input.ToolName != "bash" {
		t.Fatalf("permission control request = %+v", received)
	}
	if record.ID != "perm-managed" || record.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("record = %+v", record)
	}
}

func TestPrimaryResolvePublishesPermissionUpdateToManagedHostEventPath(t *testing.T) {
	server, sessionSvc, permissionSvc, _ := newRoutedSessionTestServer(t)
	server.SetSessionRouteStore(nil)
	var eventPeerHits atomic.Int32
	var streamPeerHits atomic.Int32
	var received managedHostSessionEventRequest
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		switch r.URL.Path {
		case peerManagedHostSessionEventPath:
			eventPeerHits.Add(1)
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatalf("decode event request: %v", err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case peerManagedHostSessionRunStreamPath:
			streamPeerHits.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer managed.Close()

	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "plan", Metadata: map[string]any{"swarm_managed_host_session": true, "swarm_managed_host_swarm_id": "managed-swarm", "swarm_managed_host_backend_url": managed.URL}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}
	record, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: "managed-session", RunID: "managed-run", CallID: "call-1", ToolName: "bash", ToolArguments: `{"cmd":"pwd"}`, Requirement: "tool", Mode: "plan"})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	req := withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/sessions/managed-session/permissions/"+record.ID+"/resolve", bytes.NewBufferString(`{"action":"approve","reason":"ok"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if eventPeerHits.Load() != 1 {
		t.Fatalf("event peer hits = %d, want 1", eventPeerHits.Load())
	}
	if streamPeerHits.Load() != 0 {
		t.Fatalf("stream peer hits = %d, want 0 for resolution propagation", streamPeerHits.Load())
	}
	if received.SessionID != "managed-session" || received.EventType != "permission.updated" {
		t.Fatalf("event request = %+v", received)
	}
	payloadPermission, ok := received.Payload["permission"].(map[string]any)
	if !ok || payloadPermission["id"] != record.ID || payloadPermission["status"] != pebblestore.PermissionStatusApproved {
		t.Fatalf("permission payload = %#v", received.Payload["permission"])
	}
}

func TestPeerManagedHostSessionEventStoresMirroredPermission(t *testing.T) {
	server, sessionSvc, permissionSvc, routeStore := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: "managed-session", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "managed-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "managed-swarm", RuntimeWorkspacePath: "/managed/workspace"}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, peerManagedHostSessionEventPath, bytes.NewBufferString(`{"session_id":"managed-session","event_type":"permission.updated","payload":{"type":"permission.updated","session_id":"managed-session","run_id":"managed-run","permission":{"id":"perm-managed","session_id":"managed-session","run_id":"managed-run","call_id":"call-1","tool_name":"bash","status":"approved","decision":"allow_once","reason":"ok"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, "managed-swarm")
	req.Header.Set(peerAuthTokenHeader, "managed-token")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	stored, err := permissionSvc.ListPermissions("managed-session", 10)
	if err != nil {
		t.Fatalf("list mirrored permissions: %v", err)
	}
	if len(stored) != 1 || stored[0].ID != "perm-managed" || stored[0].Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("stored mirrored permission = %+v", stored)
	}
}

func TestManagedHostPermissionControlPostResolvesOnPrimary(t *testing.T) {
	server, sessionSvc, permissionSvc, _ := newRoutedSessionTestServer(t)
	if _, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "managed-session", Title: "Managed", WorkspacePath: "/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModePlan, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "gpt-5.4", Thinking: "medium"}}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	pending, err := permissionSvc.CreatePending(permission.CreateInput{SessionID: "managed-session", RunID: "managed-run", CallID: "call-1", ToolName: "bash", ToolArguments: `{"cmd":"pwd"}`, Requirement: "tool", Mode: "plan"})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	body, err := json.Marshal(managedHostPermissionControlRequest{Type: managedHostPermissionControlResolve, SessionID: "managed-session", PermissionID: pending.ID, ResolveInput: permission.ResolveInput{SessionID: "managed-session", PermissionID: pending.ID, Action: "approve", Reason: "ok"}})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, peerManagedHostSessionRunStreamPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, "managed-swarm")
	req.Header.Set(peerAuthTokenHeader, "managed-token")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response managedHostPermissionControlResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || response.Permission.ID != pending.ID || response.Permission.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("response = %+v", response)
	}
}

func TestManagedHostPermissionControlClientResolveUsesRunStreamPath(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	var streamPeerHits atomic.Int32
	var legacyPermissionHits atomic.Int32
	var received managedHostPermissionControlRequest
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/swarm/peer/permissions/") {
			legacyPermissionHits.Add(1)
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != peerManagedHostSessionRunStreamPath {
			http.NotFound(w, r)
			return
		}
		streamPeerHits.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeJSON(w, http.StatusOK, managedHostPermissionControlResponse{OK: true, Type: managedHostPermissionControlResult, RequestID: received.RequestID, SessionID: received.SessionID, Permission: pebblestore.PermissionRecord{ID: received.PermissionID, SessionID: received.SessionID, Status: pebblestore.PermissionStatusApproved}})
	}))
	defer primary.Close()

	client := NewManagedHostPermissionControlClient(server)
	result, err := client.Resolve(context.Background(), sessionruntime.HostedSessionDescriptor{HostSwarmID: "primary-swarm", HostBackendURL: primary.URL, ChildSwarmID: "managed-swarm", OwnerTransport: "routed_session_peer"}, permission.ResolveInput{SessionID: "managed-session", PermissionID: "perm-1", Action: "approve", Reason: "ok"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if streamPeerHits.Load() != 1 || legacyPermissionHits.Load() != 0 {
		t.Fatalf("stream hits=%d legacy hits=%d", streamPeerHits.Load(), legacyPermissionHits.Load())
	}
	if received.Type != managedHostPermissionControlResolve || received.ResolveInput.PermissionID != "perm-1" {
		t.Fatalf("control request = %+v", received)
	}
	if result.Record.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagedHostSessionOpenRejectsPeerMismatchedSessionOwnership(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != peerManagedHostSessionOpenPath {
			http.NotFound(w, r)
			return
		}
		var req peerManagedHostSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"session": pebblestore.SessionSnapshot{ID: req.SessionID, UserID: "user-b", AccountScopeID: "account-b", WorkspacePath: req.Request.WorkspacePath, WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: req.Request.Metadata, CreatedAt: 1, UpdatedAt: 2},
		})
	}))
	defer managed.Close()

	seedManagedHostTarget(t, server, managed.URL)
	req := httptest.NewRequest(http.MethodPost, managedHostSessionOpenPath, bytes.NewBufferString(`{"title":"managed","workspace_path":"/host/workspace","host_workspace_path":"/host/workspace","runtime_workspace_path":"/managed/workspace","workspace_name":"workspace","mode":"auto","agent_name":"swarm","preference":{"provider":"codex","model":"gpt-5.5","thinking":"high"},"target_swarm_id":"managed-swarm"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
}

func TestPeerManagedHostSessionOpenRejectsSpoofedForwardedPrincipalHeaders(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "managed-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
	payload, err := json.Marshal(peerManagedHostSessionOpenRequest{
		SessionID: "managed-session",
		Request:   managedHostSessionCreateRequest{Title: "managed", WorkspacePath: "/managed/workspace", RuntimeWorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, AgentName: "swarm"},
		Route:     managedHostSessionRoute{UserID: "user-b", AccountScopeID: "account-b", PrimarySwarmID: "host-swarm-id", ManagedHostSwarmID: "managed-swarm", RuntimeWorkspacePath: "/managed/workspace"},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, peerManagedHostSessionOpenPath, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Swarm-Principal-User-ID", testPrincipal().UserID)
	req.Header.Set("X-Swarm-Principal-Account-Scope-ID", testPrincipal().AccountScopeID)
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerManagedHostSessionOpen(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestManagedHostSessionMessageRejectsCrossAccountGuessedSessionID(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("managed peer should not be called for cross-account session")
	}))
	defer managed.Close()
	seedManagedHostTarget(t, server, managed.URL)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "managed-session", UserID: "user-a", AccountScopeID: "account-a", WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: map[string]any{"swarm_managed_host_session": true, "swarm_managed_host_swarm_id": "managed-swarm"}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: "managed-session", UserID: "user-a", AccountScopeID: "account-a", ChildSwarmID: "managed-swarm", ChildBackendURL: managed.URL, HostSwarmID: "managed-swarm", RuntimeWorkspacePath: "/managed/workspace"}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, managedHostSessionMessagePath, bytes.NewBufferString(`{"session_id":"managed-session","role":"user","content":"hello managed"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, requestWithTestPrincipalForAccount(req, "user-b", "account-b"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestManagedHostSessionOpenPropagatesWorktreeModeToPeer(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	var openedWorktreeMode atomic.Value
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != peerManagedHostSessionOpenPath {
			http.NotFound(w, r)
			return
		}
		var req peerManagedHostSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		openedWorktreeMode.Store(req.Request.WorktreeMode)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": pebblestore.SessionSnapshot{
				ID:               req.SessionID,
				UserID:           req.Route.UserID,
				AccountScopeID:   req.Route.AccountScopeID,
				WorkspacePath:    "/var/cache/swarmd/workspaces/managed-repo/worktrees/ws_managed_open",
				WorkspaceName:    "workspace",
				Title:            "Managed",
				Mode:             "auto",
				Metadata:         req.Request.Metadata,
				WorktreeEnabled:  true,
				WorktreeRootPath: "/var/cache/swarmd/workspaces/managed-repo",
				WorktreeBranch:   "agent/managed-open",
				CreatedAt:        1,
				UpdatedAt:        2,
			},
		})
	}))
	defer managed.Close()

	seedManagedHostTarget(t, server, managed.URL)
	req := httptest.NewRequest(http.MethodPost, managedHostSessionOpenPath, bytes.NewBufferString(`{"title":"managed","workspace_path":"/host/workspace","host_workspace_path":"/host/workspace","runtime_workspace_path":"/managed/workspace","workspace_name":"workspace","mode":"auto","agent_name":"swarm","worktree_mode":"on","preference":{"provider":"codex","model":"gpt-5.5","thinking":"high"},"target_swarm_id":"managed-swarm"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, _ := openedWorktreeMode.Load().(string); got != "on" {
		t.Fatalf("peer worktree_mode = %q, want on", got)
	}
	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Session.WorktreeEnabled || payload.Session.WorkspacePath != "/var/cache/swarmd/workspaces/managed-repo/worktrees/ws_managed_open" || payload.Session.WorktreeRootPath != "/var/cache/swarmd/workspaces/managed-repo" || payload.Session.WorktreeBranch != "agent/managed-open" {
		t.Fatalf("mirrored worktree fields = %+v", payload.Session)
	}
}

func TestPeerManagedHostSessionOpenAllocatesRequestedWorktree(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "managed-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
	server.SetWorktreeService(&fakeWorktreeService{
		config: worktreeruntime.Config{Enabled: true, UseCurrentBranch: true},
		allocation: worktreeruntime.Allocation{
			WorkspacePath: "/var/cache/swarmd/workspaces/managed-repo/worktrees/ws_peer_managed_open",
			RepoRoot:      "/var/cache/swarmd/workspaces/managed-repo",
			BaseBranch:    "main",
			BranchName:    "agent/managed-peer-open",
			WorkspaceID:   "ws_peer_managed_open",
		},
	})

	payload, err := json.Marshal(peerManagedHostSessionOpenRequest{
		SessionID: "managed-worktree-session",
		Request: func() managedHostSessionCreateRequest {
			req := managedHostSessionCreateRequest{Title: "managed", WorkspacePath: "/managed/workspace", HostWorkspacePath: "/managed/workspace", RuntimeWorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, AgentName: "swarm", WorktreeMode: "on"}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Route: managedHostSessionRoute{
			UserID:                testPrincipal().UserID,
			AccountScopeID:        testPrincipal().AccountScopeID,
			PrimarySwarmID:        "host-swarm-id",
			PrimaryBackendURL:     "http://primary.example.test",
			ManagedHostSwarmID:    "managed-swarm",
			ManagedHostBackendURL: "http://127.0.0.1:7782",
			HostWorkspacePath:     "/host/workspace",
			RuntimeWorkspacePath:  "/managed/workspace",
		},
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, peerManagedHostSessionOpenPath, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, "host-swarm-id")
	req.Header.Set(peerAuthTokenHeader, "peer-token")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	session, ok, err := sessionSvc.GetSession("managed-worktree-session")
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if !session.WorktreeEnabled {
		t.Fatalf("WorktreeEnabled = false, session = %+v", session)
	}
	if session.WorkspacePath != "/var/cache/swarmd/workspaces/managed-repo/worktrees/ws_peer_managed_open" || session.WorktreeRootPath != "/var/cache/swarmd/workspaces/managed-repo" || session.WorktreeBranch != "agent/managed-peer-open" {
		t.Fatalf("worktree fields = %+v", session)
	}
}
