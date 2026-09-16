package run

import (
	"os"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktree "swarm/packages/swarmd/internal/worktree"
)

// Requirement: programWorkspacePath must use the persisted pre-successor lane,
// not a refreshed parent's default. Real Git plus durable program storage proves
// exact content/base preservation and dirty refusal at the scheduler boundary.
func TestTaskProgramPersistedLaneSurvivesSuccessor(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	source := programFixtureRepo(t)
	wt := &worktree.Service{}
	svc.worktrees = wt
	base, err := wt.ResolveTaskBase(source)
	if err != nil {
		t.Fatal(err)
	}
	old, err := wt.AllocateTaskWorkspace(source, base, "original", nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := wt.AllocateTaskWorkspace(source, base, "successor", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent.WorkspacePath = source
	parent.WorktreeEnabled = true
	parent.WorktreeRootPath = next.WorkspacePath
	parent.WorktreeBranch, parent.WorktreeBaseBranch = next.BranchName, base.ParentBranch
	parent.Metadata = map[string]any{"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": next.WorkspacePath, "swarm_v3_worktree_base_commit": base.BaseCommit}
	spec := &taskProgramSpec{ID: "before-successor", Stages: []taskProgramStage{{ID: "first", DependencyEvidence: "ready"}}, Jobs: []taskProgramJob{{ID: "first", StageID: "first", RequestedSubagentType: "coder", OwnedScope: []string{"source.txt"}}}}
	record, err := taskProgramInitialRecord(id, "run", "call", spec)
	if err != nil {
		t.Fatal(err)
	}
	record.RepositoryLane = &pebblestore.TaskProgramRepositoryLane{SourcePath: source, WorkspacePath: old.WorkspacePath, Branch: old.BranchName, BaseCommit: base.BaseCommit}
	record, _, err = svc.sessions.CreateTaskProgram(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old.WorkspacePath, "source.txt"), []byte("retained commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	programFixtureGit(t, old.WorkspacePath, "add", "source.txt")
	programFixtureGit(t, old.WorkspacePath, "commit", "-m", "retained")
	retainedHead := programFixtureGit(t, old.WorkspacePath, "rev-parse", "HEAD")
	for i := 0; i < 2; i++ {
		// Reload the durable record, as on recovery; never rewrite its binding.
		saved, ok, err := svc.sessions.GetTaskProgram(id, record.ProgramID)
		if err != nil || !ok {
			t.Fatalf("load: %v", err)
		}
		p := taskProgramScheduler{service: svc, parentSession: parent, record: saved}
		got, err := p.programWorkspacePath()
		if err != nil || got != old.WorkspacePath {
			t.Fatalf("retained target: %q %v", got, err)
		}
		if mustJSON(t, p.record) != mustJSON(t, saved) {
			t.Fatal("routing rewrote persisted program")
		}
	}
	if err := os.WriteFile(filepath.Join(old.WorkspacePath, "untracked.txt"), []byte("recover me"), 0600); err != nil {
		t.Fatal(err)
	}
	p := taskProgramScheduler{service: svc, parentSession: parent, record: record}
	if _, err := p.programWorkspacePath(); err == nil {
		t.Fatal("dirty retained target accepted")
	}
	if got, err := os.ReadFile(filepath.Join(old.WorkspacePath, "untracked.txt")); err != nil || string(got) != "recover me" {
		t.Fatal("dirty work lost")
	}
	if programFixtureGit(t, old.WorkspacePath, "rev-parse", "HEAD") != retainedHead || programFixtureGit(t, source, "rev-parse", "HEAD") != base.BaseCommit || programFixtureGit(t, next.WorkspacePath, "rev-parse", "HEAD") != base.BaseCommit {
		t.Fatal("routing changed Git heads")
	}
	if content, err := os.ReadFile(filepath.Join(next.WorkspacePath, "source.txt")); err != nil || string(content) != "base\n" {
		t.Fatal("successor content changed")
	}
}

// Requirement: managed-only programs do not pin repository adoption. The durable
// session admission guard must not reconcile, allocate, or mutate their record.
func TestTaskProgramManagedOnlyDoesNotPinSuccessor(t *testing.T) {
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	spec := &taskProgramSpec{ID: "managed-only", Stages: []taskProgramStage{{ID: "design", DependencyEvidence: "ready"}}, Jobs: []taskProgramJob{{ID: "design", StageID: "design", RequestedSubagentType: "designer", OutputMode: "managed"}}}
	record, err := taskProgramInitialRecord(id, "run", "call", spec)
	if err != nil {
		t.Fatal(err)
	}
	record.State = pebblestore.TaskProgramStateRunning
	if _, _, err = svc.sessions.CreateTaskProgram(record); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := svc.sessions.EnsureWorkspaceTransitionIdle(id); err != nil {
			t.Fatal(err)
		}
	}
}
