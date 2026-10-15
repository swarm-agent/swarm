package pebblestore

import "testing"

// Purpose: legacy metadata recovery must honor session ownership and account
// project authority, never fall through an existing foreign session to a worker.
// This store-level test is the narrow boundary shared by both receipt producers.
func TestWorkerActivityLegacyProjectAuthority(t *testing.T) {
	s, ws := openTestStore(t)
	sessions := NewSessionStore(s)
	for _, id := range []string{"session-project", "worker-project"} {
		if err := sessions.PutProject("account", &ProjectRecord{ID: id, AccountID: "account", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	worker, err := ws.CreateWorker("account", "user", CreateWorkerRequest{Name: "worker", Metadata: map[string]any{"project_id": "worker-project"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.CreateSessionForAccount(SessionSnapshot{ID: "owned", Metadata: map[string]any{"project_id": "session-project"}}, "user", "account"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.CreateSessionForAccount(SessionSnapshot{ID: "foreign", Metadata: map[string]any{"project_id": "session-project"}}, "other", "account"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ session, account, want string }{{"owned", "account", "session-project"}, {"", "account", "worker-project"}, {"foreign", "account", ""}, {"", "other", ""}} {
		got, err := s.workerActivityProject(WorkerRunRecord{WorkerID: worker.ID, SessionID: tc.session, AccountScopeID: tc.account, UserID: "user", Input: map[string]any{"project_id": "untrusted"}})
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %q %v", tc, got, err)
		}
	}
}
