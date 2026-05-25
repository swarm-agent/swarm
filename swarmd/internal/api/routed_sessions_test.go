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
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	agentruntime "swarm/packages/swarmd/internal/agent"
	deployruntime "swarm/packages/swarmd/internal/deploy"
	"swarm/packages/swarmd/internal/identity"
	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	remotedeploy "swarm/packages/swarmd/internal/remotedeploy"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestPeerSessionOpenPersistsRouteForManagedHostContainer(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:              "child-swarm",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		Name:                 "managed child",
		Relationship:         "child",
		BackendURL:           "http://127.0.0.1:7782",
		OwnerHostSwarmID:     "managed-swarm",
		OwnerHostContainerID: "managed-container",
	}); err != nil {
		t.Fatalf("upsert child runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:        "managed-swarm",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Name:           "managed host",
		Relationship:   "managed",
		BackendURL:     "https://managed.example.test",
	}); err != nil {
		t.Fatalf("upsert managed runtime: %v", err)
	}

	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-managed-child",
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{
				Title:                "managed child",
				WorkspacePath:        "/workspaces/swarm-go",
				HostWorkspacePath:    "/workspaces/swarm-go",
				RuntimeWorkspacePath: "/workspaces/swarm-go",
				WorkspaceName:        "swarm-go",
				Mode:                 sessionruntime.ModeAuto,
				AgentName:            "swarm",
			}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted: sessionruntime.HostedSessionDescriptor{
			HostSwarmID:          "managed-swarm",
			HostBackendURL:       "https://primary.example.test",
			HostWorkspacePath:    "/host/swarm-go",
			RuntimeWorkspacePath: "/workspaces/swarm-go",
			ChildSwarmID:         "child-swarm",
			OwnerTransport:       "routed_session_peer",
		},
		Route: pebblestore.SessionRouteRecord{
			SessionID:            "session-managed-child",
			ChildSwarmID:         "child-swarm",
			ChildBackendURL:      "http://127.0.0.1:7782",
			HostSwarmID:          "managed-swarm",
			HostContainerID:      "managed-container",
			HostWorkspacePath:    "/host/swarm-go",
			RuntimeWorkspacePath: "/workspaces/swarm-go",
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, ok, err := sessionSvc.GetSession("session-managed-child"); err != nil || !ok {
		t.Fatalf("get child session ok=%t err=%v", ok, err)
	}
	route, ok, err := routeStore.Get("session-managed-child")
	if err != nil || !ok {
		t.Fatalf("get session route ok=%t err=%v", ok, err)
	}
	if route.ChildSwarmID != "child-swarm" || route.HostSwarmID != "managed-swarm" || route.HostContainerID != "managed-container" || route.ChildBackendURL != "http://127.0.0.1:7782" {
		t.Fatalf("session route = %+v", route)
	}
	topologyRoute, ok, err := server.topology.GetSessionRouteForAccount(testPrincipal().AccountScopeID, "session-managed-child")
	if err != nil || !ok {
		t.Fatalf("get topology session route ok=%t err=%v", ok, err)
	}
	if topologyRoute.RuntimeSwarmID != "child-swarm" || topologyRoute.HostSwarmID != "managed-swarm" || topologyRoute.HostContainerID != "managed-container" || topologyRoute.BackendURL != "http://127.0.0.1:7782" || topologyRoute.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("topology session route = %+v", topologyRoute)
	}
	target, ok, err := server.routedSessionTarget(testPrincipal(), "session-managed-child")
	if err != nil || !ok {
		t.Fatalf("routed session target ok=%t err=%v", ok, err)
	}
	if target.BackendURL != "https://managed.example.test" || target.HostSwarmID != "managed-swarm" {
		t.Fatalf("routed target = %+v", target)
	}
}

func TestProxyBackendURLUsesChildLoopbackOnOwnerHost(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		SwarmID:        "managed-swarm",
		Relationship:   "managed",
		BackendURL:     "http://127.0.0.1:7782",
	}); err != nil {
		t.Fatalf("upsert managed runtime: %v", err)
	}
	got := server.proxyBackendURLForTarget(swarmTarget{
		SwarmID:     "child-swarm",
		Kind:        "mirrored",
		HostSwarmID: "managed-swarm",
		BackendURL:  "http://127.0.0.1:7782",
	})
	if got != "http://127.0.0.1:7782" {
		t.Fatalf("backend url = %q, want child loopback backend", got)
	}
}

func TestRoutedRunStreamControlProxiesHostedMirrorSession(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var requestPath atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		requestPath.Store(r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": "session-routed", "run_id": "run-1"})
	}))
	defer child.Close()

	sessionID := seedRoutedSession(t, sessionSvc)
	if _, _, err := sessionSvc.UpdateMetadata(sessionID, map[string]any{
		"swarm_managed_host_session":  "true",
		"swarm_managed_host_swarm_id": "managed-swarm",
	}); err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      child.URL,
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	body := bytes.NewBufferString(`{"type":"run.start","prompt":"hello","background":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sessionID+"/run/stream", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}
	if got, _ := requestPath.Load().(string); got != "/v1/sessions/"+sessionID+"/run/stream" {
		t.Fatalf("child path = %q, want %q", got, "/v1/sessions/"+sessionID+"/run/stream")
	}
}

func TestRoutedSessionTargetRewritesManagedLoopbackBackendToHostBackend(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	managedHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		var req peerSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode child request: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             req.SessionID,
				"title":          req.Request.Title,
				"workspace_path": req.Request.RuntimeWorkspacePath,
				"workspace_name": req.Request.WorkspaceName,
				"mode":           req.Request.Mode,
				"created_at":     1,
				"updated_at":     2,
			},
		})
	}))
	defer managedHost.Close()
	if _, err := server.swarmMirror.UpsertRemoteResource("managed-swarm", pebblestore.SwarmMirrorEventRecord{
		Sequence:  1,
		EventType: pebblestore.SwarmMirrorEventTypeUpsert,
		Kind:      mirrorResourceTarget,
		ID:        "target:child-swarm",
		Resource:  []byte(`{"swarm_id":"child-swarm","name":"managed child","role":"child","relationship":"child","kind":"remote","backend_url":"` + managedHost.URL + `","online":true,"selectable":true}`),
	}); err != nil {
		t.Fatalf("upsert mirrored target: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		SwarmID:              "child-swarm",
		Name:                 "managed child",
		Role:                 "child",
		Relationship:         "child",
		BackendURL:           "http://127.0.0.1:7782",
		Status:               "attached",
		OwnerHostSwarmID:     "managed-swarm",
		OwnerHostContainerID: "managed-swarm:container-1",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		SwarmID:        "managed-swarm",
		Name:           "managed host",
		Relationship:   "managed",
		BackendURL:     managedHost.URL,
		Status:         "online",
	}); err != nil {
		t.Fatalf("upsert managed runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            "session-managed-child",
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "managed-swarm",
		HostContainerID:      "managed-swarm:container-1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm-go",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), "session-managed-child")
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if !ok || target == nil {
		t.Fatal("routed target not found")
	}
	if target.BackendURL != managedHost.URL {
		t.Fatalf("backend url = %q, want managed host backend", target.BackendURL)
	}
	if target.HostSwarmID != "managed-swarm" {
		t.Fatalf("host swarm id = %q, want managed-swarm", target.HostSwarmID)
	}

	body := bytes.NewBufferString(`{"title":"managed child","mode":"plan","workspace_path":"/host/workspace","host_workspace_path":"/host/workspace","runtime_workspace_path":"/workspaces/swarm-go","workspace_name":"swarm-go","preference":{"provider":"fireworks","model":"accounts/fireworks/models/kimi-k2p6","thinking":"low"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=child-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	createdRoute, ok, err := server.topology.GetSessionRouteForAccount(testPrincipal().AccountScopeID, payload.Session.ID)
	if err != nil || !ok {
		t.Fatalf("get created topology route ok=%t err=%v", ok, err)
	}
	if createdRoute.HostSwarmID != "managed-swarm" {
		t.Fatalf("created route host swarm id = %q, want managed-swarm", createdRoute.HostSwarmID)
	}
}

var errTestRemoteUpdateFailure = errors.New("remote update failed")

func TestPeerSessionEventStoresAndPublishesMirroredRunEvent(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-live-peer", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Flow live", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	payload := []byte(`{"session_id":"session-live-peer","event_type":"run.assistant.delta","payload":{"type":"assistant.delta","session_id":"session-live-peer","run_id":"run-live","delta":"hello"},"causation_id":"run-live"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/event", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.handlePeerSessionEvent(rec, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	events, err := server.events.ReadFrom(1, 20)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	foundDelta := false
	for _, event := range events {
		if event.EventType == "run.assistant.delta" && event.EntityID == "session-live-peer" {
			foundDelta = true
			break
		}
	}
	if !foundDelta {
		t.Fatalf("events = %+v", events)
	}
}

func TestPeerSessionMetadataDerivesPrincipalFromPersistedSessionAndRoute(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-peer-metadata", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/runtime/workspace", WorkspaceName: "workspace", Title: "Peer metadata", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{SessionID: "session-peer-metadata", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", HostWorkspacePath: "/host/workspace", RuntimeWorkspacePath: "/runtime/workspace"}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/metadata", bytes.NewReader([]byte(`{"session_id":"session-peer-metadata","metadata":{"background_run":{"active":true}}}`)))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionMetadata(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	updated, ok, err := sessionSvc.GetSession("session-peer-metadata")
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if _, ok := updated.Metadata["background_run"]; !ok {
		t.Fatalf("metadata = %+v", updated.Metadata)
	}
}

func TestPeerSessionMetadataRejectsWithoutPersistedRoute(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-peer-metadata-orphan", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/runtime/workspace", WorkspaceName: "workspace", Title: "Peer metadata", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/metadata", bytes.NewReader([]byte(`{"session_id":"session-peer-metadata-orphan","metadata":{"background_run":{"active":true}}}`)))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionMetadata(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestPeerSessionEventRejectsCrossAccountPrincipal(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-account-a", UserID: "user-a", AccountScopeID: "account-a", WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Flow live", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	payload := []byte(`{"session_id":"session-account-a","event_type":"run.assistant.delta","payload":{"type":"assistant.delta","session_id":"session-account-a","run_id":"run-live","delta":"hello"},"causation_id":"run-live"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/event", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.handlePeerSessionEvent(rec, requestWithTestPrincipalForAccount(req, "user-b", "account-b"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	stored, ok, err := sessionSvc.GetSession("session-account-a")
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if stored.AccountScopeID != "account-a" || stored.UserID != "user-a" {
		t.Fatalf("stored session = %+v", stored)
	}
}

func TestPeerSessionOpenRejectsSpoofedForwardedPrincipalWithoutAccountOwnedRuntime(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "child-swarm", UserID: "user-a", AccountScopeID: "account-a", Name: "child", Relationship: "child", BackendURL: "http://127.0.0.1:7782"}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-spoofed",
		Request:   sessionCreateRequest{Title: "spoofed", WorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto},
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-spoofed", UserID: "user-b", AccountScopeID: "account-b", ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", RuntimeWorkspacePath: "/workspaces/swarm-go"},
		Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-b", AccountScopeID: "account-b", AccountScopeSource: identity.AccountScopeSourceSession},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestPeerSessionOpenAcceptsPairedChildPrincipalClaimWithoutLocalTopologyRuntime(t *testing.T) {
	server, sessionSvc, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", testPrincipal().UserID, testPrincipal().AccountScopeID)
	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-paired-child",
		Request: func() sessionCreateRequest {
			req := sessionCreateRequest{Title: "paired child", WorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto}
			req.Preference.Provider = "codex"
			req.Preference.Model = "gpt-5.4"
			req.Preference.Thinking = "medium"
			return req
		}(),
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-paired-child", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", RuntimeWorkspacePath: "/workspaces/swarm-go"},
		Principal: testPrincipal(),
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	session, ok, err := sessionSvc.GetSession("session-paired-child")
	if err != nil || !ok {
		t.Fatalf("get session ok=%t err=%v", ok, err)
	}
	if session.UserID != testPrincipal().UserID || session.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("session principal = %q/%q", session.UserID, session.AccountScopeID)
	}
}

func TestPeerSessionOpenRejectsPairedChildPrincipalClaimForWrongAccount(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	configureRoutedSessionTestServerAsChild(t, server, swarmStore, "child-swarm", "host-swarm-id", "user-a", "account-a")
	payload, err := json.Marshal(peerSessionOpenRequest{
		SessionID: "session-wrong-account",
		Request:   sessionCreateRequest{Title: "wrong account", WorkspacePath: "/workspaces/swarm-go", RuntimeWorkspacePath: "/workspaces/swarm-go", WorkspaceName: "swarm-go", Mode: sessionruntime.ModeAuto},
		Hosted:    sessionruntime.HostedSessionDescriptor{HostSwarmID: "host-swarm-id", RuntimeWorkspacePath: "/workspaces/swarm-go", ChildSwarmID: "child-swarm", OwnerTransport: "routed_session_peer"},
		Route:     pebblestore.SessionRouteRecord{SessionID: "session-wrong-account", UserID: "user-b", AccountScopeID: "account-b", ChildSwarmID: "child-swarm", ChildBackendURL: "http://127.0.0.1:7782", HostSwarmID: "host-swarm-id", RuntimeWorkspacePath: "/workspaces/swarm-go"},
		Principal: identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-b", AccountScopeID: "account-b", AccountScopeSource: identity.AccountScopeSourceSession},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/open", bytes.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), peerAuthAuthorizedContextKey, peerAuthContextValue{SwarmID: "host-swarm-id"}))
	rec := httptest.NewRecorder()
	server.handlePeerSessionOpen(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestStoreMirroredSessionWithEventPublishesCreatedEvent(t *testing.T) {
	_, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	session, event, err := sessionSvc.StoreMirroredSessionWithEvent(pebblestore.SessionSnapshot{ID: "session-live-created", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Flow live", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}
	if event == nil || event.EventType != "session.created" || event.EntityID != session.ID {
		t.Fatalf("event = %+v, session = %+v", event, session)
	}
	_, event, err = sessionSvc.StoreMirroredSessionWithEvent(session)
	if err != nil {
		t.Fatalf("store mirrored session again: %v", err)
	}
	if event != nil {
		t.Fatalf("duplicate store emitted event: %+v", event)
	}
}

func TestPeerSessionEventStoresMirroredPayloadMessage(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{ID: "session-live-message", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, WorkspacePath: "/host/workspace", WorkspaceName: "workspace", Title: "Flow live", Mode: sessionruntime.ModeAuto, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("store session: %v", err)
	}

	payload := []byte(`{"session_id":"session-live-message","event_type":"run.message.stored","payload":{"type":"message.stored","session_id":"session-live-message","run_id":"run-live","message":{"id":"msg_00000000000000000007","session_id":"session-live-message","global_seq":7,"role":"assistant","content":"mirrored payload","created_at":123}}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/swarm/peer/sessions/event", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.handlePeerSessionEvent(rec, requestWithTestPrincipalForAccount(req, testPrincipal().UserID, testPrincipal().AccountScopeID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	messages, err := sessionSvc.ListMessages("session-live-message", 0, 20)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "mirrored payload" || messages[0].GlobalSeq != 7 {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestRoutedSessionMessagesReloadFromHostWithoutProxy(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)
	if _, _, _, err := sessionSvc.AppendMessage(sessionID, "user", "hello from host mirror", nil); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/messages?limit=10", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(payload.Messages))
	}
	if payload.Messages[0].Content != "hello from host mirror" {
		t.Fatalf("message content = %q, want %q", payload.Messages[0].Content, "hello from host mirror")
	}
}

func TestRoutedSessionGetUsesStoredRouteWithoutSwarmID(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var requestPath atomic.Value
	var requestQuery atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		requestPath.Store(r.URL.Path)
		requestQuery.Store(r.URL.RawQuery)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             "session-routed",
				"title":          "Remote child session",
				"workspace_path": "/workspaces/swarm",
				"workspace_name": "child swarm",
				"mode":           "auto",
				"created_at":     1,
				"updated_at":     2,
				"metadata": map[string]any{
					"swarm_routed_session":                true,
					"swarm_routed_host_workspace_path":    "/host/workspace",
					"swarm_routed_runtime_workspace_path": "/workspaces/swarm",
				},
			},
		})
	}))
	defer child.Close()

	sessionID := "session-routed"
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{SwarmID: "child-swarm", UserID: testPrincipal().UserID, AccountScopeID: testPrincipal().AccountScopeID, Name: "child", Relationship: "child", BackendURL: child.URL}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      child.URL,
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      child.URL,
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/workspaces/swarm",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}
	if got, _ := requestPath.Load().(string); got != "/v1/sessions/"+sessionID {
		t.Fatalf("child path = %q, want %q", got, "/v1/sessions/"+sessionID)
	}
	if got, _ := requestQuery.Load().(string); got != "" {
		t.Fatalf("child query = %q, want empty", got)
	}
	var payload struct {
		Session struct {
			WorkspacePath string `json:"workspace_path"`
			WorkspaceName string `json:"workspace_name"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Session.WorkspacePath != "/workspaces/swarm" || payload.Session.WorkspaceName != "child swarm" {
		t.Fatalf("session identity = %q/%q, want remote child", payload.Session.WorkspacePath, payload.Session.WorkspaceName)
	}
}

func TestLocalChildTargetUsesChildPeerAuthWhenOwnerHostIsSelf(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	target := swarmTarget{
		SwarmID:     "child-swarm",
		Kind:        "local",
		HostSwarmID: "host-swarm-id",
		BackendURL:  "http://127.0.0.1:7782",
	}
	if peerSwarmID := server.peerAuthSwarmIDForTarget(target); peerSwarmID != "child-swarm" {
		t.Fatalf("peer auth swarm id = %q, want child-swarm", peerSwarmID)
	}
	if token, err := server.outgoingPeerAuthTokenForTarget(nil, target); err != nil || token != "peer-token" {
		t.Fatalf("target token = %q err=%v, want peer-token", token, err)
	}
}

func TestPrincipalForManagedHostSessionRunUsesStoredSessionPrincipal(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)

	principal := server.principalForManagedHostSessionRun(sessionID)
	if principal.UserID != testPrincipal().UserID || principal.AccountScopeID != testPrincipal().AccountScopeID {
		t.Fatalf("principal = %q/%q, want session principal %q/%q", principal.UserID, principal.AccountScopeID, testPrincipal().UserID, testPrincipal().AccountScopeID)
	}
}

func TestRoutedSessionTargetSkipsLocalRuntimeSelfRoute(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-local-runtime-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "host-swarm-id",
		ChildBackendURL:      "http://127.0.0.1:7781",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/host/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "host-swarm-id",
		ChildBackendURL:      "http://127.0.0.1:7781",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/host/workspace",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if ok || target != nil {
		t.Fatalf("routed target ok=%t target=%+v, want local handling", ok, target)
	}
}

func TestRoutedSessionTargetKeepsLocalChildLoopbackBackendWhenOwnerHostIsSelf(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-local-child-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:           testPrincipal().UserID,
		AccountScopeID:   testPrincipal().AccountScopeID,
		SwarmID:          "child-swarm",
		Name:             "child",
		Relationship:     "child",
		BackendURL:       "http://127.0.0.1:7782",
		Status:           "attached",
		OwnerHostSwarmID: "host-swarm-id",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostSwarmID:          "host-swarm-id",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if !ok || target == nil {
		t.Fatalf("routed target not found")
	}
	if target.BackendURL != "http://127.0.0.1:7782" {
		t.Fatalf("target backend url = %q, want child loopback backend", target.BackendURL)
	}
	if token, err := server.outgoingPeerAuthTokenForTarget(nil, *target); err != nil || token != "peer-token" {
		t.Fatalf("target token = %q err=%v, want peer-token", token, err)
	}
}

func TestRoutedSessionTargetUsesOwnerHostPeerAuth(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		UserID:           testPrincipal().UserID,
		AccountScopeID:   testPrincipal().AccountScopeID,
		SwarmID:          "child-swarm",
		Name:             "child",
		Relationship:     "child",
		BackendURL:       "http://127.0.0.1:7782",
		Status:           "attached",
		OwnerHostSwarmID: "managed-swarm",
	}); err != nil {
		t.Fatalf("upsert runtime: %v", err)
	}
	if _, err := server.topology.UpsertSessionRoute(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:7782",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("upsert topology route: %v", err)
	}

	target, ok, err := server.routedSessionTarget(testPrincipal(), sessionID)
	if err != nil {
		t.Fatalf("routed target: %v", err)
	}
	if !ok || target == nil {
		t.Fatalf("routed target not found")
	}
	if target.HostSwarmID != "managed-swarm" {
		t.Fatalf("target host swarm id = %q, want managed-swarm", target.HostSwarmID)
	}
	if target.Kind != "mirrored" {
		t.Fatalf("target kind = %q, want mirrored", target.Kind)
	}
	if token, err := server.outgoingPeerAuthTokenForTarget(nil, *target); err != nil || token != "peer-token" {
		t.Fatalf("target token = %q err=%v, want peer-token", token, err)
	}
}

func TestRoutedRunStreamControlUsesStoredRouteWithoutSwarmID(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var requestPath atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		requestPath.Store(r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": "session-routed", "run_id": "run-1"})
	}))
	defer child.Close()

	sessionID := "session-routed"
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      child.URL,
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	body := bytes.NewBufferString(`{"type":"run.start","prompt":"hello","background":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sessionID+"/run/stream", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}
	if got, _ := requestPath.Load().(string); got != "/v1/sessions/"+sessionID+"/run/stream" {
		t.Fatalf("child path = %q, want %q", got, "/v1/sessions/"+sessionID+"/run/stream")
	}
}

func TestRoutedSessionPermissionsReadAndResolveFromHostWithoutProxy(t *testing.T) {
	server, sessionSvc, permSvc, routeStore := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}
	record, err := permSvc.CreatePending(permission.CreateInput{
		SessionID:     sessionID,
		RunID:         "run-1",
		CallID:        "call-1",
		ToolName:      "bash",
		ToolArguments: `{"cmd":"pwd"}`,
		Requirement:   "tool",
		Mode:          "plan",
	})
	if err != nil {
		t.Fatalf("create pending permission: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/permissions?limit=200", nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("permission list status = %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	var getPayload struct {
		Count       int `json:"count"`
		Permissions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode permission list: %v", err)
	}
	if getPayload.Count != 1 || len(getPayload.Permissions) != 1 {
		t.Fatalf("permission count = %d/%d, want 1", getPayload.Count, len(getPayload.Permissions))
	}
	if getPayload.Permissions[0].ID != record.ID || getPayload.Permissions[0].Status != pebblestore.PermissionStatusPending {
		t.Fatalf("unexpected permission payload: %+v", getPayload.Permissions[0])
	}

	resolveReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+sessionID+"/permissions/"+record.ID+"/resolve", bytes.NewBufferString(`{"action":"approve","reason":"ok"}`))
	resolveReq.Header.Set("Content-Type", "application/json")
	resolveRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(resolveRec, withTestPrincipal(resolveReq))
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("permission resolve status = %d, want %d, body=%s", resolveRec.Code, http.StatusOK, resolveRec.Body.String())
	}
	var resolvePayload struct {
		Permission struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"permission"`
	}
	if err := json.Unmarshal(resolveRec.Body.Bytes(), &resolvePayload); err != nil {
		t.Fatalf("decode permission resolve: %v", err)
	}
	if resolvePayload.Permission.ID != record.ID || resolvePayload.Permission.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("unexpected resolved permission: %+v", resolvePayload.Permission)
	}
}

func TestSessionsListWithSwarmIDReadsHostWithoutProxy(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	server.SetDeployContainerService(&fakeReplicateDeployService{})
	sessionID := seedRoutedSession(t, sessionSvc)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions?swarm_id=child-swarm-1", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Sessions) != 1 || payload.Sessions[0].ID != sessionID {
		t.Fatalf("session list = %+v, want session %q", payload.Sessions, sessionID)
	}
}

func TestRoutedFlowSessionFetchReturnsCanonicalHostMirror(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := "session-flow-routed"
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Title:          "Flow smoke",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"source":            "flow",
			"lineage_kind":      "flow",
			"owner_transport":   "flow_scheduler",
			"flow_id":           "flow-routed",
			"swarm_target_name": "swarm child 4",
			sessionruntime.HostedSessionMetadataEnabled:              true,
			sessionruntime.HostedSessionMetadataHostSwarmID:          "host-swarm-id",
			sessionruntime.HostedSessionMetadataChildSwarmID:         "child-swarm",
			sessionruntime.HostedSessionMetadataHostWorkspacePath:    "/host/workspace",
			sessionruntime.HostedSessionMetadataRuntimeWorkspacePath: "/runtime/workspace",
		},
		CreatedAt: 1,
		UpdatedAt: 2,
	}); err != nil {
		t.Fatalf("store flow mirror: %v", err)
	}
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Session.WorkspacePath != "/host/workspace" || payload.Session.Metadata["swarm_target_name"] != "swarm child 4" || payload.Session.Metadata["source"] != "flow" {
		t.Fatalf("session payload = %+v", payload.Session)
	}
}

func TestRoutedSessionPreferenceReadFromHostWithoutProxy(t *testing.T) {
	server, sessionSvc, _, routeStore := newRoutedSessionTestServer(t)
	sessionID := seedRoutedSession(t, sessionSvc)
	if _, err := routeStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            sessionID,
		UserID:               testPrincipal().UserID,
		AccountScopeID:       testPrincipal().AccountScopeID,
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:1",
		HostWorkspacePath:    "/host/workspace",
		RuntimeWorkspacePath: "/runtime/workspace",
	}); err != nil {
		t.Fatalf("put route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/preference", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Preference struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Thinking string `json:"thinking"`
		} `json:"preference"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Preference.Provider != "codex" || payload.Preference.Model != "gpt-5.4" || payload.Preference.Thinking != "medium" {
		t.Fatalf("unexpected preference payload: %+v", payload.Preference)
	}
}

func TestRemoteDeploySessionCreateUsesRemotePayloadTargetPath(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var openedWorkspacePath atomic.Value
	var openedHostBackendURL atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		var req peerSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode child request: %v", err)
		}
		openedWorkspacePath.Store(req.Request.RuntimeWorkspacePath)
		hostBackendURL := req.Hosted.HostBackendURL
		openedHostBackendURL.Store(hostBackendURL)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer child.Close()

	server.SetRemoteDeployService(&fakeRemoteDeployService{
		sessions: []remotedeploy.Session{{
			ID:               "remote-deploy-1",
			Name:             "remote-child",
			Status:           "attached",
			ChildSwarmID:     "child-swarm",
			HostAPIBaseURL:   "https://remote-host.tailnet.ts.net",
			RemoteTailnetURL: child.URL,
			Preflight: remotedeploy.SessionPreflight{
				Payloads: []remotedeploy.SessionPayload{{
					WorkspacePath: "/src/swarm-go",
					WorkspaceName: "swarm-go",
					TargetPath:    "/workspaces/swarm-go",
				}},
			},
		}},
	})

	body := bytes.NewBufferString(`{"title":"remote","mode":"plan","workspace_path":"/src/swarm-go","workspace_name":"swarm-go","preference":{"provider":"fireworks","model":"accounts/fireworks/models/kimi-k2p5","thinking":"high"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=child-swarm", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, _ := openedWorkspacePath.Load().(string); got != "/workspaces/swarm-go" {
		t.Fatalf("child workspace path = %q, want %q", got, "/workspaces/swarm-go")
	}
	if got, _ := openedHostBackendURL.Load().(string); got != "https://remote-host.tailnet.ts.net" {
		t.Fatalf("child host backend url = %q, want %q", got, "https://remote-host.tailnet.ts.net")
	}
	var payload struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	route, ok, err := routeStore.Get(payload.Session.ID)
	if err != nil {
		t.Fatalf("load route: %v", err)
	}
	if !ok {
		t.Fatalf("route missing for session %q", payload.Session.ID)
	}
	if route.RuntimeWorkspacePath != "/workspaces/swarm-go" {
		t.Fatalf("runtime workspace path = %q, want %q", route.RuntimeWorkspacePath, "/workspaces/swarm-go")
	}
}

func TestRemoteSessionCreateUsesRegistryMagicDNSBackend(t *testing.T) {
	server, _, _, routeStore := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	var openedWorkspacePath atomic.Value
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/swarm/peer/sessions/open" {
			http.NotFound(w, r)
			return
		}
		var req peerSessionOpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode child request: %v", err)
		}
		openedWorkspacePath.Store(req.Request.RuntimeWorkspacePath)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id":             req.SessionID,
				"title":          req.Request.Title,
				"workspace_path": req.Request.RuntimeWorkspacePath,
				"workspace_name": req.Request.WorkspaceName,
				"mode":           req.Request.Mode,
				"created_at":     1,
				"updated_at":     2,
			},
		})
	}))
	defer child.Close()

	nodes := server.swarmNodes
	if nodes == nil {
		t.Fatal("swarm node store not configured")
	}
	if _, err := nodes.Put(pebblestore.SwarmNodeRecord{
		SwarmID:      "registry-child",
		Name:         "registry child",
		Role:         "child",
		Kind:         "remote",
		Transport:    "tailscale",
		BackendURL:   child.URL,
		MagicDNSName: "registry-child.tailnet.ts.net",
		DeploymentID: "remote-deploy-registry",
		Source:       "remote-deploy",
		Status:       "online",
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}
	server.SetSwarmNodeStore(nodes)
	server.swarmTargetHealth.entries = map[string]swarmTargetHealthEntry{
		"remote|registry-child|" + child.URL: {online: true, checkedAt: time.Now()},
	}

	body := bytes.NewBufferString(`{"title":"remote","mode":"plan","workspace_path":"/src/swarm-go","workspace_name":"swarm-go","preference":{"provider":"fireworks","model":"accounts/fireworks/models/kimi-k2p5","thinking":"high"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions?swarm_id=registry-child", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("child hits = %d, want 1", hits.Load())
	}
	if got, _ := openedWorkspacePath.Load().(string); got != "/src/swarm-go" {
		t.Fatalf("child workspace path = %q, want %q", got, "/src/swarm-go")
	}
	var payload struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	route, ok, err := routeStore.Get(payload.Session.ID)
	if err != nil {
		t.Fatalf("load route: %v", err)
	}
	if !ok {
		t.Fatalf("route missing for session %q", payload.Session.ID)
	}
	if route.ChildSwarmID != "registry-child" || route.ChildBackendURL != child.URL {
		t.Fatalf("route child = %q/%q, want registry-child/%q", route.ChildSwarmID, route.ChildBackendURL, child.URL)
	}
}

func TestRemoteDeploySessionStartIsRetired(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	fake := &fakeRemoteDeployService{}
	server.SetRemoteDeployService(fake)

	body := bytes.NewBufferString(`{"session_id":"remote-start-1","tailscale_auth_key":"tskey-launch-only"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/remote/session/start", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusGone, rec.Body.String())
	}
	if fake.lastStartInput.SessionID != "" || fake.lastStartInput.TailscaleAuthKey != "" {
		t.Fatalf("retired start path called remote deploy service: %+v", fake.lastStartInput)
	}
	var payload struct {
		PathID string `json:"path_id"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.PathID != remotedeploy.PathSessionStart {
		t.Fatalf("path_id = %q, want %q", payload.PathID, remotedeploy.PathSessionStart)
	}
	if !strings.Contains(payload.Error, "SSH remote deploy is retired") {
		t.Fatalf("error = %q, want retired guidance", payload.Error)
	}
}

func TestRemoteDeploySessionUpdateJobReturnsPartialResultOnConflict(t *testing.T) {
	server, _, _, _ := newRoutedSessionTestServer(t)
	fake := &fakeRemoteDeployService{
		updateJobResult: remotedeploy.UpdateJobResult{
			PathID: remotedeploy.PathSessionUpdateJob,
			Summary: remotedeploy.UpdateJobSummary{
				Total:  1,
				Failed: 1,
			},
		},
		updateJobErr: errTestRemoteUpdateFailure,
	}
	server.SetRemoteDeployService(fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy/remote/session/update-job", bytes.NewBufferString(`{"dev_mode":true,"post_rebuild_check":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var payload struct {
		OK     bool                         `json:"ok"`
		Result remotedeploy.UpdateJobResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.OK || payload.Result.Summary.Failed != 1 {
		t.Fatalf("payload = %+v", payload)
	}
}

func configureRoutedSessionTestServerAsChild(t *testing.T, server *Server, swarmStore *pebblestore.SwarmStore, childSwarmID, parentSwarmID, userID, accountScopeID string) {
	t.Helper()
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: childSwarmID, Name: "child-swarm", Role: "child"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{PairingState: startupconfig.PairingStatePaired, ParentSwarmID: parentSwarmID, UserID: userID, AccountScopeID: accountScopeID}); err != nil {
		t.Fatalf("put local pairing: %v", err)
	}
	server.SetSwarmStore(swarmStore)
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: childSwarmID, Name: "child-swarm", Role: "child"}},
		token: "peer-token",
	})
	startupPath := filepath.Join(t.TempDir(), "child-swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.Child = true
	cfg.SwarmName = "child-swarm"
	cfg.ParentSwarmID = parentSwarmID
	cfg.PairingState = startupconfig.PairingStatePaired
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7782
	cfg.AdvertisePort = 7782
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write child startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
}

func newRoutedSessionTestServer(t *testing.T) (*Server, *sessionruntime.Service, *permission.Service, *pebblestore.SessionRouteStore) {
	server, sessionSvc, permissionSvc, routeStore, _ := newRoutedSessionTestServerWithSwarmStore(t)
	return server, sessionSvc, permissionSvc, routeStore
}

func newRoutedSessionTestServerWithSwarmStore(t *testing.T) (*Server, *sessionruntime.Service, *permission.Service, *pebblestore.SessionRouteStore, *pebblestore.SwarmStore) {
	t.Helper()
	t.Setenv("SWARM_API_NO_AUTH", "1")

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "routed-session-api.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}

	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), eventLog)
	modelSvc := modelruntime.NewService(pebblestore.NewModelStore(store), eventLog, nil)
	permissionSvc := permission.NewService(pebblestore.NewPermissionStore(store), eventLog, nil)
	permissionSvc.SetSessionResolver(sessionSvc)
	agentSvc := agentruntime.NewService(pebblestore.NewAgentStore(store), eventLog)
	if err := agentSvc.EnsureDefaults(); err != nil {
		t.Fatalf("ensure agent defaults: %v", err)
	}
	if _, _, _, err := agentSvc.UpsertForAccount(testPrincipal().AccountScopeID, agentruntime.UpsertInput{Name: "swarm", Mode: agentruntime.ModePrimary, Enabled: pebblestore.BoolPtr(true)}); err != nil {
		t.Fatalf("create swarm agent: %v", err)
	}
	routeStore := pebblestore.NewSessionRouteStore(store)
	nodeStore := pebblestore.NewSwarmNodeStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	server := NewServer(nil, agentSvc, modelSvc, nil, sessionSvc, nil, nil, nil, nil, permissionSvc, nil, eventLog, stream.NewHub(eventLog))
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(store), nil, nil, nil, nil, nil, routeStore, pebblestore.NewWorkspaceStore(store)))
	server.SetSessionRouteStore(routeStore)
	server.SetSwarmNodeStore(nodeStore)
	server.SetSwarmStore(swarmStore)
	server.SetSwarmMirrorStore(pebblestore.NewSwarmMirrorStore(store))
	server.SetSwarmService(fakeRoutedSwarmService{
		state: swarmruntime.LocalState{
			Node: swarmruntime.LocalNodeState{
				SwarmID: "host-swarm-id",
				Name:    "host-swarm",
				Role:    "master",
			},
		},
		token: "peer-token",
	})

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmName = "host-swarm"
	cfg.Host = "127.0.0.1"
	cfg.AdvertiseHost = "127.0.0.1"
	cfg.Port = 7781
	cfg.AdvertisePort = 7781
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)

	return server, sessionSvc, permissionSvc, routeStore, swarmStore
}

func seedRoutedSession(t *testing.T, sessionSvc *sessionruntime.Service) string {
	t.Helper()

	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-routed",
		Title:          "Routed Session",
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModePlan,
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Preference: &pebblestore.ModelPreference{
			Provider: "codex",
			Model:    "gpt-5.4",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return session.ID
}

type fakeRoutedSwarmService struct {
	state swarmruntime.LocalState
	token string
}

type fakeRemoteDeployService struct {
	sessions        []remotedeploy.Session
	lastStartInput  remotedeploy.StartSessionInput
	startResult     remotedeploy.Session
	startErr        error
	updateJobResult remotedeploy.UpdateJobResult
	updateJobErr    error
}

func (f *fakeRemoteDeployService) List(_ context.Context) ([]remotedeploy.Session, error) {
	return append([]remotedeploy.Session(nil), f.sessions...), nil
}

func (f *fakeRemoteDeployService) ListCached(_ context.Context) ([]remotedeploy.Session, error) {
	return append([]remotedeploy.Session(nil), f.sessions...), nil
}

func (f *fakeRemoteDeployService) Get(_ context.Context, sessionID string, _ bool) (remotedeploy.Session, error) {
	for _, session := range f.sessions {
		if session.ID == sessionID {
			return session, nil
		}
	}
	return remotedeploy.Session{}, errors.New("remote deploy session not found")
}

func (f *fakeRemoteDeployService) Create(_ context.Context, input remotedeploy.CreateSessionInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeRemoteDeployService) UpdateSettings(_ context.Context, input remotedeploy.UpdateSettingsInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeRemoteDeployService) Delete(_ context.Context, input remotedeploy.DeleteSessionInput) (localcontainers.DeleteResult, error) {
	return localcontainers.DeleteResult{}, nil
}

func (f *fakeRemoteDeployService) Start(_ context.Context, input remotedeploy.StartSessionInput) (remotedeploy.Session, error) {
	f.lastStartInput = input
	if f.startErr != nil {
		return remotedeploy.Session{}, f.startErr
	}
	return f.startResult, nil
}

func (f *fakeRemoteDeployService) RunUpdateJob(_ context.Context, input remotedeploy.UpdateJobInput) (remotedeploy.UpdateJobResult, error) {
	return f.updateJobResult, f.updateJobErr
}

func (f *fakeRemoteDeployService) Approve(_ context.Context, input remotedeploy.ApproveSessionInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeRemoteDeployService) ChildStatus(_ context.Context, input remotedeploy.ChildStatusInput) (remotedeploy.Session, error) {
	return remotedeploy.Session{}, nil
}

func (f *fakeRemoteDeployService) SyncCredentialBundle(_ context.Context, input remotedeploy.SyncCredentialRequestInput) (deployruntime.ContainerSyncCredentialBundle, error) {
	return deployruntime.ContainerSyncCredentialBundle{}, nil
}

func (f fakeRoutedSwarmService) EnsureLocalState(swarmruntime.EnsureLocalStateInput) (swarmruntime.LocalState, error) {
	return f.state, nil
}

func (f fakeRoutedSwarmService) RenameLocalSwarm(input swarmruntime.RenameLocalSwarmInput) (swarmruntime.LocalState, error) {
	state := f.state
	state.Node.Name = strings.TrimSpace(input.Name)
	return state, nil
}

func (f fakeRoutedSwarmService) ListGroupsForSwarm(string, int) ([]swarmruntime.GroupState, string, error) {
	return nil, "", nil
}

func (f fakeRoutedSwarmService) UpsertGroup(swarmruntime.UpsertGroupInput) (swarmruntime.Group, error) {
	return swarmruntime.Group{}, nil
}

func (f fakeRoutedSwarmService) DeleteGroup(string) error {
	return nil
}

func (f fakeRoutedSwarmService) SetCurrentGroup(string, string) (swarmruntime.GroupState, error) {
	return swarmruntime.GroupState{}, nil
}

func (f fakeRoutedSwarmService) OutgoingPeerAuthToken(string) (string, bool, error) {
	return f.token, true, nil
}

func (f fakeRoutedSwarmService) ValidateIncomingPeerAuth(string, string) (bool, error) {
	return true, nil
}

func (f fakeRoutedSwarmService) UpsertGroupMember(swarmruntime.UpsertGroupMemberInput) (swarmruntime.GroupMember, error) {
	return swarmruntime.GroupMember{}, nil
}

func (f fakeRoutedSwarmService) RemoveGroupMember(swarmruntime.RemoveGroupMemberInput) error {
	return nil
}

func (f fakeRoutedSwarmService) CreateInvite(swarmruntime.CreateInviteInput) (swarmruntime.Invite, error) {
	return swarmruntime.Invite{}, nil
}

func (f fakeRoutedSwarmService) SubmitEnrollment(swarmruntime.SubmitEnrollmentInput) (swarmruntime.Enrollment, error) {
	return swarmruntime.Enrollment{}, nil
}

func (f fakeRoutedSwarmService) ListPendingEnrollments(int) ([]swarmruntime.Enrollment, error) {
	return nil, nil
}

func (f fakeRoutedSwarmService) DecideEnrollment(swarmruntime.DecideEnrollmentInput) (swarmruntime.Enrollment, []swarmruntime.TrustedPeer, error) {
	return swarmruntime.Enrollment{}, nil, nil
}

func (f fakeRoutedSwarmService) PrepareRemoteBootstrapParentPeer(swarmruntime.PrepareRemoteBootstrapParentPeerInput) error {
	return nil
}

func (f fakeRoutedSwarmService) ApproveManagedPairing(swarmruntime.ApproveManagedPairingInput) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeRoutedSwarmService) TrustManagedPeer(swarmruntime.TrustManagedPeerInput) (swarmruntime.TrustedPeer, error) {
	return swarmruntime.TrustedPeer{}, nil
}

func (f fakeRoutedSwarmService) RemoveManagedPeer(swarmruntime.RemoveManagedPeerInput) (swarmruntime.RemoveManagedPeerResult, error) {
	return swarmruntime.RemoveManagedPeerResult{}, nil
}

func (f fakeRoutedSwarmService) UpdateLocalPairingFromConfig(startupconfig.FileConfig, []swarmruntime.TransportSummary) (swarmruntime.PairingState, error) {
	return swarmruntime.PairingState{}, nil
}

func (f fakeRoutedSwarmService) DetachToStandalone(string) error {
	return nil
}

var _ swarmService = fakeRoutedSwarmService{}
