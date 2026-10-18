package api

import (
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a retried task reports the new run's outcome, even if no reader
// observes running. Threat: stale errors or late callbacks overwrite a retry.
// Owners: ApplySessionMutation and reconcileProjectTaskRunLifecycle. Real store
// mutations plus the terminal bridge are the narrowest deterministic boundary;
// the live-provider test separately proves transport behavior.
func TestProjectTaskRetryStatusUsesCurrentRun(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "retry"), nil)
	defer f.close()
	p := testPrincipal()
	sid := f.createSession("retry")
	sess, _, _ := f.sessions.GetSession(sid)
	sess.Metadata["project_id"] = "retry-project"
	sess.Metadata["task_id"] = "retry-task"
	apply := func(in sessionruntime.SessionMutationInput) {
		t.Helper()
		in.SessionID = sid
		in.AccountScopeID = p.AccountScopeID
		in.UserID = p.UserID
		in.PayloadHash = in.ClientRequestID
		_, err := f.sessions.ApplySessionMutation(in)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationUpdateMetadata, ClientRequestID: "bind", Session: &sess})
	db := f.sessions.Store()
	task := &pebblestore.ProjectTaskRecord{ID: "retry-task", Title: "Retry", ProjectID: "retry-project", AccountID: p.AccountScopeID, SessionID: sid, Agent: "swarm", Status: "in_progress"}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for i, outcome := range []string{sessionruntime.RunIntentFailed, sessionruntime.RunIntentFailed, sessionruntime.RunIntentCancelled, sessionruntime.RunIntentCompleted} {
		id := []string{"initial", "retry-fail", "retry-stop", "retry-success"}[i]
		reason := "failure-" + id
		input := sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationAppendMessage, ClientRequestID: id, Message: &pebblestore.MessageSnapshot{ID: "msg-" + id, Role: "user", Content: "continue"}, RunIntent: &pebblestore.V3SessionRunIntent{RunID: id, Status: sessionruntime.RunIntentPendingExecutor}}
		if i == 0 {
			input.Kind = sessionruntime.SessionMutationRecordRunIntent
			input.Message = nil
		}
		apply(input)
		if previous != "" {
			if err := f.server.reconcileProjectTaskRunLifecycle(sessionV3ExecutorJob{Principal: p, SessionID: sid, RunID: previous}, sessionruntime.RunIntentFailed, "stale failure"); err != nil {
				t.Fatal(err)
			}
			current, _, _ := db.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
			if current.Status != "in_progress" || current.LastError != "" {
				t.Fatalf("retry remains stale: %+v", current)
			}
		}
		apply(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, ClientRequestID: id + "-terminal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: id, Status: outcome, BlockedReason: reason}})
		if err := f.server.reconcileProjectTaskRunLifecycle(sessionV3ExecutorJob{Principal: p, SessionID: sid, RunID: id}, outcome, reason); err != nil {
			t.Fatal(err)
		}
		current, _, _ := db.GetProjectTask(p.AccountScopeID, task.ProjectID, task.ID)
		if outcome == sessionruntime.RunIntentCompleted {
			if current.Status != "needs_review" || current.LastError != "" {
				t.Fatalf("success: %+v", current)
			}
		} else if current.Status != "failed" || current.LastError != reason {
			t.Fatalf("new failure not projected: %+v", current)
		}
		previous = id
	}
}
