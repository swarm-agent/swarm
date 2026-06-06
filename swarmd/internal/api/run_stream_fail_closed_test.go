package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRoutedRunStreamWebsocketMissingStoredRouteFailsClosedBeforeUpgrade(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	var hits atomic.Int32
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer child.Close()
	if err := server.topology.UpsertRuntime(pebblestore.TopologyRuntimeRecord{
		SwarmID:        "selected-child",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Name:           "selected child",
		Relationship:   "child",
		BackendURL:     child.URL,
		Status:         "attached",
	}); err != nil {
		t.Fatalf("upsert selected runtime: %v", err)
	}
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:             "session-ws-missing-route",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Title:          "Routed websocket without route",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"owner_transport": "routed_session_peer",
			sessionruntime.HostedSessionMetadataHostWorkspacePath:    "/host/workspace",
			sessionruntime.HostedSessionMetadataRuntimeWorkspacePath: "/runtime/workspace",
			sessionruntime.HostedSessionMetadataChildSwarmID:         "missing-child",
			"swarm_routed_workspace_binding_id":                      "binding-missing-route",
		},
		CreatedAt: 1,
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}
	if _, err := server.swarmDesktopTargetSelection.PutForAccount(testPrincipal().AccountScopeID, testPrincipal().UserID, "selected-child"); err != nil {
		t.Fatalf("select target: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/session-ws-missing-route/run/stream?swarm_id=selected-child", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "routed session is missing canonical stored route") {
		t.Fatalf("body = %s, want missing canonical route error", rec.Body.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("selected target hits = %d, want 0", hits.Load())
	}
}

func TestSessionRequiresCanonicalStoredRouteForWorkspaceBindingMetadata(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:             "session-binding-only-route-required",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Title:          "Routed binding metadata without route",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"swarm_routed_workspace_binding_id": "binding-missing-route",
		},
		CreatedAt: 1,
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}

	requires, err := server.sessionRequiresCanonicalStoredRoute("session-binding-only-route-required")
	if err != nil {
		t.Fatalf("requires route: %v", err)
	}
	if !requires {
		t.Fatal("workspace binding metadata session did not require canonical stored route")
	}
}

func TestRoutedRunStreamControlMissingStoredRouteDoesNotUseLocalRunner(t *testing.T) {
	server, sessionSvc, _, _ := newRoutedSessionTestServer(t)
	if _, err := sessionSvc.StoreMirroredSession(pebblestore.SessionSnapshot{
		ID:             "session-run-missing-route-local",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		WorkspacePath:  "/host/workspace",
		WorkspaceName:  "workspace",
		Title:          "Routed run without route",
		Mode:           sessionruntime.ModeAuto,
		Metadata: map[string]any{
			"owner_transport": "routed_session_peer",
			sessionruntime.HostedSessionMetadataHostWorkspacePath:    "/host/workspace",
			sessionruntime.HostedSessionMetadataRuntimeWorkspacePath: "/runtime/workspace",
			sessionruntime.HostedSessionMetadataChildSwarmID:         "missing-child",
			"swarm_routed_workspace_binding_id":                      "binding-missing-route",
		},
		CreatedAt: 1,
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("store mirrored session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session-run-missing-route-local/run/stream", bytes.NewBufferString(`{"type":"run.start","prompt":"hello","background":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, withTestPrincipal(req))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("routed session is missing canonical stored route")) {
		t.Fatalf("body = %s, want missing canonical route error", rec.Body.String())
	}
	if server.runStreams != nil {
		server.runStreams.mu.Lock()
		runCount := len(server.runStreams.runs)
		server.runStreams.mu.Unlock()
		if runCount != 0 {
			t.Fatalf("run stream count = %d, want 0", runCount)
		}
	}
}
