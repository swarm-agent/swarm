package tool_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: when the Orchestrator pends its conversation on wait_tasks, the
// person must be able to see why, even if the Orchestrator wrote nothing
// first. Requirement: a local wait_tasks result carries a one-line notice
// naming every waited task by title, and the
// committed session.run.waiting_tasks event carries status waiting_tasks and
// the same notice (top-level and in run_intent.task_wait) for clients to show.
// A refused wait records no intent and emits no waiting event.
// Threat: the live failure: the conversation silently stops on a tool call
// and the person is left asking "why are we waiting?".
// Owners: executeManageProjectTasksContext (wait_tasks), checkProjectTaskWaitIDs,
// SessionStore.prepareProjectTaskWait (ProjectTaskWaitNotice) and the V3 event
// payload (v3EventPayload). The tool entry over a real Pebble store is the
// narrowest layer that returns the agent-visible result and commits the
// durable event together.
func TestWaitTasksNoticeNamesTasksInResultAndEvent(t *testing.T) {
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
	wait := func(ids ...string) (string, error) {
		raw, _ := json.Marshal(map[string]any{"action": "wait_tasks", "project_id": "proj-folders", "task_ids": ids})
		return f.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, f.scope, tool.Call{Name: "manage_projects", Arguments: string(raw)})
	}
	waitingEvents := func() []pebblestore.V3SessionEvent {
		events, err := f.store.ListV3SessionEvents(chat, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		var out []pebblestore.V3SessionEvent
		for _, event := range events {
			if event.EventType == "session.run.waiting_tasks" {
				out = append(out, event)
			}
		}
		return out
	}

	// Negative: a refused wait (one task not started) records nothing.
	if out, err := wait("t1", "t3"); err == nil {
		t.Fatalf("wait on an unstarted task accepted: %s", out)
	}
	if got := waitingEvents(); len(got) != 0 {
		t.Fatalf("refused wait emitted waiting events: %+v", got)
	}
	if intent, _, err := f.store.GetV3SessionRunIntent(chat, "run-1"); err != nil || intent.Status != pebblestore.V3RunIntentRunning || intent.TaskWait != nil {
		t.Fatalf("refused wait changed the run: %+v %v", intent, err)
	}

	out, err := wait("t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	const want = "This conversation is now waiting on 2 task(s): Fix login, Research cache. It wakes when they settle; a new message ends the wait."
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "waiting_tasks" || result["next_action"] != "yield_until_task_outcome" || result["notice"] != want {
		t.Fatalf("wait_tasks result: %s", out)
	}
	intent, ok, err := f.store.GetV3SessionRunIntent(chat, "run-1")
	if err != nil || !ok || intent.Status != pebblestore.V3RunIntentWaitingTasks || intent.TaskWait == nil || intent.TaskWait.Notice != want {
		t.Fatalf("durable wait: %+v %v %v", intent, ok, err)
	}
	events := waitingEvents()
	if len(events) != 1 {
		t.Fatalf("waiting events: %+v", events)
	}
	var payload struct {
		Status    string `json:"status"`
		Notice    string `json:"notice"`
		RunIntent struct {
			Status   string `json:"status"`
			TaskWait struct {
				Notice string `json:"notice"`
			} `json:"task_wait"`
		} `json:"run_intent"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "waiting_tasks" || payload.Notice != want || payload.RunIntent.Status != "waiting_tasks" || payload.RunIntent.TaskWait.Notice != want {
		t.Fatalf("waiting event payload: %s", events[0].Payload)
	}
}
