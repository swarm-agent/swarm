package pebblestore

import (
	"path/filepath"
	"testing"
)

// Requirement: a direct worker job starts with one user message whose run
// intent is the job's own run ID, while its project task is still queued (the
// task is marked in_progress only after the message is accepted). Threat: the
// continuation guard took that first message for a retry of an unadmitted
// task and refused every direct job ("task continuation requires an admitted
// task"), while a queued task must still refuse a message for any other run.
// Authority: ApplyV3SessionMutation/prepareTaskExecutionContinuation. A
// temporary real store is the narrowest layer that runs the guard.
func TestTaskExecutionContinuationAdmitsWorkerJobStart(t *testing.T) {
	for _, scenario := range []string{"own_run", "other_run"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "store"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			db := NewSessionStore(store)
			if _, err := db.ApplyV3SessionMutation(V3SessionMutationInput{SessionID: "worker-execution-wrun_1", AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationCreateSession, ClientRequestID: "create", PayloadHash: "create", Session: &SessionSnapshot{ID: "worker-execution-wrun_1", Mode: "action", Metadata: map[string]any{"project_id": "project", "task_id": "task_wrun_1"}}}); err != nil {
				t.Fatal(err)
			}
			// The admitted worker run the task belongs to, as dispatch stores it.
			if err := store.PutJSON(KeyWorkerRun("account", "worker_1", "wrun_1"), WorkerRunRecord{ID: "wrun_1", AccountScopeID: "account", UserID: "user", WorkerID: "worker_1", SessionID: "worker-execution-wrun_1", RequestSource: "direct", Status: "admitted"}); err != nil {
				t.Fatal(err)
			}
			task := &ProjectTaskRecord{ID: "task_wrun_1", Title: "Worker job", ProjectID: "project", AccountID: "account", SessionID: "worker-execution-wrun_1", Agent: "coder", Status: "queued", WorkerID: "worker_1", WorkerRunID: "wrun_1"}
			if err := db.PutProjectTask("account", task); err != nil {
				t.Fatal(err)
			}
			run := "wrun_1"
			if scenario == "other_run" {
				run = "wrun_other"
			}
			in := V3SessionMutationInput{SessionID: "worker-execution-wrun_1", AccountScopeID: "account", UserID: "user", Kind: V3SessionMutationAppendMessage, ClientRequestID: "worker-message:" + run, PayloadHash: "worker-message:" + run, Message: &MessageSnapshot{ID: "job-message", Role: "user", Content: "the job"}, RunIntent: &V3SessionRunIntent{RunID: run, Status: V3RunIntentPendingExecutor}}
			_, err = db.ApplyV3SessionMutation(in)
			after, _, _ := db.GetProjectTask("account", "project", "task_wrun_1")
			_, intentFound, _ := db.GetV3SessionRunIntent("worker-execution-wrun_1", run)
			if scenario == "other_run" {
				if err == nil || intentFound {
					t.Fatalf("a queued task admitted a message for another run: err=%v intent=%v", err, intentFound)
				}
				return
			}
			if err != nil {
				t.Fatalf("the worker job's own first message was refused: %v", err)
			}
			if !intentFound {
				t.Fatal("the worker job's run intent was not recorded")
			}
			if after.Status != "queued" || after.Revision != task.Revision {
				t.Fatalf("the job start rewrote its task: %+v", after)
			}
		})
	}
}
