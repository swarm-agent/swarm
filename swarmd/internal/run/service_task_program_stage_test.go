package run

import (
	"os"
	"path/filepath"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktree "swarm/packages/swarmd/internal/worktree"
	"testing"
)

// Purpose: a real stage barrier must integrate only scoped committed children,
// then allocate the next stage from that exact resulting HEAD without touching
// the captured checkout. This is the narrow scheduler+Git integration layer.
func TestTaskProgramRealStageUsesIntegratedBase(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	source := programFixtureRepo(t)
	original := programFixtureGit(t, source, "rev-parse", "HEAD")
	wt := &worktree.Service{}
	svc.worktrees = wt
	base, err := wt.ResolveTaskBase(source)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := wt.AllocateTaskWorkspace(source, base, "parent-lane", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent.WorktreeEnabled = true
	parent.WorktreeRootPath = lane.WorkspacePath
	parent.WorkspacePath = lane.WorkspacePath
	parent.WorktreeBranch = lane.BranchName
	parent.WorktreeBaseBranch = "dev"
	parent.Metadata = map[string]any{"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": lane.WorkspacePath}
	laneBase, err := wt.ResolveTaskBase(lane.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	child, err := wt.AllocateTaskWorkspace(lane.WorkspacePath, laneBase, "stage-child", []string{"source.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child.WorkspacePath, "source.txt"), []byte("stage result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	programFixtureGit(t, child.WorkspacePath, "add", "source.txt")
	programFixtureGit(t, child.WorkspacePath, "commit", "-m", "stage result")
	head := programFixtureGit(t, child.WorkspacePath, "rev-parse", "HEAD")
	record := pebblestore.TaskProgramRecord{ParentSessionID: id, ProgramID: "stage-program", DefinitionHash: "hash", State: "running", ActiveStageID: "build", Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}}, Jobs: []pebblestore.TaskProgramJobSpec{{ID: "build", StageID: "build", AgentType: "coder", WorkspacePath: source, OwnedScope: []string{"source.txt"}}}}, Jobs: []pebblestore.TaskProgramJobRecord{{JobID: "build", StageID: "build", State: "handoff_ready", ChildSessionID: "stage-child", CurrentSessionID: "stage-child", WorkspacePath: child.WorkspacePath, WorktreeBranch: child.BranchName, ParentBranch: lane.BranchName, ImmutableStageBase: laneBase.BaseCommit, ChildHead: head}}}
	record, _, err = svc.sessions.CreateTaskProgram(record)
	if err != nil {
		t.Fatal(err)
	}
	p := taskProgramScheduler{service: svc, parentSession: parent, record: record}
	if err := p.integrateStage(0); err != nil {
		t.Fatal(err)
	}
	next, err := wt.ResolveTaskBase(lane.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if next.BaseCommit != p.record.ParentHead || next.BaseCommit == original || p.record.Jobs[0].State != "integrated" {
		t.Fatalf("bad stage base: %+v %+v", next, p.record)
	}
	nextChild, err := wt.AllocateTaskWorkspace(lane.WorkspacePath, next, "next-child", []string{"source.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := programFixtureGit(t, nextChild.WorkspacePath, "rev-parse", "HEAD"); got != p.record.ParentHead {
		t.Fatal("next stage forked stale base")
	}
	if got := programFixtureGit(t, source, "rev-parse", "HEAD"); got != original {
		t.Fatal("captured checkout advanced")
	}
}
