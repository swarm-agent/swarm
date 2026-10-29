package tool_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: wait_tasks watches the tasks and returns at once, so the
// Orchestrator goes on and answers the person in the same turn.
// Requirement: a local wait_tasks result has status watching, a watch_id and
// a one-line notice naming every watched task and telling the AI to reply and
// end its turn; the run intent is untouched (still running, no task wait, no
// waiting_tasks event); a session.task_watch.started event carries the
// notice and the conversation's task_watches. Calling it again on watched
// tasks is idempotent (already_watching, no second event); the watch_tasks
// alias works; cancel_watch ends the watch. A refused call records nothing.
// Threat: the live failure of the pending model: a silent turn ended on the
// tool call, re-waits pending the conversation again, duplicate wakes.
// Owners: executeManageProjectTasksContext (wait_tasks), registerProjectTaskWatch,
// cancelProjectTaskWatches, normalizeProjectTaskWatchArgs and
// SessionStore.prepareProjectTaskWait over a real Pebble store: the tool entry
// is the narrowest layer that returns the agent-visible result and commits
// the durable event together.
func TestWaitTasksWatchesAndReturnsAtOnce(t *testing.T) {
	f := newProjectFolderFixture(t)
	account, user, chat := f.scope.Principal.AccountScopeID, f.scope.Principal.UserID, f.scope.SessionID
	for _, in := range []pebblestore.V3SessionMutationInput{
		{SessionID: chat, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "pending-chat", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentPendingExecutor}},
		{SessionID: chat, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "run-chat", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentRunning}},
	} {
		in.AccountScopeID, in.UserID, in.PayloadHash = account, user, in.ClientRequestID
		if _, err := f.store.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []pebblestore.ProjectTaskRecord{
		{ID: "t1", Title: "Fix login", Agent: "coder", Status: "in_progress"},
		{ID: "t2", Title: "Research cache", Agent: "finder", Status: "in_progress"},
		{ID: "t3", Title: "Not started", Agent: "coder", Status: "pending_approval"},
	} {
		if task.Status != "pending_approval" {
			child := "child-" + task.ID
			if err := f.store.CreateSession(pebblestore.SessionSnapshot{ID: child, UserID: user, AccountScopeID: account, Metadata: map[string]any{"project_id": "proj-folders", "task_id": task.ID}}); err != nil {
				t.Fatal(err)
			}
			task.SessionID, task.ActiveAttemptID = child, "a-"+task.ID
			task.Attempts = []pebblestore.ProjectTaskAttempt{{ID: "a-" + task.ID, SessionID: child, Role: task.Agent, Status: "in_progress"}}
		}
		task.ProjectID, task.AccountID = "proj-folders", account
		if err := f.store.PutProjectTask(account, &task); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: chat, RunID: "run-1"}), 20*time.Second)
	defer cancel()
	call := func(action string, extra map[string]any, ids ...string) (map[string]any, error) {
		args := map[string]any{"action": action, "project_id": "proj-folders"}
		if len(ids) > 0 {
			args["task_ids"] = ids
		}
		for k, v := range extra {
			args[k] = v
		}
		raw, _ := json.Marshal(args)
		out, err := f.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, f.scope, tool.Call{Name: "manage_projects", Arguments: string(raw)})
		if err != nil {
			return nil, err
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("result %s: %v", out, err)
		}
		return result, nil
	}
	events := func(eventType string) []pebblestore.V3SessionEvent {
		all, err := f.store.ListV3SessionEvents(chat, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		var out []pebblestore.V3SessionEvent
		for _, event := range all {
			if event.EventType == eventType {
				out = append(out, event)
			}
		}
		return out
	}
	runUnchanged := func(label string) {
		t.Helper()
		intent, _, err := f.store.GetV3SessionRunIntent(chat, "run-1")
		if err != nil || intent.Status != pebblestore.V3RunIntentRunning || intent.TaskWait != nil {
			t.Fatalf("%s changed the run: %+v %v", label, intent, err)
		}
		if got := events("session.run.waiting_tasks"); len(got) != 0 {
			t.Fatalf("%s emitted waiting events: %+v", label, got)
		}
	}

	// Negative: a refused call (one task not started) records nothing.
	if out, err := call("wait_tasks", nil, "t1", "t3"); err == nil {
		t.Fatalf("watch on an unstarted task accepted: %v", out)
	}
	if got := events("session.task_watch.started"); len(got) != 0 {
		t.Fatalf("refused call emitted watch events: %+v", got)
	}
	runUnchanged("refused call")

	result, err := call("wait_tasks", nil, "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	const want = "Watching 2 task(s): Fix login, Research cache. Reply to the person now and end your turn; a new turn starts with the outcomes when they settle."
	if result["status"] != "watching" || result["notice"] != want || result["watch_id"] == "" || result["next_action"] != nil {
		t.Fatalf("wait_tasks result: %v", result)
	}
	runUnchanged("wait_tasks")
	started := events("session.task_watch.started")
	if len(started) != 1 {
		t.Fatalf("watch events: %+v", started)
	}
	var payload struct {
		Notice      string `json:"notice"`
		TaskWatches []struct {
			WatchID string   `json:"watch_id"`
			TaskIDs []string `json:"task_ids"`
			Titles  []string `json:"titles"`
		} `json:"task_watches"`
	}
	if err := json.Unmarshal(started[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Notice != "Watching 2 tasks: Fix login, Research cache." || len(payload.TaskWatches) != 1 || payload.TaskWatches[0].WatchID != result["watch_id"] || len(payload.TaskWatches[0].TaskIDs) != 2 {
		t.Fatalf("watch event payload: %s", started[0].Payload)
	}

	// Watching again (also through the watch_tasks alias) changes nothing.
	again, err := call("watch_tasks", nil, "t2", "t1")
	if err != nil || again["status"] != "watching" || again["already_watching"] != true {
		t.Fatalf("re-watch: %v %v", again, err)
	}
	if got := events("session.task_watch.started"); len(got) != 1 {
		t.Fatalf("re-watch emitted a second event: %d", len(got))
	}
	if watches, err := f.store.ListV3SessionTaskWatches(chat); err != nil || len(watches) != 1 {
		t.Fatalf("watches after re-watch: %+v %v", watches, err)
	}

	// cancel_watch ends it; a second cancel finds nothing and says so.
	cancelled, err := call("cancel_watch", nil, "t1", "t2")
	if err != nil || cancelled["status"] != "cancelled" {
		t.Fatalf("cancel_watch: %v %v", cancelled, err)
	}
	if watches, err := f.store.ListV3SessionTaskWatches(chat); err != nil || len(watches) != 0 {
		t.Fatalf("watches after cancel: %+v %v", watches, err)
	}
	if _, err := call("wait_tasks", map[string]any{"cancel": true}, "t1"); err == nil {
		t.Fatal("cancel of nothing reported success")
	}
	runUnchanged("cancel")
}
