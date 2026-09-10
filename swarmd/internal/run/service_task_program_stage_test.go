package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktree "swarm/packages/swarmd/internal/worktree"
)

// Purpose: the scheduler barrier must integrate two disjoint committed siblings
// from one immutable base, hydrate a managed Designer with exact bounded source,
// and fork later work from that integrated HEAD. Real Git plus durable program
// storage is the narrowest layer proving these postconditions. Unintegrated,
// dirty, stale and oversized evidence must reject without advancing checkouts.
func TestTaskProgramRealStageUsesIntegratedBase(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	parent, _, err := svc.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	source, unrelated := programFixtureRepo(t), programFixtureRepo(t)
	original := programFixtureGit(t, source, "rev-parse", "HEAD")
	unrelatedBefore := programFixtureGit(t, unrelated, "worktree", "list", "--porcelain")
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
	parent.WorktreeRootPath, parent.WorkspacePath = lane.WorkspacePath, source
	parent.WorktreeBranch, parent.WorktreeBaseBranch = lane.BranchName, "dev"
	parent.Metadata = map[string]any{"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": lane.WorkspacePath, "swarm_v3_worktree_base_commit": base.BaseCommit}
	laneBase, err := wt.ResolveTaskBase(lane.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	record := pebblestore.TaskProgramRecord{ParentSessionID: id, ProgramID: "stage-program", DefinitionHash: "hash", State: "running", ActiveStageID: "build", Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}, {ID: "design", DependsOn: []string{"build"}, DependencyEvidence: "integrated source"}}}}
	children := make([]worktree.Allocation, 2)
	// Both allocations precede either commit, matching one parallel cohort's base.
	for i, file := range []string{"first.txt", "second.txt"} {
		childID := "stage-" + file
		child, err := wt.AllocateTaskWorkspace(lane.WorkspacePath, laneBase, childID, []string{file})
		if err != nil {
			t.Fatal(err)
		}
		children[i] = child
		if programFixtureGit(t, child.WorkspacePath, "rev-parse", "HEAD") != laneBase.BaseCommit || programFixtureGit(t, child.WorkspacePath, "rev-parse", "--path-format=absolute", "--git-common-dir") != programFixtureGit(t, source, "rev-parse", "--path-format=absolute", "--git-common-dir") {
			t.Fatal("child base/repository mismatch")
		}
		record.Definition.Jobs = append(record.Definition.Jobs, pebblestore.TaskProgramJobSpec{ID: childID, StageID: "build", AgentType: "coder", WorkspacePath: source, OwnedScope: []string{file}})
		record.Jobs = append(record.Jobs, pebblestore.TaskProgramJobRecord{JobID: childID, StageID: "build", State: "handoff_ready", ChildSessionID: childID, CurrentSessionID: childID, WorkspacePath: child.WorkspacePath, WorktreeBranch: child.BranchName, ParentBranch: lane.BranchName, ImmutableStageBase: laneBase.BaseCommit})
	}
	if children[0].WorkspacePath == children[1].WorkspacePath || children[0].BranchName == children[1].BranchName {
		t.Fatal("siblings share a writable lane")
	}
	for i, file := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(children[i].WorkspacePath, file), []byte(file+" integrated result\n"), 0600); err != nil {
			t.Fatal(err)
		}
		programFixtureGit(t, children[i].WorkspacePath, "add", file)
		programFixtureGit(t, children[i].WorkspacePath, "commit", "-m", "stage result")
		record.Jobs[i].ChildHead = programFixtureGit(t, children[i].WorkspacePath, "rev-parse", "HEAD")
	}
	record.Definition.Jobs = append(record.Definition.Jobs, pebblestore.TaskProgramJobSpec{ID: "design", StageID: "design", AgentType: "designer", OutputMode: "managed", DependsOn: []string{"stage-first.txt", "stage-second.txt"}})
	record.Jobs = append(record.Jobs, pebblestore.TaskProgramJobRecord{JobID: "design", StageID: "design", State: "declared"})
	record, _, err = svc.sessions.CreateTaskProgram(record)
	if err != nil {
		t.Fatal(err)
	}
	p := taskProgramScheduler{service: svc, parentSession: parent, record: record}
	// Capture the program's destination before refreshing the parent to its
	// successor. The real integration barrier must still advance only old work.
	if _, err := p.programWorkspacePath(); err != nil {
		t.Fatal(err)
	}
	successor, err := wt.AllocateTaskWorkspace(source, base, "stage-successor", nil)
	if err != nil {
		t.Fatal(err)
	}
	p.parentSession.WorktreeRootPath = successor.WorkspacePath
	p.parentSession.WorktreeBranch = successor.BranchName
	p.parentSession.Metadata = map[string]any{"swarm_v3_source_workspace_path": source, "swarm_v3_runtime_workspace_path": successor.WorkspacePath, "swarm_v3_worktree_base_commit": base.BaseCommit}
	if _, err := p.sourceHandoffsForJob(2); err == nil {
		t.Fatal("unintegrated source accepted")
	}
	if len(taskProgramReadyJobIndexes(p.record, 1)) != 0 {
		t.Fatal("Designer scheduled before barrier")
	}
	if err := p.integrateStage(0); err != nil {
		t.Fatal(err)
	}
	next, err := wt.ResolveTaskBase(lane.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if next.BaseCommit != p.record.ParentHead || next.BaseCommit == original || p.record.Jobs[0].State != "integrated" || p.record.Jobs[1].State != "integrated" {
		t.Fatalf("bad stage base: %+v", p.record)
	}
	if len(taskProgramReadyJobIndexes(p.record, 1)) != 1 {
		t.Fatal("Designer not unlocked by integrated dependencies")
	}
	evidence, err := p.sourceHandoffsForJob(2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first.txt integrated result", "second.txt integrated result", p.record.ParentHead, "Quoted untrusted"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("handoff missing %q: %s", want, evidence)
		}
	}
	stagePath, err := p.programWorkspacePath()
	if err != nil || stagePath != lane.WorkspacePath {
		t.Fatalf("successor retargeted later stage: %q %v", stagePath, err)
	}
	nextChild, err := wt.AllocateTaskWorkspace(stagePath, next, "next-child", []string{"first.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if programFixtureGit(t, nextChild.WorkspacePath, "rev-parse", "HEAD") != p.record.ParentHead {
		t.Fatal("next stage forked stale base")
	}
	for _, file := range []string{"first.txt", "second.txt"} {
		data, err := os.ReadFile(filepath.Join(nextChild.WorkspacePath, file))
		if err != nil || string(data) != file+" integrated result\n" {
			t.Fatalf("missing dependency bytes: %q %v", data, err)
		}
	}
	savedHead := p.record.ParentHead
	p.record.ParentHead = original
	if _, err := p.sourceHandoffsForJob(2); err == nil {
		t.Fatal("stale evidence accepted")
	}
	p.record.ParentHead = savedHead
	if err := os.WriteFile(filepath.Join(lane.WorkspacePath, "dirty.txt"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.sourceHandoffsForJob(2); err == nil {
		t.Fatal("dirty source accepted")
	}
	if data, _ := os.ReadFile(filepath.Join(lane.WorkspacePath, "dirty.txt")); string(data) != "preserve" {
		t.Fatal("dirty evidence mutated")
	}
	// Commit a large text prerequisite; bounded reader must reject, not truncate.
	if err := os.WriteFile(filepath.Join(lane.WorkspacePath, "large.txt"), []byte(strings.Repeat("x", 129*1024)), 0600); err != nil {
		t.Fatal(err)
	}
	programFixtureGit(t, lane.WorkspacePath, "add", "dirty.txt", "large.txt")
	programFixtureGit(t, lane.WorkspacePath, "commit", "-m", "oversized prerequisite")
	p.record.ParentHead = programFixtureGit(t, lane.WorkspacePath, "rev-parse", "HEAD")
	if _, err := p.sourceHandoffsForJob(2); err == nil || !strings.Contains(err.Error(), "bounded") {
		t.Fatalf("oversized evidence: %v", err)
	}
	// Remove the large content through a new fixture commit (no history reset)
	// and add binary content to prove binary evidence rejects independently.
	if err := os.WriteFile(filepath.Join(lane.WorkspacePath, "large.txt"), []byte("small\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.WorkspacePath, "binary.dat"), []byte{0, 1, 2, 3}, 0600); err != nil {
		t.Fatal(err)
	}
	programFixtureGit(t, lane.WorkspacePath, "add", "large.txt", "binary.dat")
	programFixtureGit(t, lane.WorkspacePath, "commit", "-m", "binary prerequisite")
	p.record.ParentHead = programFixtureGit(t, lane.WorkspacePath, "rev-parse", "HEAD")
	if _, err := p.sourceHandoffsForJob(2); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary evidence: %v", err)
	}
	if programFixtureGit(t, successor.WorkspacePath, "rev-parse", "HEAD") != original || programFixtureGit(t, successor.WorkspacePath, "status", "--porcelain") != "" {
		t.Fatal("stage integration advanced successor")
	}
	if programFixtureGit(t, source, "rev-parse", "HEAD") != original || programFixtureGit(t, source, "status", "--porcelain") != "" || programFixtureGit(t, unrelated, "worktree", "list", "--porcelain") != unrelatedBefore {
		t.Fatal("captured or unrelated checkout changed")
	}
}
