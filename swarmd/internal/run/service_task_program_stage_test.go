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
// dirty and stale evidence must reject without advancing checkouts. Coder
// consumers retain these guards but use their inherited source tree, not the
// managed Designer's inline size/binary gate (sourceHandoffsForJob).
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
	coderHandoff := func() (string, error) {
		saved := p.record.Definition.Jobs[2]
		p.record.Definition.Jobs[2].AgentType = "coder"
		p.record.Definition.Jobs[2].OutputMode = ""
		defer func() { p.record.Definition.Jobs[2] = saved }()
		return p.sourceHandoffsForJob(2)
	}
	assertCoderSource := func() {
		t.Helper()
		evidence, err := coderHandoff()
		if err != nil || !strings.Contains(evidence, p.record.ParentHead) || !strings.Contains(evidence, "allocated Coder worktree") || strings.Contains(evidence, "diff --git") || len(evidence) > 4096 {
			t.Fatalf("Coder must receive bounded exact identity without inline diff: %q %v", evidence, err)
		}
	}
	if _, err := coderHandoff(); err == nil {
		t.Fatal("Coder accepted unintegrated dependency")
	}
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
	nextChild, err := wt.AllocateTaskWorkspace(lane.WorkspacePath, next, "next-child", []string{"first.txt"})
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
	if _, err := coderHandoff(); err == nil {
		t.Fatal("Coder accepted stale lane")
	}
	p.record.ParentHead = savedHead
	if err := os.WriteFile(filepath.Join(lane.WorkspacePath, "dirty.txt"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.sourceHandoffsForJob(2); err == nil {
		t.Fatal("dirty source accepted")
	}
	if _, err := coderHandoff(); err == nil {
		t.Fatal("Coder accepted dirty lane")
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
	assertCoderSource()
	largeBase, err := wt.ResolveTaskBase(lane.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	largeChild, err := wt.AllocateTaskWorkspace(lane.WorkspacePath, largeBase, "large-next-child", []string{"first.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(largeChild.WorkspacePath, "large.txt")); err != nil || string(data) != strings.Repeat("x", 129*1024) {
		t.Fatalf("large prerequisite missing outside mutation scope: %d bytes, %v", len(data), err)
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
	assertCoderSource()
	if programFixtureGit(t, source, "rev-parse", "HEAD") != original || programFixtureGit(t, source, "status", "--porcelain") != "" || programFixtureGit(t, unrelated, "worktree", "list", "--porcelain") != unrelatedBefore {
		t.Fatal("captured or unrelated checkout changed")
	}
}
