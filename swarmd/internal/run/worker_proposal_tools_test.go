package run

import (
	"encoding/json"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: executeManageWorkersTool must reuse only this session's pending
// proposal, never accepted/discarded workers. Real session and worker stores
// exercise tool results and durable revisions without providers or API routes.
func TestManageWorkersPendingProposalReuse(t *testing.T) {
	s, settings, db := taskFallbackFixture(t)
	taskFallbackSettings(t, settings, "account-a", "specialist", "account-a-action")
	sessions := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(sessions, nil)
	for _, id := range []string{"author", "other"} {
		if err := sessions.CreateSession(store.SessionSnapshot{ID: id, AccountScopeID: "account-a", UserID: "owner", Metadata: map[string]any{"agent_name": "orchestrator"}, WorkspaceGrants: []store.WorkspaceGrant{{WorkspaceID: "folder", Kind: store.WorkspaceGrantPrimary, Available: store.BoolPtr(true)}}}); err != nil {
			t.Fatal(err)
		}
	}
	propose := func(sessionID, action string) (store.WorkerRecord, string) {
		t.Helper()
		output, err := s.executeManageWorkersTool(sessionID, `{"action":"`+action+`","name":"Weekday work","instructions":"Check the project","workspace_id":"folder"}`)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Worker store.WorkerRecord `json:"worker"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		return result.Worker, output
	}
	first, _ := propose("author", "propose")
	second, output := propose("author", "propose")
	if first.ID != second.ID || second.Revision <= first.Revision || !strings.Contains(output, "waiting for the person to accept or deny") {
		t.Fatalf("proposal not reused: %s", output)
	}
	pending, err := s.pendingWorkersForSession("account-a", "author")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	created, _ := propose("author", "create")
	if created.ID != second.ID || created.Revision <= second.Revision {
		t.Fatal("create did not reuse pending proposal")
	}
	second = created
	other, _ := propose("other", "propose")
	if other.ID == first.ID {
		t.Fatal("another session overwrote proposal")
	}
	accepted, err := s.sessions.AcceptWorker("account-a", "owner", second.ID, second.Revision, map[string]string{"primary": "folder"})
	if err != nil {
		t.Fatal(err)
	}
	third, _ := propose("author", "propose")
	if third.ID == second.ID || third.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatal("accepted worker reused")
	}
	saved, _, err := s.sessions.GetWorker("account-a", accepted.ID)
	if err != nil || saved.Revision != accepted.Revision || saved.LifecycleState != accepted.LifecycleState {
		t.Fatal("accepted worker changed")
	}
	if err := s.sessions.DeleteWorker("account-a", "owner", third.ID, third.Revision); err != nil {
		t.Fatal(err)
	}
	fourth, _ := propose("author", "create")
	if fourth.ID == third.ID {
		t.Fatal("discarded worker reused")
	}
}

// Purpose: review/progress must read schedules, folder bindings and runs from
// WorkerStore, not Automation V2 proposals. The tool boundary with a real store
// proves session lookup and explicit ID lookup, plus cross-account rejection.
func TestManageWorkersReviewProgressWorkerRecord(t *testing.T) {
	s, _, db := taskFallbackFixture(t)
	sessions := store.NewSessionStore(db)
	s.sessions = sessionruntime.NewService(sessions, nil)
	if err := sessions.CreateSession(store.SessionSnapshot{ID: "author", AccountScopeID: "account-a", UserID: "owner", Metadata: map[string]any{"agent_name": "orchestrator"}}); err != nil {
		t.Fatal(err)
	}
	w, err := sessions.WorkerStore().CreateWorker("account-a", "owner", store.CreateWorkerRequest{Name: "Scheduled work", Instructions: "Check project", InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": "folder"}, Metadata: map[string]any{"source_session_id": "author"}, Automations: []store.WorkerAutomationDefinition{{Name: "Weekdays", ActivationMode: "cron", Schedule: &store.AutomationV2Schedule{Kind: "cron", Cron: "0 9 * * 1", Timezone: "UTC"}, Enabled: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"review", "progress"} {
		for _, explicit := range []bool{false, true} {
			args := map[string]any{"action": action}
			if explicit {
				args["worker_id"] = w.ID
			}
			raw, _ := json.Marshal(args)
			output, err := s.executeManageWorkersTool("author", string(raw))
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Found  bool               `json:"found"`
				Worker store.WorkerRecord `json:"worker"`
				Runs   json.RawMessage    `json:"runs"`
			}
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if !result.Found || result.Worker.ID != w.ID || result.Worker.ProposedBindings["primary"] != "folder" || len(result.Worker.Automations) != 1 || result.Worker.Automations[0].Schedule.Cron != "0 9 * * 1" || result.Runs == nil {
				t.Fatalf("missing worker details: %s", output)
			}
		}
	}
	if err := sessions.CreateSession(store.SessionSnapshot{ID: "outsider", AccountScopeID: "account-b", UserID: "other", Metadata: map[string]any{"agent_name": "orchestrator"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.executeManageWorkersTool("outsider", `{"action":"review","worker_id":"`+w.ID+`"}`); err == nil {
		t.Fatal("cross-account worker exposed")
	}
}
