package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// remoteWaitHarness is two in-process daemons: home (the Orchestrator's
// Swarm) linked to remote as machine "box", a running home goal, and tasks
// with delegated attempts on remote's project. No provider runs: the home's
// executor is detached, so a wake leaves its continuation pending_executor.
type remoteWaitHarness struct {
	home, remote *fleetTestSwarm
	p            identity.Principal
	runtime      *tool.Runtime
}

const remoteWaitGoal = "home-goal"

func newRemoteWaitHarness(t *testing.T, tasks ...string) *remoteWaitHarness {
	t.Helper()
	home := newFleetTestSwarm(t, "home")
	remote := newFleetTestSwarm(t, "remote")
	t.Cleanup(remote.cleanup)
	t.Cleanup(home.cleanup)
	// Runs first (cleanups are LIFO): stop the home like daemon shutdown does,
	// so watchers end before its store closes.
	t.Cleanup(func() {
		home.s.CancelInFlightRuns()
		if !home.s.WaitForInFlightRuns(10 * time.Second) {
			t.Error("linked wait watchers did not stop on shutdown")
		}
	})
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if code, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: %d %v", code, out)
	}
	home.s.v3SessionExecutor = nil
	home.s.remoteTaskWaitGrace = 400 * time.Millisecond
	home.s.remoteTaskWaitRules = 50 * time.Millisecond
	fleetActivityTestSetWork(t, home, "box", pebblestore.RuleAuto)
	if err := remote.s.sessions.Store().PutProject(remote.account, &pebblestore.ProjectRecord{ID: "remote-project", Name: "Remote project"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range tasks {
		session := "rs-" + id
		fleetActivityTestSession(t, remote, session)
		task := &pebblestore.ProjectTaskRecord{ID: id, ProjectID: "remote-project", Title: "Remote " + id, Agent: "coder", Status: "running", SessionID: session, ActiveAttemptID: "attempt-" + id, Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-" + id, SessionID: session, Role: "coder", Status: "running"}}}
		if err := remote.s.sessions.Store().PutProjectTask(remote.account, task); err != nil {
			t.Fatal(err)
		}
	}
	h := &remoteWaitHarness{home: home, remote: remote, p: identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}}
	h.createGoal(t, remoteWaitGoal, map[string]any{})
	h.runtime = tool.NewRuntime(1)
	t.Cleanup(func() { _ = h.runtime.Close() })
	h.runtime.SetManageSessionService(home.s.sessions)
	h.runtime.SetProjectTaskLifecycleService(home.s)
	return h
}

func (h *remoteWaitHarness) createGoal(t *testing.T, id string, metadata map[string]any) {
	t.Helper()
	store := h.home.s.sessions.Store()
	for _, in := range []pebblestore.V3SessionMutationInput{
		{SessionID: id, Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-" + id, Session: &pebblestore.SessionSnapshot{ID: id, Mode: "auto", Metadata: metadata}},
		{SessionID: id, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "pending-" + id, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentPendingExecutor}},
		{SessionID: id, Kind: pebblestore.V3SessionMutationRecordRunIntent, ClientRequestID: "run-" + id, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "run-1", Status: pebblestore.V3RunIntentRunning}},
	} {
		in.AccountScopeID, in.UserID, in.PayloadHash = h.home.account, h.home.user, in.ClientRequestID
		if _, err := store.ApplyV3SessionMutation(in); err != nil {
			t.Fatal(err)
		}
	}
}

// wait calls manage_projects wait_tasks with machine=box as the running goal.
func (h *remoteWaitHarness) wait(t *testing.T, session string, ids ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: session, RunID: "run-1"}), 20*time.Second)
	defer cancel()
	args, _ := json.Marshal(map[string]any{"action": "wait_tasks", "machine": "box", "project_id": "remote-project", "task_ids": ids})
	return h.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: session, Principal: h.p}, tool.Call{Name: "manage_projects", Arguments: string(args)})
}

func (h *remoteWaitHarness) intent(t *testing.T, run string) (pebblestore.V3SessionRunIntent, bool) {
	t.Helper()
	intent, ok, err := h.home.s.sessions.Store().GetV3SessionRunIntent(remoteWaitGoal, run)
	if err != nil {
		t.Fatal(err)
	}
	return intent, ok
}

// finish moves a remote task to status with an attempt summary, then commits
// one event on its attempt session so the remote activity stream reports it.
func (h *remoteWaitHarness) finish(t *testing.T, id, status, summary, action string) {
	t.Helper()
	if _, err := h.remote.s.sessions.Store().UpdateProjectTask(h.remote.account, "remote-project", id, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status, task.ActionNeeded = status, action
		if action != "" {
			// As projectTaskDeclaredBlocker records a declared blocker: the
			// reason is also the task's last error, which task reads keep (they
			// recompute action_needed from Git state when last_error is empty).
			task.LastError, task.ActionNeeded = action, "Action Needed: Supply required input and resume. "+action
		}
		task.Attempts[0].Summary = summary
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.touch(t, "rs-"+id, status)
}

func (h *remoteWaitHarness) touch(t *testing.T, session, key string) {
	t.Helper()
	request := session + "-" + key
	if _, err := h.remote.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: session, UserID: h.remote.user, AccountScopeID: h.remote.account, ClientRequestID: "pending-" + request, IdempotencyKey: "pending-" + request, PayloadHash: "pending-" + request, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-" + key, Status: pebblestore.V3RunIntentPendingExecutor}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.remote.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, EventType: "session.run.completed", SessionID: session, UserID: h.remote.user, AccountScopeID: h.remote.account, ClientRequestID: request, IdempotencyKey: request, PayloadHash: request, RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-" + key, Status: pebblestore.V3RunIntentCompleted}}); err != nil {
		t.Fatal(err)
	}
}

// awaitWake waits (bounded) for the goal's deterministic continuation and
// returns the outcome JSON carried by its system message.
func (h *remoteWaitHarness) awaitWake(t *testing.T) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if next, ok := h.intent(t, pebblestore.ProjectTaskWaitResumeID("run-1")); ok {
			if next.Status != pebblestore.V3RunIntentPendingExecutor || next.TaskWaitOwnerRunID != "run-1" {
				t.Fatalf("continuation: %+v", next)
			}
			messages, err := h.home.s.sessions.Store().ListMessages(remoteWaitGoal, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range messages {
				if m.Role == "system" && strings.HasPrefix(m.Content, "Delegated project task outcomes:\n") {
					var out map[string]any
					if err := json.Unmarshal([]byte(strings.TrimPrefix(m.Content, "Delegated project task outcomes:\n")), &out); err != nil {
						t.Fatal(err)
					}
					return out
				}
			}
			t.Fatal("continuation without outcome message")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("goal was not woken")
	return nil
}

func (h *remoteWaitHarness) register(t *testing.T, ids ...string) {
	t.Helper()
	h.commitWait(t, ids...)
	// The executor's yield hook (sessions_v3_executor.go) starts this watcher.
	h.home.s.watchRemoteTaskWait(remoteWaitGoal, "run-1")
}

// commitWait runs the tool and checks the durable waiting_tasks intent, without
// starting the watcher (as if the daemon stopped right after the commit).
func (h *remoteWaitHarness) commitWait(t *testing.T, ids ...string) {
	t.Helper()
	out, err := h.wait(t, remoteWaitGoal, ids...)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil || result["next_action"] != "yield_until_task_outcome" || result["machine"] != "box" || result["untrusted_content"] != true {
		t.Fatalf("registration result: %s %v", out, err)
	}
	waiting, _ := h.intent(t, "run-1")
	if waiting.Status != pebblestore.V3RunIntentWaitingTasks || waiting.TaskWait == nil || waiting.TaskWait.Machine != "box" || len(waiting.TaskWait.Tasks) != len(ids) || waiting.TaskWait.Tasks[0].AttemptID != "attempt-"+ids[0] || waiting.TaskWait.Tasks[0].SessionID != "rs-"+ids[0] {
		t.Fatalf("registration intent: %+v", waiting)
	}
}

// noWake asserts (for a bounded window) that the goal was not woken.
func (h *remoteWaitHarness) noWake(t *testing.T, window time.Duration, why string) {
	t.Helper()
	time.Sleep(window)
	if _, ok := h.intent(t, pebblestore.ProjectTaskWaitResumeID("run-1")); ok {
		t.Fatalf("woke: %s", why)
	}
	if owner, _ := h.intent(t, "run-1"); owner.Status != pebblestore.V3RunIntentWaitingTasks {
		t.Fatalf("wait left waiting_tasks (%s): %+v", why, owner)
	}
}

// watchers is the number of linked-wait watchers this home runs.
func (h *remoteWaitHarness) watchers() int {
	h.home.s.remoteTaskWaitMu.Lock()
	defer h.home.s.remoteTaskWaitMu.Unlock()
	return len(h.home.s.remoteTaskWaitRuns)
}

func remoteWaitRow(t *testing.T, out map[string]any, i int) map[string]any {
	t.Helper()
	rows, _ := out["tasks"].([]any)
	if len(rows) <= i {
		t.Fatalf("outcome rows: %v", out)
	}
	return rows[i].(map[string]any)
}

// Purpose: an Orchestrator that delegated to a linked machine can pend its
// goal until that work ends, exactly as for local tasks. Requirement: the wait
// pins the remote attempts through the link, stays pending (no wake) while the
// task runs, and when the remote task reaches needs_review the home learns it
// from the machine's activity stream plus a link get_task and wakes the goal
// with machine/task/attempt/status/summary, marked untrusted. Threat: an
// Orchestrator stranded waiting, or remote text delivered as trusted.
// Boundary: tool executeFleetWaitTasks, Server.watchRemoteTaskWait and the
// store wake over two in-process daemons linked like production.
func TestRemoteTaskWaitWakesOnReview(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1", "t2")
	h.register(t, "t1", "t2")
	h.finish(t, "t1", "completed", "First done.", "")
	time.Sleep(300 * time.Millisecond)
	if _, ok := h.intent(t, pebblestore.ProjectTaskWaitResumeID("run-1")); ok {
		t.Fatal("woke while a selected task still runs")
	}
	h.finish(t, "t2", "needs_review", "Ready for review.", "")
	out := h.awaitWake(t)
	if out["machine"] != "box" || out["untrusted_content"] != true || out["all_ready"] != true || !strings.Contains(out["guidance"].(string), "linked machine box") {
		t.Fatalf("outcome: %v", out)
	}
	second := remoteWaitRow(t, out, 1)
	if second["machine"] != "box" || second["task_id"] != "t2" || second["attempt_id"] != "attempt-t2" || second["session_id"] != "rs-t2" || second["status"] != "needs_review" || second["summary"] != "Ready for review." {
		t.Fatalf("row: %v", second)
	}
	if first := remoteWaitRow(t, out, 0); first["status"] != "completed" || first["summary"] != "First done." {
		t.Fatalf("first row: %v", first)
	}
	if owner, _ := h.intent(t, "run-1"); owner.Status != pebblestore.V3RunIntentCompleted {
		t.Fatalf("wait not retired: %+v", owner)
	}
}

// Purpose: a remote task that needs the person's input wakes the goal at once
// even while other selected tasks run. Threat: actionable input left unseen.
func TestRemoteTaskWaitWakesOnNeedsInput(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1", "t2")
	h.register(t, "t1", "t2")
	h.finish(t, "t1", "needs_input", "", "Choose the database.")
	out := h.awaitWake(t)
	first := remoteWaitRow(t, out, 0)
	if out["all_ready"] != false || first["status"] != "needs_input" || first["summary"] != "Choose the database." || remoteWaitRow(t, out, 1)["status"] != "running" {
		t.Fatalf("actionable outcome: %v", out)
	}
}

// Purpose: a machine that goes away resolves the wait as unreachable after the
// grace period, naming the machine and its last contact, instead of waiting
// forever. Threat: a dead link stranding the goal. Boundary: the watcher's
// offline handling over the real link listener being closed.
func TestRemoteTaskWaitMachineUnreachable(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.register(t, "t1")
	time.Sleep(200 * time.Millisecond) // let the watcher's stream connect
	h.remote.s.CloseFleetListener()
	out := h.awaitWake(t)
	row := remoteWaitRow(t, out, 0)
	contact, _ := out["last_contact_at"].(float64)
	if row["status"] != pebblestore.V3ProjectTaskRemoteUnreachable || row["machine"] != "box" || !strings.Contains(row["summary"].(string), "box") || !strings.Contains(row["summary"].(string), "Last contact") || contact <= 0 || out["resolution"] != pebblestore.V3ProjectTaskRemoteUnreachable {
		t.Fatalf("unreachable outcome: %v", out)
	}
}

// Purpose: a new person message supersedes the wait; a later remote outcome
// produces no wake or continuation. Threat: stale remote results resuming a
// goal the person already redirected.
func TestRemoteTaskWaitSupersededByUserMessage(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.register(t, "t1")
	in := pebblestore.V3SessionMutationInput{SessionID: remoteWaitGoal, AccountScopeID: h.home.account, UserID: h.home.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: "user-message", PayloadHash: "user-message", Message: &pebblestore.MessageSnapshot{ID: "user-message", Role: "user", Content: "Change of plan."}}
	if _, err := h.home.s.sessions.Store().ApplyV3SessionMutation(in); err != nil {
		t.Fatal(err)
	}
	if owner, _ := h.intent(t, "run-1"); owner.Status != pebblestore.V3RunIntentCancelled {
		t.Fatalf("wait not cancelled: %+v", owner)
	}
	h.finish(t, "t1", "needs_review", "Done.", "")
	time.Sleep(500 * time.Millisecond)
	if _, ok := h.intent(t, pebblestore.ProjectTaskWaitResumeID("run-1")); ok {
		t.Fatal("superseded wait produced a continuation")
	}
	messages, _ := h.home.s.sessions.Store().ListMessages(remoteWaitGoal, 0, 100)
	for _, m := range messages {
		if strings.HasPrefix(m.Content, "Delegated project task outcomes:") {
			t.Fatalf("superseded wait delivered outcomes: %s", m.Content)
		}
	}
}

// Purpose: the linked machine's work rule is respected. Off refuses
// registration without contacting the machine; turning it off mid-wait resolves
// the wait as linked_work_off. Threat: waiting on (reading) a machine the
// person turned off.
func TestRemoteTaskWaitWorkRule(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	// The permission gate (permission.Service fleet branch) decides by rule,
	// even under bypass: off denies; ask and auto run it, because waiting sends
	// or starts no task (ask prompts only for those).
	perms := permission.NewService(pebblestore.NewPermissionStore(h.home.s.sessions.Store().Underlying()), nil, nil)
	perms.SetSessionResolver(h.home.s.sessions)
	args := `{"action":"wait_tasks","machine":"box","project_id":"remote-project","task_ids":["t1"]}`
	for rule, want := range map[string]permission.AuthorizationDecision{"off": permission.AuthorizationDeny, "ask": permission.AuthorizationApprove, "auto": permission.AuthorizationApprove} {
		fleetActivityTestSetWork(t, h.home, "box", rule)
		auth, err := perms.AuthorizeToolCall(permission.AuthorizationInput{SessionID: remoteWaitGoal, AccountScopeID: h.home.account, RunID: "run-1", CallID: "call-" + rule, ToolName: "manage_projects", ToolArguments: args, Mode: "auto+bypass_permissions"})
		if err != nil || auth.Decision != want {
			t.Fatalf("%s: %+v %v", rule, auth, err)
		}
	}
	fleetActivityTestSetWork(t, h.home, "box", "off")
	if _, err := h.wait(t, remoteWaitGoal, "t1"); err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("registration with work off: %v", err)
	}
	if owner, _ := h.intent(t, "run-1"); owner.Status != pebblestore.V3RunIntentRunning || owner.TaskWait != nil {
		t.Fatalf("refused registration changed the run: %+v", owner)
	}
	fleetActivityTestSetWork(t, h.home, "box", "ask")
	h.register(t, "t1")
	fleetActivityTestSetWork(t, h.home, "box", "off")
	out := h.awaitWake(t)
	if row := remoteWaitRow(t, out, 0); row["status"] != pebblestore.V3ProjectTaskRemoteWorkOff || row["machine"] != "box" {
		t.Fatalf("work off outcome: %v", out)
	}
}

// Purpose: registration refuses what local registration refuses: a task
// session (not the home's Orchestrator), more than 16 or duplicate IDs, and a
// task with no delegated attempt; each refusal leaves the run running.
func TestRemoteTaskWaitRegistrationRefusals(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	if err := h.remote.s.sessions.Store().PutProjectTask(h.remote.account, &pebblestore.ProjectTaskRecord{ID: "unstarted", ProjectID: "remote-project", Title: "Not started", Agent: "coder", Status: "pending_approval"}); err != nil {
		t.Fatal(err)
	}
	h.createGoal(t, "task-session", map[string]any{"task_id": "parent-task", "project_id": "p"})
	if _, err := h.wait(t, "task-session", "t1"); err == nil || !strings.Contains(err.Error(), "Orchestrator") {
		t.Fatalf("task session: %v", err)
	}
	many := []string{}
	for i := 0; i < 17; i++ {
		many = append(many, "t"+string(rune('a'+i)))
	}
	for name, ids := range map[string][]string{"too_many": many, "duplicate": {"t1", "t1"}, "unstarted": {"unstarted"}, "missing": {"no-such-task"}} {
		if _, err := h.wait(t, remoteWaitGoal, ids...); err == nil {
			t.Fatalf("%s accepted", name)
		}
		if owner, _ := h.intent(t, "run-1"); owner.Status != pebblestore.V3RunIntentRunning || owner.TaskWait != nil {
			t.Fatalf("%s changed the run: %+v", name, owner)
		}
	}
}

// Purpose: a pending linked wait holds no execution capacity and never polls.
// Requirement: after registration the run is waiting_tasks (not running) and
// the box reports no busy session for it; a remote task change that the
// machine's activity stream does not report is not read, even across many
// work-rule ticks, and the same change is read once the stream reports
// activity for the pinned session. Threat: a waiting goal that keeps a run slot
// or re-reads the machine on a timer (the live failure this replaces: repeated
// get_task calls). Boundary: Server.followRemoteTaskWait (its only timers are
// the rule re-read and the unreachable deadline) and Server.boxBusy, the
// capacity authority behind /v3/box/busy.
func TestRemoteTaskWaitHoldsNoCapacityAndDoesNotPoll(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.register(t, "t1")
	busy, err := h.home.s.boxBusy()
	if err != nil {
		t.Fatal(err)
	}
	if busy.Sessions != 0 || busy.Busy {
		t.Fatalf("waiting goal holds capacity: %+v", busy)
	}
	if active, ok, err := h.home.s.sessions.Store().GetV3SessionActiveRunIntent(remoteWaitGoal); err != nil || (ok && active.Status == pebblestore.V3RunIntentRunning) {
		t.Fatalf("waiting goal still has a running run: %+v %v", active, err)
	}
	time.Sleep(200 * time.Millisecond) // let the watcher's stream connect
	// The task finishes on the machine, but nothing reports it on the stream.
	if _, err := h.remote.s.sessions.Store().UpdateProjectTask(h.remote.account, "remote-project", "t1", func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "needs_review"
		task.Attempts[0].Summary = "Silently done."
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 20 rule ticks (50ms each): a polling watcher would have read and woken.
	h.noWake(t, time.Second, "a change the stream did not report was read")
	h.touch(t, "rs-t1", "reported")
	if row := remoteWaitRow(t, h.awaitWake(t), 0); row["status"] != "needs_review" || row["summary"] != "Silently done." {
		t.Fatalf("reported outcome: %v", row)
	}
}

// Purpose: a linked wait survives a daemon restart. Requirement: a
// waiting_tasks intent committed with no watcher running (the daemon stopped
// after the commit, before or during the yield) is picked up from the durable
// status index when the fleet store is set at startup (SetFleet ->
// resumeRemoteTaskWaits), exactly one watcher runs even if recovery repeats,
// and a later remote outcome wakes the goal once. Threat: a goal stranded in
// waiting_tasks forever after restart, or duplicate wakes. Boundary: the
// startup hook over the same durable store; this proves recovery from durable
// state, not a full process restart of a provider-backed daemon.
func TestRemoteTaskWaitResumesAfterRestart(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.commitWait(t, "t1")
	if n := h.watchers(); n != 0 {
		t.Fatalf("watchers before recovery: %d", n)
	}
	h.home.s.SetFleet(h.home.s.fleet, h.home.s.fleetTokens)
	h.home.s.resumeRemoteTaskWaits()
	if n := h.watchers(); n != 1 {
		t.Fatalf("watchers after recovery: %d, want exactly 1", n)
	}
	time.Sleep(200 * time.Millisecond)
	h.finish(t, "t1", "needs_review", "Done after restart.", "")
	if row := remoteWaitRow(t, h.awaitWake(t), 0); row["status"] != "needs_review" || row["machine"] != "box" {
		t.Fatalf("outcome after restart: %v", row)
	}
	messages, err := h.home.s.sessions.Store().ListMessages(remoteWaitGoal, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	wakes := 0
	for _, m := range messages {
		if strings.HasPrefix(m.Content, "Delegated project task outcomes:") {
			wakes++
		}
	}
	if wakes != 1 {
		t.Fatalf("outcome messages: %d, want 1", wakes)
	}
}

// Purpose: local task updates never wake a linked wait. Requirement: a home
// task that shares the wait's project and task IDs reaching needs_review, and
// local wait reconciliation for the account and that project, leave the remote
// wait waiting; outcomes come only from the machine. Threat: a home record
// (or a colliding ID) being reported as the linked machine's result. Boundary:
// SessionStore.ReconcileProjectTaskWaits via Server.reconcileProjectTaskWaits,
// the path every local task publication uses.
func TestRemoteTaskWaitIgnoresLocalTaskUpdates(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.register(t, "t1")
	store := h.home.s.sessions.Store()
	if err := store.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "remote-project", Name: "Home project with the same ID"}); err != nil {
		t.Fatal(err)
	}
	local := &pebblestore.ProjectTaskRecord{ID: "t1", ProjectID: "remote-project", Title: "Home t1", Agent: "coder", Status: "needs_review", SessionID: "rs-t1", ActiveAttemptID: "attempt-t1", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-t1", SessionID: "rs-t1", Role: "coder", Status: "needs_review", Summary: "Local result."}}}
	if err := store.PutProjectTask(h.home.account, local); err != nil {
		t.Fatal(err)
	}
	h.home.s.reconcileProjectTaskWaits(h.home.account, "remote-project")
	h.home.s.reconcileProjectTaskWaits(h.home.account, "")
	h.home.s.reconcileProjectTaskWaits("", "")
	h.noWake(t, 300*time.Millisecond, "a local task update woke a linked wait")
	h.finish(t, "t1", "completed", "Remote result.", "")
	if row := remoteWaitRow(t, h.awaitWake(t), 0); row["status"] != "completed" || row["summary"] != "Remote result." {
		t.Fatalf("outcome must come from the machine: %v", row)
	}
}
