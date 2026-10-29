package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// attentionWireRemote gives the remote daemon a real permission service with
// execution admission, wired the way NewServer wires one (the pending-summary
// publisher writes the session's permission.summary.updated outbox event).
func attentionWireRemote(t *testing.T, h *remoteWaitHarness) (*permission.Service, *executioncapacity.Manager) {
	t.Helper()
	perms := permission.NewService(pebblestore.NewPermissionStore(h.remote.s.sessions.Store().Underlying()), nil, nil)
	perms.SetSessionResolver(h.remote.s.sessions)
	perms.SetSummaryRealtimePublisher(h.remote.s.publishPermissionSummaryV3Realtime)
	manager := executioncapacity.NewManager(executioncapacity.ManagerConfig{})
	t.Cleanup(manager.Close)
	perms.SetExecutionCapacity(manager)
	h.remote.s.perm = perms
	return perms, manager
}

// attentionSent records, on the home, that this home sent task id to box
// (what propose_task records; TestFleetLinkedTaskRecordAndList covers that).
func attentionSent(t *testing.T, h *remoteWaitHarness, id string) {
	t.Helper()
	if _, err := h.home.s.fleet.PutLinkedTask(pebblestore.FleetLinkedTask{AccountScopeID: h.home.account, Machine: "box", ProjectID: "remote-project", TaskID: id, Title: "Remote " + id, HomeSessionID: "home-orch", Status: "running"}); err != nil {
		t.Fatal(err)
	}
}

// attentionPending makes the task session's run wait on a bash approval.
func attentionPending(t *testing.T, perms *permission.Service, session, call, command string) pebblestore.PermissionRecord {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"command": command})
	record, err := perms.CreatePending(permission.CreateInput{SessionID: session, RunID: "child-run", CallID: call, ToolName: "bash", ToolArguments: string(args), Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func attentionData(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(out["data"])
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("data: %s %v", raw, err)
	}
	return data
}

// attentionTask is the compact task view a linked get_task answers with.
func attentionTask(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	task, _ := attentionData(t, out)["task"].(map[string]any)
	if task == nil {
		t.Fatalf("no task in %v", out)
	}
	return task
}

// Purpose: a home sees why a task it sent to a linked machine is stalled and
// answers its approval in the chat. Requirement: when the remote task's run
// waits on a pending bash approval, the machine's task state is needs_you
// with a reason naming the tool, a summary of its arguments and the
// permission id; the home's linked get_task and list_tasks carry that state
// and the pending request unchanged; a home wait on the task fires (woken by
// the machine's activity stream, no polling) with the request in its outcome
// row; and resolve_task_permission approves it over the link, so the run's
// own wait for the decision returns approved and the task reads running
// again. Threat: the live incident of 2026-10-29, where three tasks read
// in_progress for an hour while each waited on a person and the Orchestrator
// guessed a cause and could not answer. Boundary: Server.markTaskProgressLive
// (the one task-state projection), compactFleetTaskList, the tool's compact
// get_task view, the linked wait watcher and store wake, and
// Server.ResolveTaskPermission over two in-process daemons linked like
// production. No provider runs; the remote run's wait is the permission
// service's own WaitForResolution.
func TestLinkedTaskNeedsYouWatchAndResolve(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.createGoal(t, "home-orch", map[string]any{})
	perms, _ := attentionWireRemote(t, h)
	attentionSent(t, h, "t1")
	h.register(t, "t1")
	h.noWake(t, 200*time.Millisecond, "a running task with nothing pending")

	record := attentionPending(t, perms, "rs-t1", "call-1", "go test ./internal/... -run TestBuild")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	decided := make(chan pebblestore.PermissionRecord, 1)
	go func() {
		got, err := perms.WaitForResolution(ctx, "rs-t1", record.ID)
		if err != nil {
			t.Errorf("run's wait for the decision: %v", err)
		}
		decided <- got
	}()

	// The watch fires with the pending request.
	out := h.awaitWake(t)
	row := remoteWaitRow(t, out, 0)
	request, _ := row["pending_request"].(map[string]any)
	if row["status"] != "running" || row["task_state"] != pebblestore.ProjectTaskStateNeedsYou || request == nil || request["permission_id"] != record.ID || request["tool"] != "bash" || !strings.Contains(request["summary"].(string), "go test ./internal/...") {
		t.Fatalf("wake row: %v", row)
	}
	if !strings.Contains(out["guidance"].(string), "resolve_task_permission") || out["untrusted_content"] != true {
		t.Fatalf("wake guidance: %v", out)
	}

	// get_task and list_tasks at the home carry the machine's state.
	got, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": "t1"})
	if err != nil {
		t.Fatal(err)
	}
	view := attentionTask(t, got)
	reason, _ := view["state_reason"].(string)
	pending, _ := view["pending_request"].(map[string]any)
	if view["state"] != pebblestore.ProjectTaskStateNeedsYou || view["needs_attention"] != true || !strings.Contains(reason, "bash") || !strings.Contains(reason, record.ID) || !strings.Contains(reason, "go test") || pending == nil || pending["permission_id"] != record.ID || view["next_action"] != "resolve_task_permission" {
		t.Fatalf("linked get_task: %v", view)
	}
	list, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "list_tasks", "machine": "box", "project_id": "remote-project"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := attentionData(t, list)["tasks"].([]any)
	if len(rows) != 1 {
		t.Fatalf("linked list: %v", list)
	}
	first := rows[0].(map[string]any)
	if first["state"] != pebblestore.ProjectTaskStateNeedsYou || !strings.Contains(first["state_reason"].(string), record.ID) || first["pending_request"].(map[string]any)["permission_id"] != record.ID {
		t.Fatalf("linked list row: %v", first)
	}

	// Answering it approves that one request on the machine; the run goes on.
	answered, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "resolve_task_permission", "machine": "box", "project_id": "remote-project", "task_id": "t1", "permission_id": record.ID, "decision": "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if answered["session_id"] != "rs-t1" || answered["permission"].(map[string]any)["status"] != pebblestore.PermissionStatusApproved || answered["untrusted_content"] != true {
		t.Fatalf("answer: %v", answered)
	}
	select {
	case result := <-decided:
		if result.Status != pebblestore.PermissionStatusApproved || result.ID != record.ID {
			t.Fatalf("the run saw: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("the run's wait for the decision did not return")
	}
	got, err = linkedTaskCall(t, h, "home-orch", map[string]any{"action": "get_task", "machine": "box", "project_id": "remote-project", "task_id": "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if view := attentionTask(t, got); view["state"] != pebblestore.ProjectTaskStateRunning || view["pending_request"] != nil {
		t.Fatalf("after the answer: %v", view)
	}
}

// Purpose: answering is confined and ruled. Requirement: with the linked work
// rule off the call is refused before the machine is contacted (and the
// permission gate denies it); under ask the gate waits for the person; a
// task this home did not send cannot be answered; a permission of another
// session cannot be answered through a sent task; a delegated task session
// cannot answer at all. Every refusal leaves the request pending. Threat: a
// home (or a task) approving commands of sessions it never sent, or acting
// on a machine the person turned off. Boundary: permission.Service fleet
// branch, the tool runtime and Server.ResolveTaskPermission over the real
// link.
func TestLinkedTaskPermissionRefusals(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1", "t2")
	h.createGoal(t, "home-orch", map[string]any{})
	h.createGoal(t, "home-task", map[string]any{"task_id": "local-task", "project_id": "home-project"})
	perms, _ := attentionWireRemote(t, h)
	attentionSent(t, h, "t1")
	own := attentionPending(t, perms, "rs-t1", "call-1", "make build")
	foreign := attentionPending(t, perms, "rs-t2", "call-2", "rm -rf build")
	stillPending := func(why string) {
		t.Helper()
		for _, r := range []pebblestore.PermissionRecord{own, foreign} {
			got, ok, err := perms.GetPermission(r.SessionID, r.ID)
			if err != nil || !ok || got.Status != pebblestore.PermissionStatusPending {
				t.Fatalf("%s: %s is no longer pending: %+v %v", why, r.ID, got, err)
			}
		}
	}
	resolve := func(session, task, id string) error {
		_, err := linkedTaskCall(t, h, session, map[string]any{"action": "resolve_task_permission", "machine": "box", "project_id": "remote-project", "task_id": task, "permission_id": id, "decision": "approve"})
		return err
	}

	// The gate: off denies, ask waits for the person, auto runs.
	gate := permission.NewService(pebblestore.NewPermissionStore(h.home.s.sessions.Store().Underlying()), nil, nil)
	gate.SetSessionResolver(h.home.s.sessions)
	authorize := func(call string) permission.AuthorizationResult {
		t.Helper()
		args := `{"action":"resolve_task_permission","machine":"box","project_id":"remote-project","task_id":"t1","permission_id":"` + own.ID + `","decision":"approve"}`
		result, err := gate.AuthorizeToolCall(permission.AuthorizationInput{SessionID: "home-orch", AccountScopeID: h.home.account, RunID: "run-1", CallID: call, ToolName: "manage_projects", ToolArguments: args, Mode: "auto"})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleOff)
	if result := authorize("off"); result.Decision != permission.AuthorizationDeny || !strings.Contains(result.Reason, "off") {
		t.Fatalf("off gate: %+v", result)
	}
	if err := resolve("home-orch", "t1", own.ID); err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("off rule answered: %v", err)
	}
	if _, err := h.home.s.ResolveTaskPermission(context.Background(), h.p, "box", "remote-project", "t1", own.ID, "approve", ""); err == nil || linkedTaskStatus(err) != 403 {
		t.Fatalf("off rule, service: %v", err)
	}
	stillPending("off")
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAsk)
	if result := authorize("ask"); result.Decision != permission.AuthorizationPending || result.Record == nil {
		t.Fatalf("ask gate: %+v", result)
	}
	fleetActivityTestSetWork(t, h.home, "box", pebblestore.RuleAuto)
	if result := authorize("auto"); result.Decision != permission.AuthorizationApprove {
		t.Fatalf("auto gate: %+v", result)
	}

	// A task this home did not send.
	if err := resolve("home-orch", "t2", foreign.ID); err == nil || !strings.Contains(err.Error(), "not sent from this home") {
		t.Fatalf("unsent task answered: %v", err)
	}
	// Another session's permission through a sent task.
	if err := resolve("home-orch", "t1", foreign.ID); err == nil || !strings.Contains(err.Error(), "not pending in the session of task t1") {
		t.Fatalf("another session's permission answered: %v", err)
	}
	// A delegated task never answers requests.
	if err := resolve("home-task", "t1", own.ID); err == nil || !strings.Contains(err.Error(), "Orchestrator") {
		t.Fatalf("a task session answered: %v", err)
	}
	// Bad decisions are refused.
	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "resolve_task_permission", "machine": "box", "project_id": "remote-project", "task_id": "t1", "permission_id": own.ID, "decision": "always"}); err == nil {
		t.Fatal("decision always was accepted")
	}
	stillPending("refusals")

	// Deny answers it once, as denied.
	denied, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "resolve_task_permission", "machine": "box", "project_id": "remote-project", "task_id": "t1", "permission_id": own.ID, "decision": "deny", "reason": "not now"})
	if err != nil || denied["permission"].(map[string]any)["status"] != pebblestore.PermissionStatusDenied {
		t.Fatalf("deny: %v %v", denied, err)
	}
	if got, _, _ := perms.GetPermission(foreign.SessionID, foreign.ID); got.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("the other session's request changed: %+v", got)
	}
}

// Purpose: a task held before its run starts says so. Requirement: while the
// machine drains for an update, a task whose run was admitted but has not
// started reads waiting with "held: update drain since <time>" (count bucket
// waiting, not in_progress) at the home's linked list_tasks, and the home's
// list_machines shows that machine draining. Threat: an hour of "running"
// tasks that never ran. Boundary: Server.taskHeldLive over the execution
// admission manager's drain, compactFleetTaskList and the list_machines
// status line, over the real link.
func TestLinkedTaskHeldByDrain(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.createGoal(t, "home-orch", map[string]any{})
	_, manager := attentionWireRemote(t, h)
	if _, err := h.remote.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationRecordRunIntent, SessionID: "rs-t1", UserID: h.remote.user, AccountScopeID: h.remote.account, ClientRequestID: "held", IdempotencyKey: "held", PayloadHash: "held", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-held", Status: pebblestore.V3RunIntentPendingExecutor}}); err != nil {
		t.Fatal(err)
	}
	// Not draining: an admitted run about to start is just running.
	list, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "list_tasks", "machine": "box", "project_id": "remote-project"})
	if err != nil {
		t.Fatal(err)
	}
	if row := attentionData(t, list)["tasks"].([]any)[0].(map[string]any); row["state"] != pebblestore.ProjectTaskStateRunning {
		t.Fatalf("before the drain: %v", row)
	}
	drain, err := manager.BeginDrain(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	list, err = linkedTaskCall(t, h, "home-orch", map[string]any{"action": "list_tasks", "machine": "box", "project_id": "remote-project"})
	if err != nil {
		t.Fatal(err)
	}
	data := attentionData(t, list)
	row := data["tasks"].([]any)[0].(map[string]any)
	want := "held: update drain since " + drain.Since.UTC().Format(time.RFC3339)
	if row["state"] != pebblestore.ProjectTaskStateWaiting || row["state_reason"] != want || row["pending_request"] != nil {
		t.Fatalf("held row: %v (want %q)", row, want)
	}
	if counts := data["outcome_counts"].(map[string]any); counts[pebblestore.ProjectTaskCountWaiting] != float64(1) || counts[pebblestore.ProjectTaskCountInProgress] != float64(0) {
		t.Fatalf("held counts: %v", counts)
	}
	machines, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "list_machines"})
	if err != nil {
		t.Fatal(err)
	}
	listed := machines["machines"].([]any)[0].(map[string]any)
	status, _ := listed["status"].(map[string]any)
	if listed["name"] != "box" || status == nil || status["draining"] != true || !strings.Contains(status["summary"].(string), "draining since") || machines["untrusted_content"] != true {
		t.Fatalf("list_machines: %v", machines)
	}
	manager.CancelDrain(drain.Generation)
	list, err = linkedTaskCall(t, h, "home-orch", map[string]any{"action": "list_tasks", "machine": "box", "project_id": "remote-project"})
	if err != nil {
		t.Fatal(err)
	}
	if row := attentionData(t, list)["tasks"].([]any)[0].(map[string]any); row["state"] != pebblestore.ProjectTaskStateRunning {
		t.Fatalf("after the drain: %v", row)
	}
}

// Purpose: the home answers its own task's request the same way. Requirement:
// locally, a task whose run waits on a question reads needs_you (kind
// question) in its progress, and ResolveTaskPermission with machine "" answers
// it in that task's own session; a permission that is not pending in the
// task's session, or another principal, is refused and nothing changes.
// Boundary: Server.taskWaitProgressFor and Server.ResolveTaskPermission on one
// daemon (the remote harness daemon used as a home).
func TestLocalTaskPermissionResolve(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1", "t2")
	perms, _ := attentionWireRemote(t, h)
	s := h.remote.s
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.remote.user, AccountScopeID: h.remote.account}
	args, _ := json.Marshal(map[string]any{"question": "Which database should I use?"})
	question, err := perms.CreatePending(permission.CreateInput{SessionID: "rs-t1", RunID: "child-run", CallID: "ask-1", ToolName: "ask_user", ToolArguments: string(args), Requirement: "tool", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	other := attentionPending(t, perms, "rs-t2", "call-2", "make deploy")
	task, _, err := s.sessions.Store().GetProjectTask(h.remote.account, "remote-project", "t1")
	if err != nil {
		t.Fatal(err)
	}
	progress := s.taskWaitProgressFor(h.remote.account, *task, time.Now().UnixMilli())
	if progress.TaskState != pebblestore.ProjectTaskStateNeedsYou || !strings.HasPrefix(progress.TaskStateReason, "waiting for an answer") || progress.Live == nil || progress.Live.Request.Kind != pebblestore.ProjectTaskRequestQuestion || !progress.NeedsAttention {
		t.Fatalf("local progress: %+v", progress)
	}
	if _, err := s.ResolveTaskPermission(context.Background(), p, "", "remote-project", "t1", other.ID, "approve", ""); err == nil || linkedTaskStatus(err) != 404 {
		t.Fatalf("another session's permission answered locally: %v", err)
	}
	stranger := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "someone-else", AccountScopeID: h.remote.account}
	if _, err := s.ResolveTaskPermission(context.Background(), stranger, "", "remote-project", "t1", question.ID, "approve", "Postgres"); err == nil {
		t.Fatal("another principal answered the task's question")
	}
	if got, _, _ := perms.GetPermission("rs-t1", question.ID); got.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("refusals changed the question: %+v", got)
	}
	out, err := s.ResolveTaskPermission(context.Background(), p, "", "remote-project", "t1", question.ID, "approve", "Postgres")
	if err != nil {
		t.Fatal(err)
	}
	if out["session_id"] != "rs-t1" || out["permission"].(map[string]any)["status"] != pebblestore.PermissionStatusApproved {
		t.Fatalf("local answer: %v", out)
	}
	if got, _, _ := perms.GetPermission("rs-t2", other.ID); got.Status != pebblestore.PermissionStatusPending {
		t.Fatalf("the other task's request changed: %+v", got)
	}
	if progress := s.taskWaitProgressFor(h.remote.account, *task, time.Now().UnixMilli()); progress.TaskState != pebblestore.ProjectTaskStateRunning || progress.Live != nil {
		t.Fatalf("after the local answer: %+v", progress)
	}
}
