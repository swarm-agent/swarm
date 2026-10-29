package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// watchToolFixture is the local wait fixture with a project Orchestrator goal
// whose run-1 is running, and the tool runtime wired to the real server (its
// TaskWatchFollower), as the daemon wires it.
type watchToolFixture struct {
	*waitWorkflowFixture
	runtime *tool.Runtime
}

func newWatchToolFixture(t *testing.T, status string) *watchToolFixture {
	t.Helper()
	w := newWaitWorkflowFixture(t)
	db := w.f.s.sessions.Store()
	if err := db.PutProject(w.f.account, &pebblestore.ProjectRecord{ID: "proj-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	task := w.task(time.Now().UnixMilli())
	task.Status, task.Attempts[0].Status = status, status
	task.SourceWorkspace = pebblestore.ProjectTaskSource{}
	if err := db.PutProjectTask(w.f.account, &task); err != nil {
		t.Fatal(err)
	}
	goal := map[string]any{"swarm_v3_project_id": "proj-1", "project_id": "proj-1", "agent_name": "system-orchestrator", "resolved_agent_name": "system-orchestrator"}
	for _, in := range []pebblestore.V3SessionMutationInput{
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-goal", Session: &pebblestore.SessionSnapshot{ID: "goal", Mode: "auto", Metadata: goal}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "pending-goal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentPendingExecutor}},
		{SessionID: "goal", Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "run-goal", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentRunning}},
	} {
		in.AccountScopeID, in.UserID, in.PayloadHash = w.f.account, w.f.user, in.ClientRequestID
		if _, err := db.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	runtime := tool.NewRuntime(1)
	t.Cleanup(func() { _ = runtime.Close() })
	runtime.SetManageSessionService(w.f.s.sessions)
	runtime.SetManageProjectStore(db)
	runtime.SetProjectTaskLifecycleService(w.f.s)
	return &watchToolFixture{waitWorkflowFixture: w, runtime: runtime}
}

// addTask adds another task of proj-1 with its own task session, as the
// fixture's task-1.
func (w *watchToolFixture) addTask(t *testing.T, id, status string) {
	t.Helper()
	sessionID := "task-session-" + id
	snapshot := pebblestore.SessionSnapshot{ID: sessionID, UserID: w.f.user, AccountScopeID: w.f.account, Metadata: map[string]any{"project_id": "proj-1", "task_id": id}}
	if _, err := w.f.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationCreateSession, SessionID: sessionID, UserID: w.f.user, AccountScopeID: w.f.account, Session: &snapshot, ClientRequestID: sessionID, IdempotencyKey: sessionID, PayloadHash: sessionID, NowUnixMs: 1000}); err != nil {
		t.Fatal(err)
	}
	task := w.task(time.Now().UnixMilli())
	task.ID, task.Title, task.SessionID = id, "Task "+id, sessionID
	task.Attempts[0].SessionID, task.Attempts[0].RunID = sessionID, pebblestore.InitialTaskRunID(id)
	task.Status, task.Attempts[0].Status = status, status
	task.SourceWorkspace = pebblestore.ProjectTaskSource{}
	if err := w.f.s.sessions.Store().PutProjectTask(w.f.account, &task); err != nil {
		t.Fatal(err)
	}
}

// call runs manage_projects action on task-1 inside the goal's run-1.
func (w *watchToolFixture) call(t *testing.T, action string) map[string]any {
	t.Helper()
	return w.callTasks(t, action, "task-1")
}

// callTasks runs manage_projects action on the named tasks inside run-1.
func (w *watchToolFixture) callTasks(t *testing.T, action string, ids ...string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: "goal", RunID: "run-1"}), 20*time.Second)
	defer cancel()
	args, _ := json.Marshal(map[string]any{"action": action, "project_id": "proj-1", "task_ids": ids, "milestones": false})
	out, err := w.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: "goal", Principal: w.principal()}, tool.Call{Name: "manage_projects", Arguments: string(args)})
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("%s result %s: %v", action, out, err)
	}
	return result
}

func (w *watchToolFixture) principal() identity.Principal {
	return identity.Principal{Type: identity.PrincipalTypeUser, UserID: w.f.user, AccountScopeID: w.f.account}
}

func (w *watchToolFixture) active(t *testing.T, watchID string) bool {
	t.Helper()
	_, ok, err := w.f.s.sessions.Store().GetV3SessionTaskWatch("goal", watchID)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// delivered says whether watchID's outcome reached run-1 as its wake note.
func (w *watchToolFixture) delivered(t *testing.T, watchID string) bool {
	t.Helper()
	_, ok, err := w.f.s.sessions.Store().GetV3MessageByID("goal", pebblestore.SessionWakeMessageID("goal", watchID+":outcome", "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func (w *watchToolFixture) setStatus(t *testing.T, status string) {
	t.Helper()
	w.setTaskStatus(t, "task-1", status)
}

func (w *watchToolFixture) setTaskStatus(t *testing.T, id, status string) {
	t.Helper()
	if _, err := w.f.s.sessions.Store().UpdateProjectTask(w.f.account, "proj-1", id, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status, task.Attempts[0].Status = status, status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.f.s.reconcileProjectTaskWaits(w.f.account, "proj-1")
}

// Purpose: a watch on tasks that already settled delivers their outcome
// without any later task change.
// Requirement: wait_tasks on a needs_review task (no workflow pending)
// returns watching and the outcome is delivered at once (here as a wake note
// on the registering run-1, which is active) and the watch ends; with no
// further publication. Negative: a watch on an in-progress task is not fired
// by registration (no note, the watch stays) and fires when the task settles.
// Threat: reviewer finding: registration alone never reconciled a local
// watch, so a quick task finished before wait_tasks left the conversation
// "Watching" until an unrelated task in the project changed or a restart.
// Authority: tool registerProjectTaskWatch -> Server.FollowTaskWatch ->
// SessionStore.ReconcileSessionTaskWatches over a real store; the tool entry
// with the real server is the narrowest layer that owns both the
// registration and the check after it.
func TestTaskWatchOnSettledTaskFiresAtRegistration(t *testing.T) {
	t.Run("settled", func(t *testing.T) {
		w := newWatchToolFixture(t, "needs_review")
		result := w.call(t, "wait_tasks")
		id, _ := result["watch_id"].(string)
		if result["status"] != "watching" || id == "" {
			t.Fatalf("wait_tasks: %v", result)
		}
		if !w.delivered(t, id) || w.active(t, id) {
			t.Fatalf("settled task: delivered=%v active=%v", w.delivered(t, id), w.active(t, id))
		}
	})
	t.Run("in_progress", func(t *testing.T) {
		w := newWatchToolFixture(t, "in_progress")
		id, _ := w.call(t, "wait_tasks")["watch_id"].(string)
		if id == "" || w.delivered(t, id) || !w.active(t, id) {
			t.Fatalf("in-progress task fired at registration: delivered=%v active=%v", w.delivered(t, id), w.active(t, id))
		}
		w.setStatus(t, "needs_review")
		if !w.delivered(t, id) || w.active(t, id) {
			t.Fatalf("settling did not fire: delivered=%v active=%v", w.delivered(t, id), w.active(t, id))
		}
	})
}

// Purpose: watching the same tasks again in the same run, after that run's
// watch on them ended, records a new watch that later delivers.
// Requirement: a repeated wait_tasks while the watch is active is idempotent
// (already_watching, same watch_id); after the watch fired as a note into
// the active run-1 (the task was reopened) or after cancel_watch, wait_tasks
// in run-1 returns a new watch_id without already_watching, the watch record
// exists, and its outcome is delivered as its own note, not a replay of the
// earlier watch's.
// Threat: reviewer finding: the registration key (run + task set) replayed
// the ended registration, so the AI was told "watching" while no watch
// existed and the outcome was never delivered; a reused watch identity would
// also replay the first wake instead of delivering a new one.
// Authority: tool registerProjectTaskWatch (generation key),
// pebblestore.ProjectTaskWatchID, ProjectTaskWatchWakeInput and the session
// wake, through the real server and store.
func TestTaskWatchRewatchInTheSameRunIsANewWatch(t *testing.T) {
	w := newWatchToolFixture(t, "in_progress")
	first, _ := w.call(t, "wait_tasks")["watch_id"].(string)
	again := w.call(t, "wait_tasks")
	if again["watch_id"] != first || again["already_watching"] != true {
		t.Fatalf("repeat while active is not idempotent: first %q, again %v", first, again)
	}

	// The watch fires into run-1; the task is then reopened (rework).
	w.setStatus(t, "needs_review")
	if !w.delivered(t, first) || w.active(t, first) {
		t.Fatalf("first watch did not fire: delivered=%v active=%v", w.delivered(t, first), w.active(t, first))
	}
	w.setStatus(t, "in_progress")
	rewatch := w.call(t, "wait_tasks")
	second, _ := rewatch["watch_id"].(string)
	if second == "" || second == first || rewatch["already_watching"] != nil || rewatch["status"] != "watching" {
		t.Fatalf("re-watch after the watch fired: %v (first %q)", rewatch, first)
	}
	if !w.active(t, second) || w.delivered(t, second) {
		t.Fatalf("re-watch recorded no watch: active=%v delivered=%v", w.active(t, second), w.delivered(t, second))
	}
	w.setStatus(t, "needs_review")
	if !w.delivered(t, second) || w.active(t, second) {
		t.Fatalf("re-watch did not deliver: delivered=%v active=%v", w.delivered(t, second), w.active(t, second))
	}

	// Cancelled, then watched again in the same run.
	w.setStatus(t, "in_progress")
	third, _ := w.call(t, "wait_tasks")["watch_id"].(string)
	if third == "" || third == first || third == second || !w.active(t, third) {
		t.Fatalf("third watch: %q", third)
	}
	if got := w.call(t, "cancel_watch"); got["status"] != "cancelled" || w.active(t, third) {
		t.Fatalf("cancel_watch: %v active=%v", got, w.active(t, third))
	}
	after := w.call(t, "wait_tasks")
	fourth, _ := after["watch_id"].(string)
	if fourth == "" || fourth == third || after["already_watching"] != nil || !w.active(t, fourth) {
		t.Fatalf("re-watch after cancel: %v (third %q)", after, third)
	}
	w.setStatus(t, "needs_review")
	if !w.delivered(t, fourth) || w.active(t, fourth) || w.delivered(t, third) {
		t.Fatalf("after cancel: fourth delivered=%v active=%v, cancelled third delivered=%v", w.delivered(t, fourth), w.active(t, fourth), w.delivered(t, third))
	}
}

// watchRecord reads an active watch of the goal conversation.
func (w *watchToolFixture) watchRecord(t *testing.T, watchID string) pebblestore.V3SessionTaskWatch {
	t.Helper()
	watch, ok, err := w.f.s.sessions.Store().GetV3SessionTaskWatch("goal", watchID)
	if err != nil || !ok {
		t.Fatalf("watch %q: ok=%v err=%v", watchID, ok, err)
	}
	return watch
}

// Purpose: a watch whose registered subset equals an ended watch's task set
// in the same run is a new watch that delivers its own outcome.
// Requirement: in run-1, W_A watches task-1 (still active); W_B watches
// task-2, which already settled, so W_B fires at registration and ends;
// task-2 is reopened; wait_tasks {task-1, task-2} registers only task-2
// (task-1 is already watched) under a watch id different from W_B's, with
// its own start line; settling task-2 delivers that new watch's outcome as
// its own note, and W_A stays active.
// Threat: reviewer finding: the store derived the watch id from the
// registered subset while the tool's generation follows the requested
// selection, so this call took W_B's id (and its start message id); its wake
// key matched W_B's committed wake, the idempotent replay republished W_B's
// old outcome and the new watch never delivered ("Watching" forever).
// Authority: tool registerProjectTaskWatch, SessionStore
// prepareProjectTaskWatchRegistration (ProjectTaskWatchID over the requested
// selection, ErrProjectTaskWatchIdentityUsed) and the watch wake, through the
// real tool runtime, server and store: the narrowest layer where the tool's
// key and the store's identity meet.
func TestTaskWatchSubsetOfEndedWatchIsANewWatch(t *testing.T) {
	w := newWatchToolFixture(t, "in_progress")
	w.addTask(t, "task-2", "needs_review")

	watchA, _ := w.callTasks(t, "wait_tasks", "task-1")["watch_id"].(string)
	if watchA == "" || !w.active(t, watchA) {
		t.Fatalf("W_A not watching: %q", watchA)
	}
	watchB, _ := w.callTasks(t, "wait_tasks", "task-2")["watch_id"].(string)
	if watchB == "" || !w.delivered(t, watchB) || w.active(t, watchB) {
		t.Fatalf("W_B on a settled task did not fire at registration: %q", watchB)
	}

	w.setTaskStatus(t, "task-2", "in_progress")
	result := w.callTasks(t, "wait_tasks", "task-1", "task-2")
	watchC, _ := result["watch_id"].(string)
	if result["status"] != "watching" || watchC == "" || watchC == watchB || watchC == watchA {
		t.Fatalf("subset re-watch reused an ended identity: %v (W_A %q, W_B %q)", result, watchA, watchB)
	}
	record := w.watchRecord(t, watchC)
	if len(record.Wait.Tasks) != 1 || record.Wait.Tasks[0].TaskID != "task-2" {
		t.Fatalf("W_C should register only task-2: %+v", record.Wait.Tasks)
	}
	if _, ok, err := w.f.s.sessions.Store().GetV3MessageByID("goal", watchC+"-watch-notice"); err != nil || !ok {
		t.Fatalf("W_C has no start line of its own: ok=%v err=%v", ok, err)
	}
	if w.delivered(t, watchC) {
		t.Fatal("W_C delivered before task-2 settled")
	}

	w.setTaskStatus(t, "task-2", "needs_review")
	if !w.delivered(t, watchC) || w.active(t, watchC) {
		t.Fatalf("W_C did not deliver a new outcome: delivered=%v active=%v", w.delivered(t, watchC), w.active(t, watchC))
	}
	if !w.active(t, watchA) {
		t.Fatal("W_A ended though task-1 is still in progress")
	}
}

// Purpose: a second cancel_watch in the same run ends the re-registered
// watch instead of replaying the first cancellation.
// Requirement: in run-1, wait_tasks [task-1], cancel_watch [task-1],
// wait_tasks [task-1] (a new generation watch), cancel_watch [task-1]: the
// second cancel returns cancelled, the new watch record is gone, and
// settling task-1 afterwards delivers no outcome for either watch.
// Threat: reviewer finding: the cancel idempotency key named only run,
// machine, project, watch id and task ids, so the second cancel replayed the
// first one's stored result without running prepareProjectTaskWatchCancel;
// the tool reported "cancelled" while the new watch kept running.
// Authority: tool cancelProjectTaskWatches (key over the targeted watch ids,
// replay check), pebblestore.ProjectTaskWatchCancelTargets and
// prepareProjectTaskWatchCancel, through the real tool runtime, server and
// store.
func TestTaskWatchSecondCancelInTheSameRunEndsTheNewWatch(t *testing.T) {
	w := newWatchToolFixture(t, "in_progress")
	first, _ := w.call(t, "wait_tasks")["watch_id"].(string)
	if got := w.call(t, "cancel_watch"); got["status"] != "cancelled" || w.active(t, first) {
		t.Fatalf("first cancel: %v active=%v", got, w.active(t, first))
	}
	second, _ := w.call(t, "wait_tasks")["watch_id"].(string)
	if second == "" || second == first || !w.active(t, second) {
		t.Fatalf("re-watch after cancel: %q (first %q)", second, first)
	}
	if got := w.call(t, "cancel_watch"); got["status"] != "cancelled" {
		t.Fatalf("second cancel: %v", got)
	}
	if w.active(t, second) {
		t.Fatal("second cancel reported cancelled but the new watch is still active")
	}
	w.setStatus(t, "needs_review")
	if w.delivered(t, first) || w.delivered(t, second) {
		t.Fatalf("a cancelled watch delivered: first=%v second=%v", w.delivered(t, first), w.delivered(t, second))
	}
}
