package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	messageTaskProject = "msg-project"
	messageTaskID      = "msg-task"
)

// seedMessageTask binds a running project task to sessionID, the task
// session a delegated attempt runs in.
func seedMessageTask(t *testing.T, store *pebblestore.SessionStore, p identity.Principal, sessionID string) {
	t.Helper()
	if err := store.PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: messageTaskProject, AccountID: p.AccountScopeID, Name: "Messages"}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: messageTaskID, ProjectID: messageTaskProject, AccountID: p.AccountScopeID, Title: "Task", Agent: "coder", Status: "running", SessionID: sessionID, ActiveAttemptID: "attempt-1", Attempts: []pebblestore.ProjectTaskAttempt{{ID: "attempt-1", SessionID: sessionID, Role: "coder", Status: "running"}}}
	if err := store.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
}

// messageTaskRuntime is the native manage_projects tool over server, the
// Orchestrator's view (orchestrator is its own session).
func messageTaskRuntime(t *testing.T, server *Server) *tool.Runtime {
	t.Helper()
	rt := tool.NewRuntime(1)
	t.Cleanup(func() { _ = rt.Close() })
	rt.SetManageSessionService(server.sessions)
	rt.SetManageProjectStore(server.sessions.Store())
	rt.SetProjectTaskLifecycleService(server)
	return rt
}

func callManageProjects(t *testing.T, rt *tool.Runtime, sessionID string, p identity.Principal, args map[string]any) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(args)
	return rt.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: sessionID, Principal: p}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
}

// Purpose: the Orchestrator's message_task on a running task reaches that
// task's run as the person's queued note, through the same channel the
// person's own mid-run message takes: the reply is small and says queued with
// the running run's id, the note arrives in the same run's next provider
// request as a user item after the tool result, exactly once, no second run
// starts and the task is not reopened (revision and attempts unchanged).
// expected_revision and client_request_id are optional.
// Threat: a mid-run instruction lost, wrapped in tool output, starting a
// competing run, or forcing a reopen (and a get_task) of a running task.
// Boundary: POST /v3/projects/{id}/tasks/{task}/message (handleProjectTaskControl),
// Server.MessageProjectTask, queueProjectTaskMessage, acceptSessionsV3QueuedNote.
// Narrowest layer: the real HTTP route, executor and run loop with a scripted
// model, since delivery spans all of them.
func TestMessageTaskQueuesIntoTheRunningTaskRun(t *testing.T) {
	const steer = "also-update-the-changelog"
	var (
		mu     sync.Mutex
		status int
		reply  string
	)
	f := newQueuedNoteFixture(t, func(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
		if hasFunctionCallOutput(req.Input) {
			return provideriface.Response{Text: "finished", StopReason: "stop"}
		}
		code, out := f.do(http.MethodPost, "/v3/projects/"+messageTaskProject+"/tasks/"+messageTaskID+"/message", `{"message":"`+steer+`","client_request_id":"steer-1"}`)
		mu.Lock()
		status, reply = code, out
		mu.Unlock()
		return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: "probe1", Name: "probe_tool", Arguments: `{}`}}}
	})
	p := testPrincipal()
	seedMessageTask(t, f.sessions.Store(), p, f.sessionID)
	before, _, _ := f.sessions.Store().GetProjectTask(p.AccountScopeID, messageTaskProject, messageTaskID)
	f.say(f.sessionID, "start-1", "begin the task")

	startRun := stableSessionsV3PrimaryRunID(f.sessionID, "start-1")
	mu.Lock()
	code, out := status, reply
	mu.Unlock()
	var got tool.ProjectTaskMessageResult
	if code != http.StatusOK || json.Unmarshal([]byte(out), &got) != nil || got.DeliveredAs != tool.ProjectTaskMessageQueued || got.RunID != startRun || got.TaskID != messageTaskID {
		t.Fatalf("message_task on a running task: %d %s, want queued into %s", code, out, startRun)
	}
	if len(strings.TrimSpace(out)) > 400 {
		t.Fatalf("reply is %d bytes, want at most 400: %s", len(out), out)
	}
	requests := f.requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2 (tool call, then answer in the same run)", len(requests))
	}
	if toolResults, userMessages := countInInput(requests[1].Input, steer); toolResults != 0 || userMessages != 1 {
		t.Fatalf("message arrived %d times in tool results and %d as user messages; want once, as a user message", toolResults, userMessages)
	}
	if intents := f.runIntents(); len(intents) != 1 || intents[0].RunID != startRun {
		t.Fatalf("run intents = %+v, want only the task's running run", intents)
	}
	after, _, _ := f.sessions.Store().GetProjectTask(p.AccountScopeID, messageTaskProject, messageTaskID)
	if after.Revision != before.Revision || len(after.Attempts) != len(before.Attempts) {
		t.Fatalf("a queued message changed the task: revision %d -> %d, attempts %d -> %d", before.Revision, after.Revision, len(before.Attempts), len(after.Attempts))
	}
	// The run has ended. A retry with the same key replays the queued
	// delivery: no reopen, no second note.
	code, out = f.do(http.MethodPost, "/v3/projects/"+messageTaskProject+"/tasks/"+messageTaskID+"/message", `{"message":"`+steer+`","client_request_id":"steer-1"}`)
	var retried tool.ProjectTaskMessageResult
	if code != http.StatusOK || json.Unmarshal([]byte(out), &retried) != nil || retried != got {
		t.Fatalf("retry after the run ended: %d %s, want the original %+v", code, out, got)
	}
	notes := 0
	for _, m := range f.messages() {
		if m.Content == steer {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("message stored %d times, want once", notes)
	}
	if again, _, _ := f.sessions.Store().GetProjectTask(p.AccountScopeID, messageTaskProject, messageTaskID); again.Revision != before.Revision || len(again.Attempts) != len(before.Attempts) {
		t.Fatalf("a retried message changed the task: revision %d -> %d, attempts %d -> %d", before.Revision, again.Revision, len(before.Attempts), len(again.Attempts))
	}
	// The same key for a different message is refused.
	if code, out := f.do(http.MethodPost, "/v3/projects/"+messageTaskProject+"/tasks/"+messageTaskID+"/message", `{"message":"something else","client_request_id":"steer-1"}`); code != http.StatusConflict {
		t.Fatalf("reused key for a different message: %d %s", code, out)
	}
	// An empty message and an unknown task are refused without a write.
	if code, _ := f.do(http.MethodPost, "/v3/projects/"+messageTaskProject+"/tasks/"+messageTaskID+"/message", `{"message":"  "}`); code != http.StatusBadRequest {
		t.Fatalf("empty message: %d", code)
	}
	if code, _ := f.do(http.MethodPost, "/v3/projects/"+messageTaskProject+"/tasks/no-such-task/message", `{"message":"x"}`); code != http.StatusNotFound {
		t.Fatalf("unknown task: %d", code)
	}
	if n := len(f.messages()); n == 0 {
		t.Fatal("no messages")
	}
}

// Purpose: stop_task stops the task's active run with the person's stop: the
// native tool answers stopped with the run id in at most 300 bytes and the
// run's durable intent is cancelled; a second stop is refused (no active run)
// and changes nothing; a task of another account cannot be stopped.
// Threat: a stop that leaves the run going, reports success for a run that
// was not running, or reaches another account's task.
// Boundary: manage_projects stop_task (executeManageProjectTasksContext),
// Server.StopProjectTask, stopActiveSessionRun, sessionV3Executor.CancelRun.
// Narrowest layer: the native tool over the real executor with a provider
// that blocks until its run is cancelled.
func TestStopTaskStopsTheTaskRun(t *testing.T) {
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "stop.pebble"), nil)
	defer f.close()
	started := make(chan struct{}, 1)
	f.runner.handler = func(ctx context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return provideriface.Response{}, ctx.Err()
	}
	taskSession := f.createSession("task-session")
	orchestrator := f.createSession("orchestrator")
	p := testPrincipal()
	seedMessageTask(t, f.sessions.Store(), p, taskSession)
	if code, out := f.do(http.MethodPost, "/v3/sessions/"+taskSession+"/messages", `{"client_request_id":"start-1","role":"user","content":"work"}`); code != http.StatusOK {
		t.Fatalf("start: %d %s", code, out)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("task run never started")
	}
	runID := stableSessionsV3PrimaryRunID(taskSession, "start-1")
	rt := messageTaskRuntime(t, f.server)

	out, err := callManageProjects(t, rt, orchestrator, p, map[string]any{"action": "stop_task", "project_id": messageTaskProject, "task_id": messageTaskID})
	if err != nil {
		t.Fatal(err)
	}
	var stopped map[string]any
	if json.Unmarshal([]byte(out), &stopped) != nil || stopped["status"] != "stopped" || stopped["run_id"] != runID || stopped["task_id"] != messageTaskID {
		t.Fatalf("stop_task: %s", out)
	}
	if len(out) > 300 {
		t.Fatalf("reply is %d bytes, want at most 300: %s", len(out), out)
	}
	f.waitRun(taskSession, runID, sessionruntime.RunIntentCancelled)

	if _, err := callManageProjects(t, rt, orchestrator, p, map[string]any{"action": "stop_task", "project_id": messageTaskProject, "task_id": messageTaskID}); err == nil || !strings.Contains(err.Error(), "no active run") {
		t.Fatalf("second stop: %v", err)
	}
	other := identity.Principal{Type: identity.PrincipalTypeUser, UserID: p.UserID, AccountScopeID: "other-account"}
	if _, err := f.server.StopProjectTask(context.Background(), other, messageTaskProject, messageTaskID); err == nil || !strings.Contains(err.Error(), "task not found") {
		t.Fatalf("cross-account stop: %v", err)
	}
	if intents := f.runIntents(taskSession); len(intents) != 1 {
		t.Fatalf("run intents after stops = %+v, want only the stopped run", intents)
	}
}

func (f *confirmationFixture) runIntents(sessionID string) []pebblestore.V3SessionRunIntent {
	f.t.Helper()
	intents, err := f.sessions.Store().ListRunIntents(sessionID, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return intents
}

// Purpose: message_task on a finished task (no active run) reopens it with the
// message as the follow-up, at the task's current revision when the caller
// gives none: the reply says reopened with the new run, the task has a new
// attempt whose request is the message, and its follow-up run is recorded in
// the task session.
// Threat: a message to a finished task silently dropped as a note no run will
// read, or the Orchestrator forced to get_task for a revision first.
// Boundary: Server.MessageProjectTask -> ReopenProjectTask (the canonical
// follow-up authority) through the native manage_projects message_task.
// Narrowest layer: real Git source, worktree service, task store and V3
// executor; the reopen authority needs all of them.
func TestMessageTaskReopensAFinishedTask(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	child := filepath.Join(root, "child")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	integrationGit(t, source, "init", "-b", "dev")
	integrationGit(t, source, "commit", "--allow-empty", "-m", "base")
	base := integrationGit(t, source, "rev-parse", "HEAD")
	integrationGit(t, source, "worktree", "add", "-b", "agent/task", child)

	f := newConfirmationFixture(t, filepath.Join(root, "store"), nil)
	defer f.close()
	// The reopened run stays active until the test ends, so a retry meets it
	// running.
	release := make(chan struct{})
	defer close(release)
	f.runner.handler = func(ctx context.Context, _ provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		select {
		case <-release:
			return provideriface.Response{Text: "done", StopReason: "stop"}, nil
		case <-ctx.Done():
			return provideriface.Response{}, ctx.Err()
		}
	}
	orchestrator := f.createSession("orchestrator")
	p := testPrincipal()
	db := f.sessions.Store()
	catalogDB, err := pebblestore.Open(filepath.Join(root, "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalogDB.Close()
	f.server.workspace = workspaceruntime.NewService(pebblestore.NewWorkspaceStore(catalogDB))
	if _, _, err := pebblestore.NewWorkspaceStore(catalogDB).CreateForAccountIfAbsent(p.AccountScopeID, source, "source", ""); err != nil {
		t.Fatal(err)
	}
	proj := &pebblestore.ProjectRecord{ID: messageTaskProject, AccountID: p.AccountScopeID, Name: "Messages", Workspaces: []pebblestore.ProjectWorkspaceRef{{Path: source}}}
	if err := db.PutProject(p.AccountScopeID, proj); err != nil {
		t.Fatal(err)
	}
	bound, err := f.server.resolveProjectTaskSource(p, proj, source, "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	// The source's canonical local binding, as adding the folder records it.
	if _, err := f.server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID: "binding-message-task-source", UserID: p.UserID, AccountScopeID: p.AccountScopeID,
		SourceWorkspaceID: bound.WorkspaceID, SourceWorkspaceGeneration: bound.WorkspaceGeneration, SourceWorkspacePath: source, SourceWorkspaceName: "source",
		DestinationRuntimeSwarmID: "host-swarm-id", DestinationAuthorityHostSwarmID: "host-swarm-id", DestinationHostSwarmID: "host-swarm-id",
		DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost, DestinationWorkspacePath: source, PlacementGeneration: 1, BindingGeneration: 1,
		State: pebblestore.TopologyWorkspaceBindingStateBound, AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind: pebblestore.TopologyWorkspaceBindingMaterializationSource, AttestedByHostSwarmID: "host-swarm-id", Writable: true,
	}); err != nil {
		t.Fatal(err)
	}
	session := pebblestore.SessionSnapshot{ID: "finished-child", AccountScopeID: p.AccountScopeID, UserID: p.UserID, WorkspacePath: child, WorktreeEnabled: true, WorktreeRootPath: child, WorktreeBranch: "agent/task", WorktreeBaseBranch: "dev", Metadata: map[string]any{"swarm_v3_source_workspace_path": source, "base_commit": base, "project_id": proj.ID, "task_id": messageTaskID, "swarm_v3_source_workspace_id": bound.WorkspaceID, "swarm_v3_source_workspace_generation": bound.WorkspaceGeneration, "swarm_v3_worktree_owner_session_id": "finished-child", "swarm_v3_runtime_workspace_path": child}}
	if err := db.CreateSessionForAccount(session, p.UserID, p.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRepositoryHistoryMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "legacy", AllocatedRuntimeRoot: true, Path: child, SourcePath: source, OwnerSessionID: session.ID, Branch: session.WorktreeBranch}, Kind: sessionruntime.SessionMutationUpdateMetadata, SessionID: session.ID, AccountScopeID: p.AccountScopeID, UserID: p.UserID, ClientRequestID: "create-child", PayloadHash: "create-child", Session: &session}); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: messageTaskID, ProjectID: proj.ID, AccountID: p.AccountScopeID, Title: "Finished", Agent: "coder", Status: "needs_review", SessionID: session.ID, WorkspacePath: child, WorktreeBranch: "agent/task", BaseBranch: "dev", BaseCommit: base, SourceWorkspace: bound}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	f.server.worktrees = &worktreeruntime.Service{}
	before, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, messageTaskID)
	before.EnsureTaskAttempts()
	rt := messageTaskRuntime(t, f.server)

	out, err := callManageProjects(t, rt, orchestrator, p, map[string]any{"action": "message_task", "project_id": proj.ID, "task_id": messageTaskID, "message": "also add a test", "client_request_id": "follow-1"})
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if json.Unmarshal([]byte(out), &sent) != nil || sent["delivered_as"] != tool.ProjectTaskMessageReopened || sent["task_id"] != messageTaskID || sent["run_id"] == "" {
		t.Fatalf("message_task on a finished task: %s", out)
	}
	if len(out) > 400 {
		t.Fatalf("reply is %d bytes, want at most 400: %s", len(out), out)
	}
	after, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, messageTaskID)
	attempt := after.ActiveAttempt()
	if after.Revision <= before.Revision || len(after.Attempts) != len(before.Attempts)+1 || attempt == nil || attempt.Request != "also add a test" {
		t.Fatalf("task not reopened with the message: revision %d -> %d, attempts %d -> %d, active %+v", before.Revision, after.Revision, len(before.Attempts), len(after.Attempts), attempt)
	}
	if _, ok, err := f.sessions.GetSessionRunIntent(after.SessionID, sent["run_id"].(string)); err != nil || !ok {
		t.Fatalf("reopened run %v not recorded in the task session: %v %v", sent["run_id"], ok, err)
	}

	// A retry with the same key while the reopened run is active replays the
	// reopen: no second attempt and no queued note on top of the follow-up.
	if _, active, err := f.sessions.GetSessionActiveRunIntent(after.SessionID); err != nil || !active {
		t.Fatalf("reopened run not active for the retry: %v %v", active, err)
	}
	retry, err := callManageProjects(t, rt, orchestrator, p, map[string]any{"action": "message_task", "project_id": proj.ID, "task_id": messageTaskID, "message": "also add a test", "client_request_id": "follow-1"})
	if err != nil {
		t.Fatal(err)
	}
	var replayed map[string]any
	if json.Unmarshal([]byte(retry), &replayed) != nil || replayed["delivered_as"] != tool.ProjectTaskMessageReopened || replayed["run_id"] != sent["run_id"] {
		t.Fatalf("retry: %s, want the original %s", retry, out)
	}
	again, _, _ := db.GetProjectTask(p.AccountScopeID, proj.ID, messageTaskID)
	if again.Revision != after.Revision || len(again.Attempts) != len(after.Attempts) {
		t.Fatalf("retry reopened again: revision %d -> %d, attempts %d -> %d", after.Revision, again.Revision, len(after.Attempts), len(again.Attempts))
	}
	messages, err := f.sessions.ListSessionMessages(after.SessionID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if runruntime.IsQueuedNote(m) {
			t.Fatalf("retry queued a note on top of the reopen: %+v", m)
		}
	}
}

// Purpose: from the home, message_task and stop_task with machine reach the
// linked machine's task session through the same task routes: a message to the
// remote running task is stored there as a queued note for its run (the
// home's reply carries delivered_as queued and that run id), stop_task cancels
// the remote run's durable intent, and the remote's refusals (a second stop
// with no active run, an unknown task) come back as errors naming the machine.
// Threat: the Orchestrator unable to redirect or stop linked work, or a remote
// refusal reported as success.
// Boundary: executeFleetProjects (FleetProjectActionAllowed), Server.CallFleetProject
// path mapping, and on the remote handleProjectTaskControl ->
// MessageProjectTask/StopProjectTask. Narrowest layer: two in-process
// daemons over the real pinned link, as the other linked-task tests.
func TestLinkedMessageAndStopTaskReachTheRemoteSession(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	remote := h.remote.s
	for _, in := range []sessionruntime.SessionMutationInput{
		{ClientRequestID: "rs-pending", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-run", Status: pebblestore.V3RunIntentPendingExecutor}},
		{ClientRequestID: "rs-running", RunIntent: &pebblestore.V3SessionRunIntent{RunID: "child-run", Status: pebblestore.V3RunIntentRunning}},
	} {
		in.Kind, in.SessionID, in.UserID, in.AccountScopeID = sessionruntime.SessionMutationRecordRunIntent, "rs-t1", h.remote.user, h.remote.account
		in.IdempotencyKey, in.PayloadHash = in.ClientRequestID, in.ClientRequestID
		if _, err := remote.applySessionV3PrimaryMutation(in); err != nil {
			t.Fatal(err)
		}
	}
	// The remote run takes queued notes, as an executing run does.
	remote.v3SessionExecutor.mu.Lock()
	remote.v3SessionExecutor.openQueuedNotesLocked(sessionV3ExecutorJob{SessionID: "rs-t1", RunID: "child-run", queuedNotes: runruntime.NewQueuedNoteTracker(0)})
	remote.v3SessionExecutor.mu.Unlock()

	sent, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "message_task", "machine": "box", "project_id": "remote-project", "task_id": "t1", "message": "also cover the edge case"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := sent["data"].(map[string]any)
	if sent["untrusted_content"] != true || data["delivered_as"] != tool.ProjectTaskMessageQueued || data["run_id"] != "child-run" || data["task_id"] != "t1" {
		t.Fatalf("linked message_task: %v", sent)
	}
	if raw, _ := json.Marshal(sent); len(raw) > 400 {
		t.Fatalf("linked reply is %d bytes, want at most 400: %s", len(raw), raw)
	}
	messages, err := remote.sessions.ListSessionMessages("rs-t1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var note *pebblestore.MessageSnapshot
	for i := range messages {
		if messages[i].Content == "also cover the edge case" {
			note = &messages[i]
		}
	}
	if note == nil || note.Role != "user" || !runruntime.IsQueuedNote(*note) || note.Metadata[runruntime.QueuedNoteRunMetadataKey] != "child-run" {
		t.Fatalf("remote session has no queued note for its run: %+v", note)
	}

	stopped, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "stop_task", "machine": "box", "project_id": "remote-project", "task_id": "t1"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = stopped["data"].(map[string]any)
	if data["status"] != "stopped" || data["run_id"] != "child-run" {
		t.Fatalf("linked stop_task: %v", stopped)
	}
	if raw, _ := json.Marshal(stopped); len(raw) > 300 {
		t.Fatalf("linked stop reply is %d bytes, want at most 300: %s", len(raw), raw)
	}
	intent, ok, err := remote.sessions.GetSessionRunIntent("rs-t1", "child-run")
	if err != nil || !ok || intent.Status != sessionruntime.RunIntentCancelled {
		t.Fatalf("remote run not cancelled: %+v %v %v", intent, ok, err)
	}
	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "stop_task", "machine": "box", "project_id": "remote-project", "task_id": "t1"}); err == nil || !strings.Contains(err.Error(), "box refused stop_task") || !strings.Contains(err.Error(), "no active run") {
		t.Fatalf("remote refusal not passed through: %v", err)
	}
	if _, err := linkedTaskCall(t, h, "home-orch", map[string]any{"action": "message_task", "machine": "box", "project_id": "remote-project", "task_id": "no-such-task", "message": "x"}); err == nil || !strings.Contains(err.Error(), "box refused message_task") || !strings.Contains(err.Error(), "task not found") {
		t.Fatalf("remote not-found not passed through: %v", err)
	}
}

// Purpose: reading a linked task's latest work costs a few KB: by default
// read_task_session returns only the last 5 assistant/tool messages, newest
// kept first, and its whole native reply stays within 5 KB even when the
// remote session holds large messages; max_chars raises the content budget up
// to 20 KB and no further, and role selects other messages.
// Threat: one read pulling ~80 KB of remote transcript into the
// Orchestrator's context, or losing the newest message to the budget.
// Boundary: Server.ReadFleetLinkedTaskSession (normalizeFleetTaskSessionRead,
// boundLinkedTaskMessages) behind the native read_task_session action.
// Narrowest layer: two in-process daemons over the real link, since the
// remote page and the home's bounding meet there.
func TestFleetLinkedTaskSessionReadIsBoundedByDefault(t *testing.T) {
	h := newRemoteWaitHarness(t, "t1")
	h.createGoal(t, "home-orch", map[string]any{"project_id": "home-project"})
	big := strings.Repeat("<step & detail> ", 300) // 4,800 bytes, escaped larger in JSON
	for i := 0; i < 12; i++ {
		role := "assistant"
		if i%3 == 2 {
			role = "user"
		}
		id := "remote-msg-" + string(rune('a'+i))
		content := id + " " + big
		if _, err := h.remote.s.sessions.Store().ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "rs-t1", AccountScopeID: h.remote.account, UserID: h.remote.user, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: id, PayloadHash: id, Message: &pebblestore.MessageSnapshot{ID: id, Role: role, Content: content}}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(args map[string]any) (map[string]any, int) {
		t.Helper()
		args["action"], args["machine"], args["project_id"], args["task_id"] = "read_task_session", "box", "remote-project", "t1"
		raw, _ := json.Marshal(args)
		out, err := h.runtime.ExecuteForWorkspaceScopeWithRuntime(context.Background(), tool.WorkspaceScope{SessionID: "home-orch", Principal: h.p}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
		if err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		return view, len(out)
	}

	view, size := read(map[string]any{})
	if size > 5*1024 {
		t.Fatalf("default read is %d bytes, want at most 5 KB", size)
	}
	messages, _ := view["messages"].([]any)
	if len(messages) == 0 || len(messages) > 5 || view["has_more_older"] != true || view["untrusted_content"] != true {
		t.Fatalf("default read: %d messages %v", len(messages), view)
	}
	newest := messages[len(messages)-1].(map[string]any)
	if !strings.HasPrefix(newest["content"].(string), "remote-msg-k ") {
		t.Fatalf("the newest assistant message was not kept: %.40v", newest["content"])
	}
	for _, m := range messages {
		if role := m.(map[string]any)["role"]; role != "assistant" && role != "tool" {
			t.Fatalf("default read returned a %v message", role)
		}
	}

	wide, size := read(map[string]any{"max_chars": 50000, "limit": 100})
	if size > 21*1024 || size <= 5*1024 {
		t.Fatalf("max_chars read is %d bytes, want more than 5 KB and capped near 20 KB", size)
	}
	if wide["max_chars"] != float64(20000) {
		t.Fatalf("max_chars not capped at 20000: %v", wide["max_chars"])
	}
	users, _ := read(map[string]any{"role": "user"})
	for _, m := range users["messages"].([]any) {
		if m.(map[string]any)["role"] != "user" {
			t.Fatalf("role=user returned %v", m)
		}
	}
	if len(users["messages"].([]any)) == 0 {
		t.Fatal("role=user returned nothing")
	}
	if code, _ := linkedTaskRoute(t, h, h.home.user, fleetLinkedTaskSessionPath+"?machine=box&project_id=remote-project&task_id=t1&role=boss"); code != http.StatusBadRequest {
		t.Fatalf("unknown role: %d", code)
	}
}
