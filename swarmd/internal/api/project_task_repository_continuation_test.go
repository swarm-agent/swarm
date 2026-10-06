package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	worktree "swarm/packages/swarmd/internal/worktree"
)

// Purpose: ReopenProjectTask must recover both actual integrated repository
// results when the coordinator has no delta and a later no-op attempt exists.
// Real Git integration, Pebble reservation/restart and V3 admission are the
// narrow joined layer proving exact commits, isolation and retry postconditions;
// this is not provider-backed E2E or validation of the retained implementation.
func TestProjectTaskRepositoryContinuationAcrossNoopAndRestart(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	bases := []string{followupSourceGit(t, repos[0], "rev-parse", "HEAD"), followupSourceGit(t, repos[1], "rev-parse", "HEAD")}
	program := retainContinuationProgram(t, f, task, repos[:2], "useful", "sentinel")
	// Reserve a historical no-op using the existing store path: no new code and
	// no program. This reproduces an intervening pre-fix attempt, not a new grant.
	noop, err := f.server.sessions.Store().ReserveTaskFollowup(p.AccountScopeID, project.ID, task.ID, p.UserID, "noop", "inspect only", task.Revision, 100)
	if err != nil { t.Fatal(err) }
	if _, err := f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, project.ID, task.ID, func(current *pebblestore.ProjectTaskRecord) error { current.Status = "failed"; return nil }); err != nil { t.Fatal(err) }
	current, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if current.SessionID != noop.SessionID { t.Fatal("no-op identity lost") }
	refs, err := f.server.resolveTaskRepositoryContinuations(p, current, current.ProgramSources)
	if err != nil || len(refs) != 2 { t.Fatalf("retained selection: %+v %v", refs, err) }
	for _, ref := range refs { if ref.HeadCommit != program.LaneHeads[ref.Source.Path] || ref.SessionID != task.SessionID { t.Fatal("selected coordinator or no-op instead of useful result") } }
	// Stop after primary allocation but before secondary allocation/session.
	// The pinned results and partial allocation must survive instead of altering
	f.server.v3SessionExecutor = nil
	// The existing execution binding is retained rather than altering
	// topology authority or synthesizing launch success.
	f.server.worktrees = &failSecondaryContinuationAllocator{Service: f.server.worktrees.(*worktree.Service)}
	req := tool.ProjectTaskFollowupInput{ClientRequestID: "correct-results", Feedback: "Correct retained implementation", Revision: current.Revision}
	_, err = f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, req)
	if err == nil || !strings.Contains(err.Error(), "injected allocation failure") { t.Fatalf("allocation failure: %v", err) }
	reserved, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !reflect.DeepEqual(refs, reserved.ActiveAttempt().RepositoryContinuations) { t.Fatal("reservation lost exact results") }
	forged := append([]pebblestore.ProjectTaskRepositoryContinuation(nil), refs...)
	forged[0].TargetHead = forged[0].HeadCommit
	if _, err := f.server.sessions.Store().ReserveTaskFollowupWithRepositories(p.AccountScopeID, project.ID, task.ID, p.UserID, req.ClientRequestID, req.Feedback, req.Revision, 101, nil, forged, reserved.OriginSessionID); err == nil { t.Fatal("same-key changed pinned evidence accepted") }
	unchanged, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !reflect.DeepEqual(reserved, unchanged) { t.Fatal("changed-evidence rejection mutated reservation") }
	for _, repo := range repos[:2] { followupSourceGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "source advance") }
	if err := f.db.Close(); err != nil { t.Fatal(err) }
	f.db, err = pebblestore.Open(f.dir)
	if err != nil { t.Fatal(err) }
	el, err := pebblestore.NewEventLog(f.db)
	if err != nil { t.Fatal(err) }
	f.server.sessions = sessionruntime.NewService(pebblestore.NewSessionStore(f.db), el)
	f.server.agents = agent.NewService(pebblestore.NewAgentStore(f.db), el)
	f.server.model = model.NewService(pebblestore.NewModelStore(f.db), el, nil)
	f.server.agentModelSettings = agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(f.db))
	f.server.planLifecycle = sessionruntime.NewPlanLifecycleService(f.server.sessions)
	f.server.planLifecycle.SetApplySessionMutation(f.server.applySessionV3PrimaryMutation)
	f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(f.db))
	f.server.worktrees = worktree.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
	// Missing executor stops before a provider can run; the real session and
	// worktrees must already have been created from the retained exact heads.
	_, err = f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, req)
	if err == nil || !strings.Contains(err.Error(), "executor") { t.Fatalf("retry: %v", err) }
	created, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	owner, found, err := f.server.sessions.Store().GetSession(created.SessionID)
	if err != nil || !found { t.Fatalf("session: %v", err) }
	assertContinuationLanes(t, f, owner, refs)
	before := []string{followupSourceGit(t, repos[0], "worktree", "list", "--porcelain"), followupSourceGit(t, repos[1], "worktree", "list", "--porcelain")}
	_, err = f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, req)
	if err == nil || !strings.Contains(err.Error(), "executor") { t.Fatalf("same-key retry: %v", err) }
	after, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if after.SessionID != created.SessionID || len(after.Attempts) != 3 || !reflect.DeepEqual(after.ActiveAttempt().RepositoryContinuations, refs) { t.Fatal("retry duplicated or rebound attempt") }
	intents, err := f.server.sessions.Store().ListRunIntents(owner.ID, 10)
	if err != nil || len(intents) != 1 { t.Fatalf("duplicate run: %+v %v", intents, err) }
	for i, repo := range repos[:2] {
		if followupSourceGit(t, repo, "worktree", "list", "--porcelain") != before[i] || followupSourceGit(t, repo, "status", "--porcelain") != "" || followupSourceGit(t, repo, "rev-parse", "HEAD") == bases[i] { t.Fatal("retry changed source or duplicated lane") }
		if _, err := os.Stat(filepath.Join(repo, fmt.Sprintf("sentinel-%d.txt", i))); !os.IsNotExist(err) { t.Fatal("retained code promoted into source") }
	}
}

func retainContinuationProgram(t *testing.T, f *matrixTestFixture, task *pebblestore.ProjectTaskRecord, repos []string, id, prefix string) pebblestore.TaskProgramRecord {
	t.Helper()
	trees := f.server.worktrees.(*worktree.Service)
	definition := pebblestore.TaskProgramDefinition{ID: id, Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "committed source"}}}
	record := pebblestore.TaskProgramRecord{ParentSessionID: task.SessionID, ProgramID: id, DefinitionHash: id, ReservationRunID: task.ExecutionRunID(), State: pebblestore.TaskProgramStateRunning, RepositoryLanes: map[string]pebblestore.TaskProgramRepositoryLane{}, LaneHeads: map[string]string{}}
	for i, repo := range repos {
		followupSourceGit(t, repo, "config", "user.name", "Fixture")
		followupSourceGit(t, repo, "config", "user.email", "fixture@example.invalid")
		base, err := trees.ResolveTaskBase(repo)
		if err != nil { t.Fatal(err) }
		digest := sha256.Sum256([]byte(task.SessionID + "\x00" + repo))
		seed := "program-lane-" + hex.EncodeToString(digest[:12])
		lane, err := trees.AllocateTaskWorkspace(repo, base, seed, nil)
		if err != nil { t.Fatal(err) }
		jobID := fmt.Sprintf("%s-job-%d", id, i)
		file := fmt.Sprintf("%s-%d.txt", prefix, i)
		laneBase, err := trees.ResolveTaskBase(lane.WorkspacePath)
		if err != nil { t.Fatal(err) }
		child, err := trees.AllocateTaskWorkspace(lane.WorkspacePath, laneBase, jobID, []string{file})
		if err != nil { t.Fatal(err) }
		if err := os.WriteFile(filepath.Join(child.WorkspacePath, file), []byte(jobID), 0600); err != nil { t.Fatal(err) }
		followupSourceGit(t, child.WorkspacePath, "add", file)
		followupSourceGit(t, child.WorkspacePath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "sentinel result")
		head := followupSourceGit(t, child.WorkspacePath, "rev-parse", "HEAD")
		plan, err := trees.PrepareTaskIntegration(lane.WorkspacePath, lane.BranchName, base.BaseCommit, []worktree.TaskIntegrationChild{{SessionID: jobID, BaseCommit: base.BaseCommit, HeadCommit: head, OwnedScopes: []string{file}}})
		if err != nil { t.Fatal(err) }
		result, err := trees.ApplyTaskIntegration(lane.WorkspacePath, plan)
		if err != nil { t.Fatal(err) }
		var source pebblestore.ProjectTaskSource
		for _, candidate := range task.ProgramSources { if candidate.Path == repo { source = candidate } }
		record.RepositoryLanes[repo] = pebblestore.TaskProgramRepositoryLane{WorkspaceID: source.WorkspaceID, WorkspaceGeneration: source.WorkspaceGeneration, SourcePath: repo, WorkspacePath: lane.WorkspacePath, Branch: lane.BranchName, BaseCommit: base.BaseCommit}
		record.LaneHeads[repo] = result.ResultingParentHead
		definition.Jobs = append(definition.Jobs, pebblestore.TaskProgramJobSpec{ID: jobID, StageID: "build", AgentType: "coder", WorkspacePath: repo, Title: "Fixture", MetaPrompt: "Fixture", Deliverable: "Commit", OwnedScope: []string{file}, AcceptanceCriteria: []string{"sentinel"}, DependencyEvidence: "committed"})
		record.Jobs = append(record.Jobs, pebblestore.TaskProgramJobRecord{JobID: jobID, StageID: "build", State: pebblestore.TaskProgramJobIntegrated, IntegrationState: "integrated", SourceWorkspacePath: repo, ChildSessionID: jobID, ChildHead: head, ImmutableStageBase: base.BaseCommit, ParentBranch: lane.BranchName})
	}
	record.Definition = definition
	record.State = pebblestore.TaskProgramStateCompleted
	record, _, err := f.server.sessions.Store().CreateTaskProgram(record)
	if err != nil { t.Fatal(err) }
	return record
}

func assertContinuationLanes(t *testing.T, f *matrixTestFixture, owner pebblestore.SessionSnapshot, refs []pebblestore.ProjectTaskRepositoryContinuation) {
	t.Helper()
	for _, ref := range refs {
		path := owner.WorktreeRootPath
		if ref.Source.Path != owner.Metadata["swarm_v3_source_workspace_path"] {
			path = ""
			rows, _ := owner.Metadata["swarm_v3_worktree_history"].([]any)
			for _, row := range rows { item := row.(map[string]any); if item["source_workspace_path"] == ref.Source.Path { path, _ = item["path"].(string) } }
		}
		if path == "" || path == ref.Lane.WorkspacePath || followupSourceGit(t, path, "rev-parse", "HEAD") != ref.HeadCommit { t.Fatal("correction lane did not capture exact useful result") }
		if err := f.server.worktrees.(*worktree.Service).ValidateSessionRepositoryLane(ref.Source.Path, path, owner.ID, followupSourceGit(t, path, "branch", "--show-current")); err != nil { t.Fatal(err) }
		// A real downstream child allocation from each correction lane must carry
		// its sentinel, not the current source checkout or old coordinator.
		base, err := f.server.worktrees.(*worktree.Service).ResolveTaskBase(path)
		if err != nil || base.BaseCommit != ref.HeadCommit { t.Fatalf("launch base: %+v %v", base, err) }
		child, err := f.server.worktrees.(*worktree.Service).AllocateTaskWorkspace(path, base, "assert-child-"+ref.Source.WorkspaceID, nil)
		if err != nil { t.Fatal(err) }
		if followupSourceGit(t, child.WorkspacePath, "rev-parse", "HEAD") != ref.HeadCommit { t.Fatal("child lost retained base") }
		files, err := filepath.Glob(filepath.Join(child.WorkspacePath, "sentinel-*.txt"))
		if err != nil || len(files) != 1 { t.Fatal("child missing repository-specific sentinel") }
		if bytes, err := os.ReadFile(files[0]); err != nil || len(bytes) == 0 { t.Fatal("child sentinel content missing") }
	}
	if _, err := f.server.sessions.Store().TaskRepositoryContinuationsForSession(owner); err != nil { t.Fatal(err) }
}

// Purpose: retained-result authentication rejects forged lineage, catalog and
// Git facts before reservation/allocation. The real joined store/Git boundary
// proves rejection AND unchanged worktrees/task; dirty data is never removed.
func TestProjectTaskRepositoryContinuationRejectsInvalidEvidence(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	retainContinuationProgram(t, f, task, repos[:2], "useful", "sentinel")
	refs, err := f.server.resolveTaskRepositoryContinuations(p, task, task.ProgramSources)
	if err != nil { t.Fatal(err) }
	missing := refs[0]; missing.Lane.WorkspacePath = filepath.Join(t.TempDir(), "missing")
	if err := f.server.validateTaskRepositoryContinuation(p, task, missing); err == nil { t.Fatal("missing lane accepted") }
	before := followupSourceGit(t, repos[0], "worktree", "list", "--porcelain")
	for _, mutate := range []func(*pebblestore.ProjectTaskRepositoryContinuation){
		func(r *pebblestore.ProjectTaskRepositoryContinuation) { r.AttemptID = "unrelated" },
		func(r *pebblestore.ProjectTaskRepositoryContinuation) { r.SessionID = "foreign" },
		func(r *pebblestore.ProjectTaskRepositoryContinuation) { r.ProgramID = "foreign" },
		func(r *pebblestore.ProjectTaskRepositoryContinuation) { r.ProgramRevision++ },
		func(r *pebblestore.ProjectTaskRepositoryContinuation) { r.Source.WorkspaceGeneration++ },
		func(r *pebblestore.ProjectTaskRepositoryContinuation) { r.Lane.WorkspacePath = repos[1] },
		func(r *pebblestore.ProjectTaskRepositoryContinuation) { r.HeadCommit = r.Lane.BaseCommit },
	} {
		ref := refs[0]; mutate(&ref)
		if err := f.server.validateTaskRepositoryContinuation(p, task, ref); err == nil { t.Fatal("forged evidence accepted") }
	}
	foreign := p; foreign.AccountScopeID = "foreign"
	if err := f.server.validateTaskRepositoryContinuation(foreign, task, refs[0]); err == nil { t.Fatal("foreign account accepted") }
	other := *task; other.ID = "other-task"
	if err := f.server.validateTaskRepositoryContinuation(p, &other, refs[0]); err == nil { t.Fatal("foreign task accepted") }
	dirty := filepath.Join(refs[0].Lane.WorkspacePath, "unfinished")
	if err := os.WriteFile(dirty, []byte("preserve"), 0600); err != nil { t.Fatal(err) }
	_, err = f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "dirty", Feedback: "continue", Revision: task.Revision})
	if err == nil { t.Fatal("dirty lane reopened") }
	after, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !reflect.DeepEqual(task, after) || followupSourceGit(t, repos[0], "worktree", "list", "--porcelain") != before { t.Fatal("rejection mutated task or allocated") }
	if bytes, err := os.ReadFile(dirty); err != nil || string(bytes) != "preserve" { t.Fatal("rejection destroyed dirty work") }
}

// Purpose: two authenticated useful same-task histories with different result
// heads are not ordered by timestamp or latest session. Resolver + real Git/V3
// fixtures prove ambiguity rejects before reservation and preserves both results.
func TestProjectTaskRepositoryContinuationRejectsAmbiguousHistory(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	first := retainContinuationProgram(t, f, task, repos[:2], "first", "first-result")
	next, err := f.server.sessions.Store().ReserveTaskFollowup(p.AccountScopeID, project.ID, task.ID, p.UserID, "historical-retry", "historical work", task.Revision, 100)
	if err != nil { t.Fatal(err) }
	for _, repo := range repos[:2] {
		base := followupSourceGit(t, repo, "rev-parse", "HEAD")
		_, err = f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, project.ID, task.ID, func(current *pebblestore.ProjectTaskRecord) error {
			if repo == repos[0] { current.BaseBranch = "dev"; current.BaseCommit = base; current.ActiveAttempt().AllocationHead = base }
			return nil
		})
		if err != nil { t.Fatal(err) }
	}
	next, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	f.server.v3SessionExecutor = nil
	if err := f.server.deployProjectTaskExecution(p, project, next, "in_progress", "historical work"); err == nil || !strings.Contains(err.Error(), "executor") { t.Fatalf("historical allocation: %v", err) }
	if err := f.server.sessions.Store().PutProjectTask(p.AccountScopeID, next); err != nil { t.Fatal(err) }
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	next, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	second := retainContinuationProgram(t, f, next, repos[:2], "second", "second-result")
	_, err = f.server.resolveTaskRepositoryContinuations(p, next, next.ProgramSources)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") { t.Fatalf("conflicting useful history: %v", err) }
	after, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !reflect.DeepEqual(next, after) { t.Fatal("ambiguous read changed task") }
	for _, record := range []pebblestore.TaskProgramRecord{first, second} {
		for source, lane := range record.RepositoryLanes {
			if followupSourceGit(t, lane.WorkspacePath, "rev-parse", "HEAD") != record.LaneHeads[source] { t.Fatal("ambiguity destroyed useful result") }
		}
	}
}

// Purpose: the durable integrated head is immutable even when its old lane later
// receives another real commit. API admission + Git is the narrow authority;
// rejecting stale provenance must retain the new commit and create no session.
func TestProjectTaskRepositoryContinuationRejectsMovedLaneHead(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	program := retainContinuationProgram(t, f, task, repos[:2], "useful", "sentinel")
	lane := program.RepositoryLanes[repos[0]]
	followupSourceGit(t, lane.WorkspacePath, "commit", "--allow-empty", "-m", "unrecorded lane result")
	head := followupSourceGit(t, lane.WorkspacePath, "rev-parse", "HEAD")
	before := followupSourceGit(t, repos[0], "worktree", "list", "--porcelain")
	_, err := f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "moved", Feedback: "continue", Revision: task.Revision})
	if err == nil { t.Fatal("unrecorded head accepted") }
	after, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !reflect.DeepEqual(task, after) || followupSourceGit(t, repos[0], "worktree", "list", "--porcelain") != before || followupSourceGit(t, lane.WorkspacePath, "rev-parse", "HEAD") != head { t.Fatal("rejection changed task or destroyed moved head") }
}

// Fault injection only: allocations/Git remain real; no provider outcome is faked.
type failSecondaryContinuationAllocator struct { *worktree.Service }

func (s *failSecondaryContinuationAllocator) AllocateProjectTaskFollowup(p identity.Principal, source, owner, branch, head, target string) (worktree.Allocation, error) {
	if strings.HasPrefix(branch, "agent/followup-repository-") { return worktree.Allocation{}, errors.New("injected allocation failure") }
	return s.Service.AllocateProjectTaskFollowup(p, source, owner, branch, head, target)
}
