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
	if _, err := nodes.Put(pebblestore.SwarmNodeRecord{SwarmID: "managed-swarm", Name: "Managed Host", Role: swarmruntime.RelationshipManaged, Kind: "host", Transport: startupconfig.NetworkModeTailscale, BackendURL: backendURL, Status: "online"}); err != nil {
		t.Fatalf("put managed node: %v", err)
	}
	server.SetSwarmNodeStore(nodes)
	server.swarmTargetHealth.entries = map[string]swarmTargetHealthEntry{
		"host|managed-swarm|" + backendURL: {online: true, checkedAt: time.Now()},
	}
}
