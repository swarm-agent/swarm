package run

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: project cards learn child identities during allocation, not only
// after the entire stage finishes. Exercise the real scheduler transition and
// durable project store; rejected stale transitions must not rewrite card state.
func TestTaskProgramTransitionPublishesLiveCardChildren(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	parent.Metadata = map[string]any{"project_id": "project", "task_id": "task"}
	db := svc.sessions.Store()
	if err := db.PutProject(parent.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutProjectTask(parent.AccountScopeID, &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Fixture", Agent: "swarm", SessionID: id, Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	record, _, err := svc.sessions.CreateTaskProgram(pebblestore.TaskProgramRecord{ParentSessionID: id, ProgramID: "cards", DefinitionHash: "hash", State: pebblestore.TaskProgramStateDeclared, ActiveStageID: "build", Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "job", StageID: "build", AgentType: "coder", Title: "Output", MetaPrompt: "Create output", Deliverable: "commit", OwnedScope: []string{"output.txt"}, AcceptanceCriteria: []string{"committed"}, DependencyEvidence: "ready"}}}, Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "job", StageID: "build", State: pebblestore.TaskProgramJobDeclared}}})
	if err != nil {
		t.Fatal(err)
	}
	p := taskProgramScheduler{service: svc, parentSession: parent, record: record}
	state := pebblestore.TaskProgramStateRunning
	transition := pebblestore.TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "allocate", State: &state, Jobs: []pebblestore.TaskProgramJobTransition{{JobID: "job", ExpectedState: pebblestore.TaskProgramJobDeclared, State: pebblestore.TaskProgramJobRunning, AttemptNumber: 1, ChildSessionID: "child", ImmutableStageBase: "base", WorktreeBranch: "agent/child", IntegrationState: "pending_handoff"}}}
	updated, changed, err := p.transition(id, "cards", transition)
	if err != nil || !changed {
		t.Fatalf("transition: %v %v", changed, err)
	}
	task, ok, err := db.GetProjectTask(parent.AccountScopeID, "project", "task")
	if err != nil || !ok || task.TaskProgramID != "cards" || task.TaskProgramStatus == nil || task.TaskProgramStatus.Jobs[0].ChildSessionID != "child" || task.TaskProgramStatus.Jobs[0].State != pebblestore.TaskProgramJobRunning {
		t.Fatalf("live card missing child: %#v %v", task, err)
	}
	transition.MutationID = "stale"
	if _, _, err := p.transition(id, "cards", transition); err == nil {
		t.Fatal("stale transition accepted")
	}
	task, _, _ = db.GetProjectTask(parent.AccountScopeID, "project", "task")
	if task.TaskProgramStatus.Revision != updated.Revision || p.record.Revision != updated.Revision {
		t.Fatal("failed transition overwrote durable card snapshot")
	}
}
