package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

func openTaskProgramTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func taskProgramStoreFixture(sessionID, programID, hash string) TaskProgramRecord {
	return TaskProgramRecord{
		ParentSessionID: sessionID, ProgramID: programID, DefinitionHash: hash,
		Definition: TaskProgramDefinition{
			Stages: []TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}},
			Jobs:   []TaskProgramJobSpec{{ID: "api", StageID: "build", AgentType: "coder", Title: "API", MetaPrompt: "implement API", Deliverable: "commit", OwnedScope: []string{"api/**"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "ready"}},
		},
		ActiveStageID: "build", State: TaskProgramStateDeclared, NextAction: "launch_ready_jobs",
		Jobs: []TaskProgramJobRecord{{JobID: "api", StageID: "build", State: TaskProgramJobDeclared}},
	}
}

func TestTaskProgramStoreScopesCreationAndStatusToParentSession(t *testing.T) {
	store := openTaskProgramTestStore(t)
	sessions := NewSessionStore(store)
	created, fresh, err := sessions.CreateTaskProgram(taskProgramStoreFixture("parent-a", "release", "hash-a"))
	if err != nil || !fresh || created.Revision != 1 {
		t.Fatalf("create=%#v fresh=%v err=%v", created, fresh, err)
	}
	duplicate, fresh, err := sessions.CreateTaskProgram(taskProgramStoreFixture("parent-a", "release", "hash-a"))
	if err != nil || fresh || duplicate.Revision != created.Revision {
		t.Fatalf("duplicate=%#v fresh=%v err=%v", duplicate, fresh, err)
	}
	if _, _, err := sessions.CreateTaskProgram(taskProgramStoreFixture("parent-a", "release", "hash-b")); err == nil || !strings.Contains(err.Error(), "different validated definition") {
		t.Fatalf("definition collision err=%v", err)
	}
	if _, ok, err := sessions.GetTaskProgram("parent-b", "release"); err != nil || ok {
		t.Fatalf("cross-parent lookup ok=%v err=%v", ok, err)
	}
}

func TestTaskProgramStoreRevisionGuardsAndIdempotentTransitions(t *testing.T) {
	store := openTaskProgramTestStore(t)
	sessions := NewSessionStore(store)
	record, _, err := sessions.CreateTaskProgram(taskProgramStoreFixture("parent", "release", "hash"))
	if err != nil {
		t.Fatal(err)
	}
	state, next := TaskProgramStateRunning, "await_running_jobs"
	transition := TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "launch:call-1", State: &state, NextAction: &next, Jobs: []TaskProgramJobTransition{{JobID: "api", ExpectedState: TaskProgramJobDeclared, State: TaskProgramJobRunning, AttemptNumber: 1, ChildSessionID: "child", ImmutableStageBase: "base", WorktreeBranch: "agent/api", IntegrationState: "pending_handoff"}}}
	updated, changed, err := sessions.TransitionTaskProgram("parent", "release", transition)
	if err != nil || !changed || updated.Revision != 2 || updated.Jobs[0].ChildSessionID != "child" {
		t.Fatalf("transition=%#v changed=%v err=%v", updated, changed, err)
	}
	duplicate, changed, err := sessions.TransitionTaskProgram("parent", "release", transition)
	if err != nil || changed || duplicate.Revision != updated.Revision {
		t.Fatalf("duplicate=%#v changed=%v err=%v", duplicate, changed, err)
	}
	transition.MutationID = "different"
	if _, _, err := sessions.TransitionTaskProgram("parent", "release", transition); err == nil || !strings.Contains(err.Error(), "revision mismatch") {
		t.Fatalf("stale transition err=%v", err)
	}
}

func TestTaskProgramStorePersistsStructuredBlockerForNewProgramPlanning(t *testing.T) {
	store := openTaskProgramTestStore(t)
	sessions := NewSessionStore(store)
	record, _, err := sessions.CreateTaskProgram(taskProgramStoreFixture("parent", "release", "hash"))
	if err != nil {
		t.Fatal(err)
	}
	state, next := TaskProgramStateBlocked, "author_new_program_for_remaining_work"
	blocker := TaskProgramBlocker{Code: "integration_conflict", Message: "conflict", NextAction: next, RepairAction: next, ProgramID: "release", ProgramRevision: 2, StageID: "build", JobID: "api", AttemptNumber: 1, ExpectedParentHead: "base", PreservedChildren: []TaskProgramPreservedChild{{JobID: "api", State: TaskProgramJobHandoffReady, ChildSessionID: "child", ImmutableStageBase: "base", ChildHead: "head"}}}
	blocked, _, err := sessions.TransitionTaskProgram("parent", "release", TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "blocked", State: &state, NextAction: &next, Blocker: &blocker})
	if err != nil || blocked.Blocker == nil || blocked.Blocker.NextAction != next || len(blocked.Blocker.PreservedChildren) != 1 {
		t.Fatalf("blocked=%#v err=%v", blocked, err)
	}
}

func TestTaskProgramStoreParallelIntegrationDependentFixerAndTerminalCompletion(t *testing.T) {
	store := openTaskProgramTestStore(t)
	sessions := NewSessionStore(store)
	fixture := taskProgramStoreFixture("parent", "pipeline", "hash")
	fixture.Definition.Stages = []TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}, {ID: "fix", DependsOn: []string{"build"}, DependencyEvidence: "integrated build required"}}
	fixture.Definition.Jobs = []TaskProgramJobSpec{
		{ID: "api", StageID: "build", AgentType: "coder", Title: "API", MetaPrompt: "implement API", Deliverable: "commit", OwnedScope: []string{"api/**"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "ready"},
		{ID: "web", StageID: "build", AgentType: "coder", Title: "Web", MetaPrompt: "implement Web", Deliverable: "commit", OwnedScope: []string{"web/**"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "ready"},
		{ID: "fixer", StageID: "fix", DependsOn: []string{"api", "web"}, AgentType: "coder", Title: "Fixer", MetaPrompt: "fix integration", Deliverable: "commit", OwnedScope: []string{"integration/**"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "integrated build required"},
	}
	fixture.Jobs = []TaskProgramJobRecord{{JobID: "api", StageID: "build", State: TaskProgramJobHandoffReady, AttemptNumber: 1}, {JobID: "web", StageID: "build", State: TaskProgramJobHandoffReady, AttemptNumber: 1}, {JobID: "fixer", StageID: "fix", State: TaskProgramJobDeclared}}
	record, _, err := sessions.CreateTaskProgram(fixture)
	if err != nil {
		t.Fatal(err)
	}
	integratedHead, integrateNext := "integrated-head", "advance_stage"
	integrated, _, err := sessions.TransitionTaskProgram("parent", "pipeline", TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "integrate-build", ParentHead: &integratedHead, NextAction: &integrateNext, Jobs: []TaskProgramJobTransition{{JobID: "api", ExpectedState: TaskProgramJobHandoffReady, State: TaskProgramJobIntegrated, IntegrationState: "integrated"}, {JobID: "web", ExpectedState: TaskProgramJobHandoffReady, State: TaskProgramJobIntegrated, IntegrationState: "integrated"}}})
	if err != nil {
		t.Fatal(err)
	}
	fixStage, launchNext := "fix", "launch_ready_jobs"
	advanced, _, err := sessions.TransitionTaskProgram("parent", "pipeline", TaskProgramTransition{ExpectedRevision: integrated.Revision, MutationID: "advance-fix", ActiveStageID: &fixStage, NextAction: &launchNext})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.ParentHead != integratedHead || advanced.Jobs[0].State != TaskProgramJobIntegrated || advanced.Jobs[1].State != TaskProgramJobIntegrated {
		t.Fatalf("dependent stage did not retain integrated barrier: %#v", advanced)
	}
	runningState, runningNext := TaskProgramStateRunning, "await_running_jobs"
	running, _, err := sessions.TransitionTaskProgram("parent", "pipeline", TaskProgramTransition{ExpectedRevision: advanced.Revision, MutationID: "run-fixer", State: &runningState, NextAction: &runningNext, Jobs: []TaskProgramJobTransition{{JobID: "fixer", ExpectedState: TaskProgramJobDeclared, State: TaskProgramJobRunning, AttemptNumber: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	completedState, completedNext := TaskProgramStateCompleted, "none"
	completed, _, err := sessions.TransitionTaskProgram("parent", "pipeline", TaskProgramTransition{ExpectedRevision: running.Revision, MutationID: "complete-pipeline", State: &completedState, NextAction: &completedNext, Jobs: []TaskProgramJobTransition{{JobID: "fixer", ExpectedState: TaskProgramJobRunning, State: TaskProgramJobIntegrated, ChildHead: "fixer-head", IntegrationState: "integrated"}}})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != TaskProgramStateCompleted || completed.NextAction != "none" || completed.ParentHead != integratedHead || completed.Jobs[2].AttemptNumber != 1 || completed.Jobs[2].State != TaskProgramJobIntegrated {
		t.Fatalf("terminal pipeline record = %#v", completed)
	}
}

func TestTaskProgramStoreBlockedContextPreservesIntegratedJobs(t *testing.T) {
	store := openTaskProgramTestStore(t)
	sessions := NewSessionStore(store)
	fixture := taskProgramStoreFixture("parent", "repairable", "hash")
	fixture.Definition.Stages = []TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}, {ID: "fix", DependsOn: []string{"build"}, DependencyEvidence: "integrated build required"}}
	fixture.Definition.Jobs = []TaskProgramJobSpec{
		{ID: "api", StageID: "build", AgentType: "coder", Title: "API", MetaPrompt: "implement API", Deliverable: "commit", OwnedScope: []string{"api/**"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "ready"},
		{ID: "web", StageID: "build", AgentType: "coder", Title: "Web", MetaPrompt: "implement Web", Deliverable: "commit", OwnedScope: []string{"web/**"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "ready"},
		{ID: "fixer", StageID: "fix", DependsOn: []string{"api", "web"}, AgentType: "coder", Title: "Fixer", MetaPrompt: "fix integration", Deliverable: "commit", OwnedScope: []string{"integration/**"}, AcceptanceCriteria: []string{"done"}, DependencyEvidence: "integrated build required"},
	}
	fixture.Jobs = []TaskProgramJobRecord{
		{JobID: "api", StageID: "build", State: TaskProgramJobIntegrated, AttemptNumber: 1, ChildSessionID: "api-child", ImmutableStageBase: "base", ChildHead: "api-head", IntegrationState: "integrated"},
		{JobID: "web", StageID: "build", State: TaskProgramJobHandoffReady, AttemptNumber: 1, ChildSessionID: "web-child", WorkspacePath: "web-worktree", ImmutableStageBase: "base", ChildHead: "web-head", IntegrationState: "pending"},
		{JobID: "fixer", StageID: "fix", State: TaskProgramJobDeclared},
	}
	record, _, err := sessions.CreateTaskProgram(fixture)
	if err != nil {
		t.Fatal(err)
	}
	blockedState, blockedNext := TaskProgramStateBlocked, "author_new_program_for_remaining_work"
	blocker := TaskProgramBlocker{Code: "integration_conflict", Message: "conflict", NextAction: blockedNext, RepairAction: blockedNext, ProgramID: record.ProgramID, ProgramRevision: record.Revision + 1, StageID: "build", JobID: "web", ExpectedParentHead: "base", PreservedChildren: []TaskProgramPreservedChild{{JobID: "api", State: TaskProgramJobIntegrated, ChildSessionID: "api-child", ChildHead: "api-head", IntegrationState: "integrated"}, {JobID: "web", State: TaskProgramJobHandoffReady, ChildSessionID: "web-child", ChildHead: "web-head", IntegrationState: "pending"}}}
	blocked, _, err := sessions.TransitionTaskProgram("parent", "repairable", TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "block-conflict", State: &blockedState, NextAction: &blockedNext, Blocker: &blocker})
	if err != nil || blocked.NextAction != blockedNext || blocked.Jobs[0].State != TaskProgramJobIntegrated || blocked.Jobs[1].State != TaskProgramJobHandoffReady || blocked.Blocker == nil || len(blocked.Blocker.PreservedChildren) != 2 {
		t.Fatalf("blocked context cannot guide a new remaining-work program: %#v err=%v", blocked, err)
	}
}

func TestTaskProgramStoreReconstructsBlockedDirtyRecoveryAfterReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pebble")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)
	record, _, err := sessions.CreateTaskProgram(taskProgramStoreFixture("parent", "blocked-recovery", "hash"))
	if err != nil {
		t.Fatal(err)
	}
	blockedState, blockedNext := TaskProgramStateBlocked, "resolve_named_blocker_then_author_new_program_for_unfinished_work"
	jobBlocker := TaskProgramBlocker{Code: "required_input", Message: "schema token required", Evidence: []string{"API returned 401"}, CompletedScope: []string{"parser implemented"}, ResolutionRequirement: "provide a scoped schema token", Dirty: true, ChangedFiles: []string{"parser.go"}, NextAction: blockedNext}
	programBlocker := jobBlocker
	programBlocker.ProgramID = record.ProgramID
	programBlocker.ProgramRevision = record.Revision + 1
	programBlocker.StageID = "build"
	programBlocker.JobID = "api"
	programBlocker.PreservedChildren = []TaskProgramPreservedChild{{JobID: "api", State: TaskProgramJobBlocked, AttemptNumber: 1, ChildSessionID: "child", RunID: "run-1", WorkspacePath: "worktree", WorktreeBranch: "agent/api", ImmutableStageBase: "base", ChildHead: "head", IntegrationState: "blocked", Dirty: true, ChangedFiles: []string{"parser.go"}}}
	_, _, err = sessions.TransitionTaskProgram("parent", "blocked-recovery", TaskProgramTransition{
		ExpectedRevision: record.Revision, MutationID: "block-child", State: &blockedState, NextAction: &blockedNext, Blocker: &programBlocker,
		Jobs: []TaskProgramJobTransition{{JobID: "api", ExpectedState: TaskProgramJobDeclared, State: TaskProgramJobBlocked, AttemptNumber: 1, ChildSessionID: "child", CurrentSessionID: "child", CurrentRunID: "run-1", WorkspacePath: "worktree", WorktreeBranch: "agent/api", ImmutableStageBase: "base", ChildHead: "head", IntegrationState: "blocked", Blocker: &jobBlocker}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reconstructed, ok, err := NewSessionStore(store).GetTaskProgram("parent", "blocked-recovery")
	if err != nil || !ok || reconstructed.State != TaskProgramStateBlocked || reconstructed.Jobs[0].State != TaskProgramJobBlocked || reconstructed.Jobs[0].CurrentRunID != "run-1" || reconstructed.Jobs[0].Blocker == nil || !reconstructed.Jobs[0].Blocker.Dirty || len(reconstructed.Jobs[0].Blocker.ChangedFiles) != 1 || reconstructed.Blocker == nil || len(reconstructed.Blocker.PreservedChildren) != 1 || !reconstructed.Blocker.PreservedChildren[0].Dirty {
		t.Fatalf("reconstructed blocked recovery=%#v ok=%v err=%v", reconstructed, ok, err)
	}
}

func TestTaskProgramStoreReconstructsBoundedHandoffAfterReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pebble")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(store)
	record, _, err := sessions.CreateTaskProgram(taskProgramStoreFixture("parent", "release", "hash"))
	if err != nil {
		t.Fatal(err)
	}
	state, next := TaskProgramStateRunning, "integrate_handoff_ready_jobs"
	_, _, err = sessions.TransitionTaskProgram("parent", "release", TaskProgramTransition{ExpectedRevision: record.Revision, MutationID: "handoff", State: &state, NextAction: &next, Jobs: []TaskProgramJobTransition{{JobID: "api", ExpectedState: TaskProgramJobDeclared, State: TaskProgramJobHandoffReady, AttemptNumber: 1, ChildSessionID: "child", WorkspacePath: "worktree", WorktreeBranch: "agent/api", ParentBranch: "dev", ImmutableStageBase: "base", ChildHead: "head", IntegrationState: "pending"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reconstructed, ok, err := NewSessionStore(store).GetTaskProgram("parent", "release")
	if err != nil || !ok || reconstructed.NextAction != next || reconstructed.Jobs[0].ChildHead != "head" || reconstructed.Jobs[0].ImmutableStageBase != "base" {
		t.Fatalf("reconstructed=%#v ok=%v err=%v", reconstructed, ok, err)
	}
}
