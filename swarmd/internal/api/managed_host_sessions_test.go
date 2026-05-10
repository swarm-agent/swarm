package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestManagedHostSessionMessageUsesNewPeerAPIWithAuthAndMirrors(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
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
			"session": pebblestore.SessionSnapshot{ID: "managed-session", WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: map[string]any{"swarm_managed_host_session": true}, CreatedAt: 1, UpdatedAt: 123, MessageCount: 1, LastMessageAt: 123},
		})
	}))
	defer managed.Close()

	seedManagedHostTarget(t, server, managed.URL)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:            "managed-session",
		WorkspacePath: "/managed/workspace",
		WorkspaceName: "workspace",
		Title:         "Managed",
		Mode:          "auto",
		Metadata: map[string]any{
			"swarm_managed_host_session":  true,
			"swarm_managed_host_swarm_id": "managed-swarm",
		},
		CreatedAt: 1,
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, managedHostSessionMessagePath, bytes.NewBufferString(`{"session_id":"managed-session","role":"user","content":"hello managed"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

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
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	var openedWorkspacePath atomic.Value
	var openedHostWorkspacePath atomic.Value
	var openedRuntimeWorkspacePath atomic.Value
	var openedMetadata atomic.Value
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
		openedWorkspacePath.Store(req.Request.WorkspacePath)
		openedHostWorkspacePath.Store(req.Request.HostWorkspacePath)
		openedRuntimeWorkspacePath.Store(req.Request.RuntimeWorkspacePath)
		openedMetadata.Store(req.Request.Metadata)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"session": pebblestore.SessionSnapshot{ID: req.SessionID, WorkspacePath: req.Request.WorkspacePath, WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: req.Request.Metadata, CreatedAt: 1, UpdatedAt: 2},
		})
	}))
	defer managed.Close()

	seedManagedHostTarget(t, server, managed.URL)
	req := httptest.NewRequest(http.MethodPost, managedHostSessionOpenPath, bytes.NewBufferString(`{"title":"managed","workspace_path":"/host/workspace","host_workspace_path":"/host/workspace","runtime_workspace_path":"/managed/workspace","workspace_name":"workspace","mode":"auto","agent_name":"swarm","preference":{"provider":"codex","model":"gpt-5.5","thinking":"high"},"target_swarm_id":"managed-swarm"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
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
	if metadata[sessionruntime.HostedSessionMetadataEnabled] != true || metadata[sessionruntime.HostedSessionMetadataHostSwarmID] != "host-swarm-id" || metadata[sessionruntime.HostedSessionMetadataHostBackendURL] != "http://127.0.0.1:7781" || metadata[sessionruntime.HostedSessionMetadataHostWorkspacePath] != "/host/workspace" || metadata[sessionruntime.HostedSessionMetadataRuntimeWorkspacePath] != "/managed/workspace" || metadata[sessionruntime.HostedSessionMetadataChildSwarmID] != "managed-swarm" {
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
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "managed-session", WorkspacePath: "/managed/workspace", WorkspaceName: "workspace", Title: "Managed", Mode: "auto", Metadata: map[string]any{"swarm_managed_host_session": true, "swarm_managed_host_swarm_id": "managed-swarm"}, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store mirror: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/managed-session/run/stream?swarm_id=managed-swarm", bytes.NewBufferString(`{"type":"run.start","prompt":"hello managed","background":true}`))
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

func TestPeerManagedHostSessionEventPublishesToPrimaryRunStream(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
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
		"host|managed-swarm|" + backendURL: {online: true, checkedAt: time.Now()},
	}
}
