package run

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	worktree "swarm/packages/swarmd/internal/worktree"
)

// Purpose: multi-repository admission must reject aliases of one actual Git
// repository before allocating any lane. ResolveTaskBase, not lexical path
// comparison, owns this boundary.
func TestMultiRepositoryRejectsGitRootAliasBeforeAllocation(t *testing.T) {
	repo := programFixtureRepo(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	svc := &Service{worktrees: &worktree.Service{}}
	p := &taskProgramScheduler{service: svc, record: pebblestore.TaskProgramRecord{Definition: pebblestore.TaskProgramDefinition{Jobs: []pebblestore.TaskProgramJobSpec{{ID: "one", AgentType: "coder", WorkspacePath: repo}, {ID: "two", AgentType: "coder", WorkspacePath: alias}}}}}
	before := programFixtureGit(t, repo, "worktree", "list", "--porcelain")
	if _, err := p.multiRepositoryWorkspacePath(); err == nil {
		t.Fatal("accepted unbound source aliases")
	}
	if got := programFixtureGit(t, repo, "worktree", "list", "--porcelain"); got != before {
		t.Fatal("denial allocated a worktree")
	}
}

// Purpose: revocation or rebinding of a granted canonical workspace generation
// must reject lane reuse without changing the integration destination.
func TestTaskProgramCanonicalSourceGenerationRejectsRebinding(t *testing.T) {
	repo := programFixtureRepo(t)
	parent := pebblestore.SessionSnapshot{ID: "parent", UserID: "user", AccountScopeID: "account", WorkspacePath: repo, WorkspaceGrants: []pebblestore.WorkspaceGrant{{WorkspaceID: "repo", WorkspaceGeneration: 1, Path: repo}}}
	svc := &Service{}
	svc.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		return SessionWorkspaceCanonicalization{WorkspaceID: input.WorkspaceID, WorkspaceGeneration: 2, WorkspaceState: "active", WorkspaceName: "repo", SourceWorkspacePath: repo, RuntimeWorkspacePath: repo, WorkspaceBindingID: "binding", RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1}, nil
	})
	p := &taskProgramScheduler{service: svc, parentSession: parent}
	if _, _, err := p.canonicalRepositorySource(repo); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("stale workspace generation: %v", err)
	}
}

// Purpose: a multi-repository Coder without a pinned source must never route
// through the parent's primary checkout.
func TestMultiRepositoryCoderSourceRequiresExplicitTarget(t *testing.T) {
	p := &taskProgramScheduler{record: pebblestore.TaskProgramRecord{RepositoryLanes: map[string]pebblestore.TaskProgramRepositoryLane{"repo": {SourcePath: "repo"}}}}
	_, err := p.coderSourceForJob(pebblestore.TaskProgramJobSpec{ID: "build"})
	if err == nil || !strings.Contains(err.Error(), "explicit workspace_path") {
		t.Fatalf("missing target err=%v", err)
	}
}

func TestMultiRepositoryIntegrationRequiresWorktreeAuthority(t *testing.T) {
	p := &taskProgramScheduler{service: &Service{}, record: pebblestore.TaskProgramRecord{Definition: pebblestore.TaskProgramDefinition{Stages: []pebblestore.TaskProgramStageSpec{{ID: "build"}}}}}
	if err := p.integrateMultiRepositoryStage(0); err == nil || !strings.Contains(err.Error(), "canonical task integration") {
		t.Fatalf("integration without authority err=%v", err)
	}
}

// The fixture uses two unrelated real Git repositories and a temporary Pebble
// store. Canonical grants, lane allocation, child commits, and integration all
// go through production authorities; no provider or daemon is required.
func multiRepoProgramFixture(t *testing.T, dependent bool) (*taskProgramScheduler, [2]string, [2]string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc, id, cleanup := newTaskLaunchPermissionTestService(t)
	t.Cleanup(cleanup)
	parent, ok, err := svc.sessions.GetSession(id)
	if err != nil || !ok {
		t.Fatalf("parent: %v", err)
	}
	sources := [2]string{programFixtureRepo(t), programFixtureRepo(t)}
	bases := [2]string{programFixtureGit(t, sources[0], "rev-parse", "HEAD"), programFixtureGit(t, sources[1], "rev-parse", "HEAD")}
	parent.WorkspacePath = sources[0]
	parent.TemporaryWorkspaceRoots = sources[:]
	catalog := pebblestore.NewWorkspaceStore(svc.sessions.Store().Underlying())
	entries := make([]pebblestore.WorkspaceEntry, 0, 2)
	for _, source := range sources {
		entry, err := catalog.AddForAccount(parent.AccountScopeID, source, "Fixture")
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
		parent.WorkspaceGrants = append(parent.WorkspaceGrants, pebblestore.WorkspaceGrant{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: source})
	}
	svc.SetSessionWorkspaceCanonicalizer(func(input SessionWorkspaceCanonicalizeInput) (SessionWorkspaceCanonicalization, error) {
		for i, source := range sources {
			if input.WorkspaceID == entries[i].WorkspaceID {
				return SessionWorkspaceCanonicalization{WorkspaceID: input.WorkspaceID, WorkspaceGeneration: 1, WorkspaceState: "active", WorkspaceName: input.WorkspaceID, SourceWorkspacePath: source, RuntimeWorkspacePath: source, WorkspaceBindingID: "binding", RuntimeSwarmID: "swarm", PlacementGeneration: 1, BindingGeneration: 1}, nil
			}
		}
		return SessionWorkspaceCanonicalization{}, errors.New("unknown canonical workspace")
	})
	svc.worktrees = worktree.NewService(pebblestore.NewWorktreeStore(svc.sessions.Store().Underlying()), workspace.NewService(catalog), nil)
	// Production accepted-plan parents already own a worktree of repository A.
	// Exercise source resolution through that authenticated runtime redirect.
	base, err := svc.worktrees.ResolveTaskBase(sources[0])
	if err != nil {
		t.Fatal(err)
	}
	owned, err := svc.worktrees.(*worktree.Service).AllocateProjectTaskFollowup(identity.Principal{Type: "user", UserID: parent.UserID, AccountScopeID: parent.AccountScopeID}, sources[0], parent.ID, "agent/accepted-parent", base.BaseCommit, base.ParentBranch)
	if err != nil {
		t.Fatal(err)
	}
	parent.WorktreeEnabled = true
	parent.WorktreeRootPath, parent.WorkspacePath = owned.WorkspacePath, owned.WorkspacePath
	parent.WorktreeBranch, parent.WorktreeBaseBranch = owned.BranchName, base.ParentBranch
	parent.Metadata = map[string]any{"swarm_v3_source_workspace_path": sources[0], "swarm_v3_source_workspace_id": entries[0].WorkspaceID, "swarm_v3_source_workspace_generation": entries[0].WorkspaceGeneration, "swarm_v3_runtime_workspace_path": owned.WorkspacePath, "swarm_v3_worktree_base_commit": base.BaseCommit, "swarm_v3_worktree_owner_session_id": parent.ID}
	stages := []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "ready"}}
	jobs := []pebblestore.TaskProgramJobSpec{{ID: "job-a", StageID: "build", AgentType: "coder", WorkspacePath: sources[0], OwnedScope: []string{"a.txt"}}, {ID: "job-b", StageID: "build", AgentType: "coder", WorkspacePath: sources[1], OwnedScope: []string{"b.txt"}}}
	if dependent {
		stages = append(stages, pebblestore.TaskProgramStageSpec{ID: "follow", DependsOn: []string{"build"}, DependencyEvidence: "integrated"})
		jobs[1].StageID, jobs[1].DependsOn = "follow", []string{"job-a"}
	}
	record := pebblestore.TaskProgramRecord{ParentSessionID: id, ProgramID: "multi-git", ReservationRunID: "run", DefinitionHash: "fixture-hash", State: pebblestore.TaskProgramStateRunning, ActiveStageID: stages[0].ID, Definition: pebblestore.TaskProgramDefinition{ID: "multi-git", Stages: stages, Jobs: jobs}}
	for _, job := range jobs {
		record.Jobs = append(record.Jobs, pebblestore.TaskProgramJobRecord{JobID: job.ID, StageID: job.StageID, State: pebblestore.TaskProgramJobDeclared})
	}
	p := &taskProgramScheduler{service: svc, parentSession: parent, record: record}
	if _, err := p.multiRepositoryWorkspacePath(); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	record, _, err = svc.sessions.CreateTaskProgram(record)
	if err != nil {
		t.Fatalf("persist program: %v", err)
	}
	p.record = record
	if _, err := p.multiRepositoryWorkspacePath(); err != nil {
		t.Fatalf("allocate lanes: %v", err)
	}
	for i, source := range sources {
		lane := p.record.RepositoryLanes[source]
		if lane.SourcePath != source || lane.WorkspacePath == source || lane.BaseCommit != bases[i] {
			t.Fatalf("incorrect lane for %s: %+v", source, lane)
		}
	}
	return p, sources, bases
}

func multiRepoCommitChild(t *testing.T, p *taskProgramScheduler, source, jobID, file string) string {
	t.Helper()
	lane := p.record.RepositoryLanes[source]
	base, err := p.service.worktrees.ResolveTaskBase(lane.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	child, err := p.service.worktrees.AllocateTaskWorkspace(lane.WorkspacePath, base, jobID, []string{file})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child.WorkspacePath, file), []byte(jobID+" result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	programFixtureGit(t, child.WorkspacePath, "add", file)
	programFixtureGit(t, child.WorkspacePath, "commit", "-m", jobID)
	head := programFixtureGit(t, child.WorkspacePath, "rev-parse", "HEAD")
	index := taskProgramJobIndex(p.record, jobID)
	update := pebblestore.TaskProgramJobTransition{JobID: jobID, ExpectedState: pebblestore.TaskProgramJobDeclared, State: pebblestore.TaskProgramJobHandoffReady, ChildSessionID: jobID, CurrentSessionID: jobID, CurrentRunID: "run-" + jobID, CurrentGeneration: 1, SourceWorkspacePath: source, WorkspacePath: child.WorkspacePath, WorktreeBranch: child.BranchName, ParentBranch: lane.Branch, ImmutableStageBase: base.BaseCommit, ChildHead: head}
	if index < 0 {
		t.Fatalf("missing job %q", jobID)
	}
	if _, _, err := p.transition(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: "handoff:" + jobID, Jobs: []pebblestore.TaskProgramJobTransition{update}}); err != nil {
		t.Fatalf("persist handoff: %v", err)
	}
	return head
}

// Purpose: independent Coders in different canonical repositories must produce
// isolated child commits and integrate only into their respective durable lane;
// captured source checkouts must remain unchanged (integrateMultiRepositoryStage).
func TestMultiRepositoryStageIntegratesOnlyOwnLane(t *testing.T) {
	p, sources, bases := multiRepoProgramFixture(t, false)
	multiRepoCommitChild(t, p, sources[0], "job-a", "a.txt")
	multiRepoCommitChild(t, p, sources[1], "job-b", "b.txt")
	if err := p.integrateMultiRepositoryStage(0); err != nil {
		t.Fatal(err)
	}
	for i, source := range sources {
		lane := p.record.RepositoryLanes[source]
		head := programFixtureGit(t, lane.WorkspacePath, "rev-parse", "HEAD")
		if head == bases[i] || p.record.LaneHeads[source] != head || programFixtureGit(t, source, "rev-parse", "HEAD") != bases[i] {
			t.Fatalf("lane %d did not advance independently: %+v", i, p.record.LaneHeads)
		}
		own, other := "a.txt", "b.txt"
		if i == 1 {
			own, other = other, own
		}
		if _, err := os.Stat(filepath.Join(lane.WorkspacePath, own)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(lane.WorkspacePath, other)); !os.IsNotExist(err) {
			t.Fatalf("cross-repository commit imported to lane %d: %v", i, err)
		}
		if got := programFixtureGit(t, lane.WorkspacePath, "rev-list", "--count", bases[i]+"..HEAD"); got != "1" {
			t.Fatalf("lane %d contains %s commits, expected one", i, got)
		}
	}
	for _, job := range p.record.Jobs {
		if job.State != pebblestore.TaskProgramJobIntegrated {
			t.Fatalf("job not integrated: %+v", job)
		}
	}
}

// Purpose: a dependent Coder in repository B receives bounded quoted evidence
// from A, but inherits B's own integration lane, not A's Git ancestry. The
// stage barrier and sourceHandoffsForJob are the narrowest production boundary.
func TestMultiRepositoryDependentStageKeepsRepositoryHistorySeparate(t *testing.T) {
	p, sources, bases := multiRepoProgramFixture(t, true)
	multiRepoCommitChild(t, p, sources[0], "job-a", "a.txt")
	if err := p.integrateMultiRepositoryStage(0); err != nil {
		t.Fatal(err)
	}
	if p.record.Jobs[0].State != pebblestore.TaskProgramJobIntegrated || p.record.Jobs[1].State != pebblestore.TaskProgramJobDeclared {
		t.Fatalf("stage barrier changed unrelated job: %+v", p.record.Jobs)
	}
	if err := p.advanceStage(1); err != nil {
		t.Fatal(err)
	}
	evidence, err := p.sourceHandoffsForJob(1)
	if err != nil || !strings.Contains(evidence, "Quoted untrusted") || !strings.Contains(evidence, "a.txt") {
		t.Fatalf("cross-repo dependency evidence: %q %v", evidence, err)
	}
	laneB := p.record.RepositoryLanes[sources[1]]
	baseB, err := p.service.worktrees.ResolveTaskBase(laneB.WorkspacePath)
	if err != nil || baseB.BaseCommit != bases[1] {
		t.Fatalf("B based on A instead of B: %+v %v", baseB, err)
	}
	if programFixtureGit(t, laneB.WorkspacePath, "merge-base", bases[1], "HEAD") != bases[1] {
		t.Fatal("B ancestry changed before its Coder")
	}
	if _, err := os.Stat(filepath.Join(laneB.WorkspacePath, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("A files imported into B: %v", err)
	}
	multiRepoCommitChild(t, p, sources[1], "job-b", "b.txt")
	if err := p.integrateMultiRepositoryStage(1); err != nil {
		t.Fatal(err)
	}
	if p.record.Jobs[1].State != pebblestore.TaskProgramJobIntegrated {
		t.Fatalf("B not integrated: %+v", p.record.Jobs[1])
	}
	if got := programFixtureGit(t, laneB.WorkspacePath, "rev-list", "--count", bases[1]+"..HEAD"); got != "1" {
		t.Fatalf("B imported foreign history: %s commits", got)
	}
	for i, source := range sources {
		if programFixtureGit(t, source, "rev-parse", "HEAD") != bases[i] {
			t.Fatalf("source %d advanced", i)
		}
	}
}

type failSecondLaneIntegration struct {
	*worktree.Service
	failPath string
	attempts map[string]int
}

func (w *failSecondLaneIntegration) ApplyTaskIntegration(path string, plan worktree.TaskIntegrationPlan) (worktree.TaskIntegrationResult, error) {
	w.attempts[path]++
	if path == w.failPath && w.attempts[path] == 1 {
		return worktree.TaskIntegrationResult{}, errors.New("injected second-lane integration failure")
	}
	return w.Service.ApplyTaskIntegration(path, plan)
}

// Purpose: a failure applying B after A's durable receipt must not replay A
// when the stage is reopened from Pebble. The exact receipt, Git head, and
// integrated job state together fence repeated ApplyTaskIntegration calls.
func TestMultiRepositoryIntegrationRetrySkipsDurableFirstLane(t *testing.T) {
	p, sources, bases := multiRepoProgramFixture(t, false)
	multiRepoCommitChild(t, p, sources[0], "job-a", "a.txt")
	multiRepoCommitChild(t, p, sources[1], "job-b", "b.txt")
	laneA, laneB := p.record.RepositoryLanes[sources[0]], p.record.RepositoryLanes[sources[1]]
	injected := &failSecondLaneIntegration{Service: &worktree.Service{}, failPath: laneB.WorkspacePath, attempts: map[string]int{}}
	p.service.worktrees = injected
	if err := p.integrateMultiRepositoryStage(0); err == nil || !strings.Contains(err.Error(), "injected second-lane") {
		t.Fatalf("expected second-lane failure: %v", err)
	}
	firstHead := programFixtureGit(t, laneA.WorkspacePath, "rev-parse", "HEAD")
	stored, ok, err := p.service.sessions.GetTaskProgram(p.parentSession.ID, p.record.ProgramID)
	if err != nil || !ok || stored.LaneHeads[sources[0]] != firstHead || stored.Jobs[0].State != pebblestore.TaskProgramJobIntegrated || stored.Jobs[1].State != pebblestore.TaskProgramJobHandoffReady {
		t.Fatalf("first receipt missing: %+v %v", stored, err)
	}
	// Reconstruct the scheduler from durable state as recovery does, without
	// resetting the failed lane or manufacturing a second program.
	reopened := &taskProgramScheduler{service: p.service, parentSession: p.parentSession, record: stored}
	if err := reopened.integrateMultiRepositoryStage(0); err != nil {
		t.Fatal(err)
	}
	if injected.attempts[laneA.WorkspacePath] != 1 || injected.attempts[laneB.WorkspacePath] != 2 {
		t.Fatalf("replayed first lane: %+v", injected.attempts)
	}
	if got := programFixtureGit(t, laneA.WorkspacePath, "rev-parse", "HEAD"); got != firstHead {
		t.Fatalf("replayed first lane changed head: %s", got)
	}
	for i, source := range sources {
		lane := reopened.record.RepositoryLanes[source]
		if got := programFixtureGit(t, lane.WorkspacePath, "rev-list", "--count", bases[i]+"..HEAD"); got != "1" {
			t.Fatalf("lane %d replayed %s commits", i, got)
		}
		if programFixtureGit(t, source, "rev-parse", "HEAD") != bases[i] {
			t.Fatalf("source %d advanced", i)
		}
	}
	for _, job := range reopened.record.Jobs {
		if job.State != pebblestore.TaskProgramJobIntegrated {
			t.Fatalf("lost integrated job: %+v", job)
		}
	}
}
