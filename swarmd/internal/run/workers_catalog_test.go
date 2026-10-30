package run

import (
	"context"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: Service.ManageWorkers is the one worker implementation behind the
// catalog's manage_workers. For a caller with no conversation (Swarm Control
// or a linked home: key scopes in ctx) it must never read or reuse a
// conversation's pending proposals (review/context/progress need
// worker_id); it never routes machine itself (the API server does); and a
// conversation caller keeps the Orchestrator-only rule and its principal.
// Each refusal leaves the worker store unchanged. Boundary:
// Service.ManageWorkers, manageWorkersAction; a temporary store is the
// narrowest layer that proves no worker was written.
func TestManageWorkersServiceCallers(t *testing.T) {
	s, _, db := taskFallbackFixture(t)
	ss := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(ss, nil)
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-a", AccountScopeID: "account-a"}
	if err := ss.CreateSessionForAccount(store.SessionSnapshot{ID: "orch", AccountScopeID: "account-a", UserID: "user-a"}, "user-a", "account-a"); err != nil {
		t.Fatal(err)
	}
	control := tool.WithControlScopes(context.Background(), []string{"automations:read", "automations:write"})
	count := func() int {
		res, err := ss.WorkerStore().ListWorkers("account-a", store.ListWorkersQuery{Limit: 50, IncludeDeleted: true})
		if err != nil {
			t.Fatal(err)
		}
		return len(res.Workers)
	}
	before := count()
	for _, action := range []string{"review", "context", "progress"} {
		if _, err := s.ManageWorkers(control, p, "", "", map[string]any{"action": action}); err == nil || !strings.Contains(err.Error(), "worker_id is required") {
			t.Fatalf("%s without worker_id for a caller with no conversation: %v", action, err)
		}
	}
	if _, err := s.ManageWorkers(control, p, "", "", map[string]any{"action": "list", "machine": "box"}); err == nil || !strings.Contains(err.Error(), "machine") {
		t.Fatalf("machine reached the run service: %v", err)
	}
	if _, err := s.ManageWorkers(control, p, "", "", map[string]any{"action": "activate", "worker_id": "w", "expected_revision": float64(1)}); err == nil || !strings.Contains(err.Error(), "explicit user approval") {
		t.Fatalf("activate for a caller with no conversation: %v", err)
	}
	if _, err := s.ManageWorkers(context.Background(), p, "orch", "fixture-coder", map[string]any{"action": "create", "name": "x", "instructions": "y"}); err == nil || !strings.Contains(err.Error(), "exclusive to Swarm Orchestrator") {
		t.Fatalf("a non-Orchestrator conversation: %v", err)
	}
	foreign := p
	foreign.AccountScopeID = "account-b"
	if _, err := s.ManageWorkers(context.Background(), foreign, "orch", "system-orchestrator", map[string]any{"action": "list"}); err == nil || !strings.Contains(err.Error(), "principal mismatch") {
		t.Fatalf("a foreign principal on the conversation: %v", err)
	}
	if after := count(); after != before {
		t.Fatalf("a refused call wrote a worker: %d -> %d", before, after)
	}
	// A caller with no conversation lists the account's workers.
	if out, err := s.ManageWorkers(control, p, "", "", map[string]any{"action": "list"}); err != nil || !strings.Contains(out, `"workers"`) {
		t.Fatalf("list for a caller with no conversation: %s %v", out, err)
	}
}
