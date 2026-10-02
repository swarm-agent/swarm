package api

import (
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: a nested program's review state cannot hide current execution. The
// threat is premature task finalization, including stale initial-run callbacks.
// syncTaskSessionState and reconcileProjectTaskRunLifecycle own the projection;
// real isolated V3 store mutations are the narrowest layer proving precedence
// and account/run binding without a provider or a live daemon.
func TestProjectTask_ExecutionOutranksProgramReview(t *testing.T) {
	server, _, store := newWorkspaceOverviewTopologyTestServer(t)
	db := pebblestore.NewSessionStore(store)
	p := testPrincipal()
	project := &pebblestore.ProjectRecord{Name: "Execution precedence"}
	if err := db.PutProject(p.AccountScopeID, project); err != nil {
		t.Fatal(err)
	}
	create := func(sid string) {
		t.Helper()
		snap := pebblestore.SessionSnapshot{ID: sid, UserID: p.UserID, AccountScopeID: p.AccountScopeID, Mode: "auto", Metadata: map[string]any{"project_id": project.ID, "task_id": "task"}}
		if _, err := applyProjectLifecycleFixture(server, sessionruntime.SessionMutationInput{
			SessionID: sid, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
			ClientRequestID: "create:" + sid, IdempotencyKey: "create:" + sid, PayloadHash: "create:" + sid, RequestHash: "create:" + sid,
			Kind: sessionruntime.SessionMutationCreateSession, Session: &snap, NowUnixMs: 1000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	clock := int64(2000)
	run := func(sid, rid, status string) {
		t.Helper()
		clock++
		key := sid + ":" + rid + ":" + status
		apply := applyProjectLifecycleFixture
		if _, found, err := db.GetV3SessionRunState(sid); err == nil && found && status != pebblestore.V3RunIntentRunning && status != pebblestore.V3RunIntentPendingExecutor {
			apply = func(s *Server, input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
				return s.applySessionV3PrimaryMutation(input)
			}
		}
		if _, err := apply(server, sessionruntime.SessionMutationInput{
			SessionID: sid, UserID: p.UserID, AccountScopeID: p.AccountScopeID,
			ClientRequestID: key, IdempotencyKey: key, PayloadHash: key, RequestHash: key,
			Kind: sessionruntime.SessionMutationRecordRunIntent, NowUnixMs: clock,
			RunIntent: &pebblestore.V3SessionRunIntent{SessionID: sid, RunID: rid, Status: status, UserID: p.UserID, AccountScopeID: p.AccountScopeID, CreatedAt: clock, UpdatedAt: clock},
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("parent")
	create("child")
	run("parent", "owner", pebblestore.V3RunIntentRunning)
	for _, programState := range []string{pebblestore.TaskProgramStateBlocked, pebblestore.TaskProgramStateCompleted} {
		prog := pebblestore.TaskProgramRecord{
			ParentSessionID: "parent", ProgramID: programState, ReservationRunID: "owner", DefinitionHash: "definition", State: programState,
			Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "stage"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "job", StageID: "stage"}}},
			Jobs:       []pebblestore.TaskProgramJobRecord{{JobID: "job", StageID: "stage", State: pebblestore.TaskProgramJobCompleted, CurrentSessionID: "child", CurrentRunID: "child-run"}},
		}
		if _, _, err := db.CreateTaskProgram(prog); err != nil {
			t.Fatal(err)
		}
		task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, AccountID: p.AccountScopeID, SessionID: "parent", Status: "needs_review", TaskProgramID: programState, ActionNeeded: "Review child blocker"}
		syncTaskSessionState(task, db)
		if task.Status != "in_progress" || task.ActionNeeded != "Review child blocker" || task.TaskProgramStatus == nil {
			t.Fatalf("running parent hidden or review detail lost: %+v", task)
		}
		task.IsIntegrated = true
		syncTaskSessionState(task, db)
		if task.Status != "in_progress" || !task.IsIntegrated {
			t.Fatal("Git integration must not erase running evidence")
		}
	}
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: project.ID, AccountID: p.AccountScopeID, SessionID: "parent", Status: "in_progress", TaskProgramID: pebblestore.TaskProgramStateBlocked}
	if err := db.PutProjectTask(p.AccountScopeID, task); err != nil {
		t.Fatal(err)
	}
	// A delayed callback for an initial attempt must not demote a newer run.
	if err := server.reconcileProjectTaskRunLifecycle(sessionV3ExecutorJob{SessionID: "parent", RunID: "older", Principal: p}, pebblestore.V3RunIntentCompleted, ""); err != nil {
		t.Fatal(err)
	}
	stored, _, err := db.GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if err != nil || stored.Status != "in_progress" {
		t.Fatalf("stale callback changed task: %+v, %v", stored, err)
	}
	run("parent", "owner", pebblestore.V3RunIntentCompleted)
	run("child", "child-run", pebblestore.V3RunIntentRunning)
	syncTaskSessionState(task, db)
	if task.Status != "in_progress" {
		t.Fatal("current child hidden behind blocked program")
	}
	foreign := *task
	foreign.AccountID = "another-account"
	foreign.Status = "needs_review"
	if db.ProjectTaskExecuting(&foreign) {
		t.Fatal("cross-account execution accepted")
	}
	run("child", "child-run", pebblestore.V3RunIntentCompleted)
	syncTaskSessionState(task, db)
	if task.Status != "needs_review" {
		t.Fatalf("last termination did not restore review: %s", task.Status)
	}
	task.TaskProgramID = pebblestore.TaskProgramStateCompleted
	task.PlanBinding = &pebblestore.ProjectTaskPlanBinding{SessionID: "parent", PlanID: "not-hydrated"}
	task.Status = "blocked"
	syncTaskSessionState(task, db)
	if task.Status != "blocked" {
		t.Fatal("completed subprogram finished an unknown owning plan")
	}
	run("parent", "newer", pebblestore.V3RunIntentPendingExecutor)
	if _, ok := db.CurrentTaskProgram(task); ok {
		t.Fatal("program from old owning run accepted")
	}
	if db.ProjectTaskExecuting(task) {
		t.Fatal("queued execution shown as running")
	}
}
