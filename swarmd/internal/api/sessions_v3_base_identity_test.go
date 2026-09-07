package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Requirement: primary create must persist allocator-owned immutable base identity.
// Threat: a successful create otherwise fails before provider execution, or client
// metadata can forge the base. The registered HTTP create plus durable reload is
// the narrowest layer proving persistence and replay, separate from real-Git allocation.
func TestSessionsV3CreatePersistsAllocatorBase(t *testing.T) {
	server, _, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	binding := seedSessionsV3PrimaryAuthority(t, server, "/host/swarm-go")
	base := strings.Repeat("a", 40)
	fake := &fakeWorktreeService{allocation: worktreeruntime.Allocation{RepoRoot: "/host/swarm-go", BaseCommit: base}}
	server.SetWorktreeService(fake)
	body, _ := json.Marshal(map[string]any{"session_id": "base-proof", "client_request_id": "base-proof", "swarm_id": "host-swarm-id", "workspace_binding_id": binding, "agent_name": "swarm", "mode": "auto", "worktree_mode": "on", "worktree_branch_name": "agent/base-proof", "metadata": map[string]any{"base_commit": strings.Repeat("b", 40)}, "preference": map[string]string{"provider": "codex", "model": "gpt-5.4", "thinking": "medium"}})
	bad := httptest.NewRecorder()
	server.Handler().ServeHTTP(bad, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions", strings.NewReader(string(body)))))
	if bad.Code != http.StatusBadRequest || fake.lastNameSeed != "" {
		t.Fatal("forged base was not rejected before allocation")
	}
	if _, found, err := server.sessions.GetSession("base-proof"); err != nil || found {
		t.Fatal("forged create persisted state")
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	delete(request, "metadata")
	body, _ = json.Marshal(request)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, withTestPrincipal(httptest.NewRequest(http.MethodPost, "/v3/sessions", strings.NewReader(string(body)))))
		if rec.Code != http.StatusOK {
			t.Fatalf("create/replay: %d %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Session pebblestore.SessionSnapshot `json:"session"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		stored, ok, err := server.sessions.GetSession(response.Session.ID)
		if err != nil || !ok {
			t.Fatalf("reload: %v %v", ok, err)
		}
		if stored.Metadata["base_commit"] != base || response.Session.Metadata["base_commit"] != base {
			t.Fatalf("allocator base lost or forged: %v", stored.Metadata["base_commit"])
		}
		fake.allocation.BaseCommit = strings.Repeat("c", 40)
	}
}
