package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Requirement: same-session retries update the project receipt atomically with
// the admitted run. Threat: stale failure makes clients settle before new work,
// or a rejected/duplicate request partially rewrites task state. Authority:
// ApplyV3SessionMutation/prepareTaskExecutionContinuation and the Pebble batch.
// A temporary real store is the narrowest layer proving rollback and restart.
func TestTaskExecutionContinuationAtomic(t *testing.T) {
	for _, scenario := range []string{"success", "commit_failure", "cross_account", "cross_user", "archived", "superseded", "pending_approval", "integrated"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store")
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			db := NewSessionStore(store)
			apply := func(in V3SessionMutationInput) V3SessionMutationResult {
				t.Helper()
				in.SessionID = "task-session"
				in.AccountScopeID = "account"
				in.UserID = "user"
				in.PayloadHash = in.ClientRequestID
				r, e := db.ApplyV3SessionMutation(in)
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			apply(V3SessionMutationInput{Kind: V3SessionMutationCreateSession, ClientRequestID: "create", Session: &SessionSnapshot{ID: "task-session", Mode: "action", Metadata: map[string]any{"project_id": "project", "task_id": "task"}}})
			initial := "desktop-v3-run:task-task"
			for _, status := range []string{V3RunIntentPendingExecutor, V3RunIntentFailed} {
				apply(V3SessionMutationInput{Kind: V3SessionMutationRecordRunIntent, ClientRequestID: status, RunIntent: &V3SessionRunIntent{RunID: initial, Status: status, BlockedReason: "old failure"}})
			}
			task := &ProjectTaskRecord{ID: "task", Title: "Retry fixture", ProjectID: "project", AccountID: "account", SessionID: "task-session", Agent: "swarm", Status: "failed", LastError: "old failure", ActionNeeded: "old action"}
			switch scenario {
			case "archived":
				task.Archived = true
			case "superseded":
				task.SessionID = "new-session"
			case "pending_approval":
				task.Status = "pending_approval"
			case "integrated":
				task.IsIntegrated = true
			}
			if err := db.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			before, _, _ := db.GetProjectTask("account", "project", "task")
			in := V3SessionMutationInput{SessionID: "task-session", AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationAppendMessage, ClientRequestID: "retry", PayloadHash: "retry", Message: &MessageSnapshot{ID: "retry-message", Role: "user", Content: "try again"}, RunIntent: &V3SessionRunIntent{RunID: "retry-run", Status: V3RunIntentPendingExecutor}}
			if scenario == "cross_account" {
				in.AccountScopeID = "other"
			}
			if scenario == "cross_user" {
				in.UserID = "other"
			}
			if scenario == "commit_failure" {
				store.sessionMutations.beforeDurableCommit = func(string) { panic("injected commit failure") }
			}
			var result V3SessionMutationResult
			func() {
				defer func() {
					if v := recover(); v != nil {
						if v != "injected commit failure" {
							panic(v)
						}
						err = errors.New("injected commit failure")
					}
				}()
				result, err = db.ApplyV3SessionMutation(in)
			}()
			store.sessionMutations.beforeDurableCommit = nil
			after, _, _ := db.GetProjectTask("account", "project", "task")
			if scenario != "success" {
				if err == nil {
					t.Fatal("invalid continuation admitted")
				}
				if after.Status != before.Status || after.LastError != before.LastError || after.Revision != before.Revision {
					t.Fatalf("task changed after rejection: %+v", after)
				}
				if _, found, _ := db.GetV3SessionRunIntent("task-session", "retry-run"); found {
					t.Fatal("rejected run persisted")
				}
				messages, _ := db.ListMessages("task-session", 0, 100)
				if len(messages) != 0 {
					t.Fatal("rejected message persisted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != "in_progress" || after.LastError != "" || after.ActionNeeded != "" || after.ExecutionRunID() != "retry-run" {
				t.Fatalf("stale task: %+v", after)
			}
			replay, err := db.ApplyV3SessionMutation(in)
			if err != nil || !replay.Replayed || replay.PrimarySeq != result.PrimarySeq {
				t.Fatalf("duplicate: %+v %v", replay, err)
			}
			again, _, _ := db.GetProjectTask("account", "project", "task")
			if again.Revision != after.Revision {
				t.Fatal("duplicate rewrote task")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			db = NewSessionStore(store)
			persisted, _, _ := db.GetProjectTask("account", "project", "task")
			intent, found, _ := db.GetV3SessionRunIntent("task-session", "retry-run")
			if persisted.Status != "in_progress" || persisted.LastError != "" || !found || intent.Status != V3RunIntentPendingExecutor {
				t.Fatal("retry did not survive restart")
			}
		})
	}
}
