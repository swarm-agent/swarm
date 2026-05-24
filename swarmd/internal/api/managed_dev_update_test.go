package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedHostUpdateRunRoutesToPeerUpdateRun(t *testing.T) {
	server := newManagedGitSyncTestServer(t)
	var peerHits int
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != peerUpdateRunPath {
			t.Fatalf("peer path=%q want %q", r.URL.Path, peerUpdateRunPath)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth=%q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		peerHits++
		writeJSON(w, http.StatusAccepted, managedHostPeerUpdateRunResponse{OK: true})
	}))
	t.Cleanup(peer.Close)
	source := initGitCommitTestRepo(t)
	seedManagedGitSyncTopologyBinding(t, server, source, peer.URL)

	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, managedHostUpdateRunPath, strings.NewReader(`{"target_swarm_id":"managed-swarm"}`)))
	rec := httptest.NewRecorder()
	server.handleManagedHostUpdateRun(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if peerHits != 1 {
		t.Fatalf("peer hits=%d want 1", peerHits)
	}
}
