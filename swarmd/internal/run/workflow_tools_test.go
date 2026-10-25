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
}

func (r *recordingWorkflowLifecycle) ManageWorkflows(_ context.Context, p identity.Principal, session string, args map[string]any) (any, error) {
	r.calls++
	r.principal = p
	r.session = session
	return map[string]any{"action": args["action"]}, nil
}

// Purpose: native execution must use the shared lifecycle with the authenticated
// session identity, never argument-supplied ownership, and refuse non-Orchestrator
// profiles before calling it. A recording lifecycle is the narrowest boundary test.
func TestManageWorkflowsLifecycle(t *testing.T) {
	s, _, db := taskFallbackFixture(t)
	sessions := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(sessions, nil)
	if err := sessions.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account-a", UserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	lifecycle := &recordingWorkflowLifecycle{}
	s.SetWorkflowLifecycleService(lifecycle)
	if _, err := s.executeManageWorkflowsTool("author", `{"action":"propose","account":"foreign","session_id":"foreign"}`, store.AgentProfile{Name: "orchestrator"}); err != nil {
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
}
