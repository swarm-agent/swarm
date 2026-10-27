package run

import (
	"context"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

type recordingWorkflowLifecycle struct {
	calls     int
	principal identity.Principal
	session   string
	args      map[string]any
}

func (r *recordingWorkflowLifecycle) ManageWorkflows(_ context.Context, p identity.Principal, session string, args map[string]any) (any, error) {
	r.calls++
	r.args = args
	r.principal = p
	r.session = session
	return map[string]any{"action": args["action"]}, nil
}

// Purpose: native execution must use the shared lifecycle with the authenticated
// session identity, never argument-supplied ownership, and refuse non-Orchestrator
// profiles before calling it. Trigger arguments must reach that service unchanged;
// unsupported rotation must fail without a proposal instead of silently succeeding.
// A recording lifecycle is the narrowest boundary test.
func TestManageWorkflowsLifecycle(t *testing.T) {
	s, _, db := taskFallbackFixture(t)
	sessions := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(sessions, nil)
	if err := sessions.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account-a", UserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	lifecycle := &recordingWorkflowLifecycle{}
	s.SetWorkflowLifecycleService(lifecycle)
	if _, err := s.executeManageWorkflowsTool("author", `{"action":"propose","account":"foreign","session_id":"foreign","on":{"event":{"source":"webhook","id":"id"},"start":{"worker":"worker-id","brief":"Handle event"}}}`, store.AgentProfile{Name: "orchestrator"}); err != nil {
		t.Fatal(err)
	}
	if lifecycle.calls != 1 || lifecycle.principal.AccountScopeID != "account-a" || lifecycle.principal.UserID != "owner" || lifecycle.session != "author" {
		t.Fatal("incorrect lifecycle identity", lifecycle)
	}
	if _, err := s.executeManageWorkflowsTool("author", `{"action":"propose"}`, store.AgentProfile{Name: "coder"}); err == nil {
		t.Fatal("coder allowed")
	}
	if lifecycle.calls != 1 {
		t.Fatal("unauthorized lifecycle call")
	}
	if lifecycle.args["on"].(map[string]any)["start"].(map[string]any)["worker"] != "worker-id" {
		t.Fatal("trigger lost")
	}
	if _, err := s.executeManageWorkflowsTool("author", `{"action":"propose","rotate_key":true}`, store.AgentProfile{Name: "orchestrator"}); err == nil {
		t.Fatal("rotation silently ignored")
	}
	if lifecycle.calls != 1 {
		t.Fatal("unsupported rotation mutated lifecycle")
	}
}
