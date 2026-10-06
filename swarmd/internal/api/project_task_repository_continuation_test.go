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
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	worktree "swarm/packages/swarmd/internal/worktree"
)

// Purpose: ReopenProjectTask must recover both actual integrated repository
// results after two terminal empty launch failures, when the coordinator has no
// delta and a later no-op attempt exists.
// Real Git integration, Pebble reservation/restart and V3 admission are the
// narrow joined layer proving exact commits, isolation and retry postconditions;
// this is not provider-backed E2E or validation of the retained implementation.
func TestProjectTaskRepositoryContinuationAcrossNoopAndRestart(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	bases := []string{followupSourceGit(t, repos[0], "rev-parse", "HEAD"), followupSourceGit(t, repos[1], "rev-parse", "HEAD")}
	retainEmptyContinuationProgram(t, f, task, repos[:2], "failed-launch-one")
	retainEmptyContinuationProgram(t, f, task, repos[:2], "failed-launch-two")
	program := retainContinuationProgram(t, f, task, repos[:2], "useful", "sentinel")
	if followupSourceGit(t, task.WorkspacePath, "rev-parse", "HEAD") != task.BaseCommit { t.Fatal("coordinator must remain at captured base") }
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
	f.server.auth = auth.NewService(pebblestore.NewAuthStore(f.db), el)
	f.server.agents = agent.NewService(pebblestore.NewAgentStore(f.db), el)
	f.server.model = model.NewService(pebblestore.NewModelStore(f.db), el, nil)
	f.server.agentModelSettings = agentmodelsettings.NewService(pebblestore.NewAgentModelSettingsStore(f.db))
	f.server.planLifecycle = sessionruntime.NewPlanLifecycleService(f.server.sessions)
	f.server.planLifecycle.SetApplySessionMutation(f.server.applySessionV3PrimaryMutation)
	f.server.swarmStore = pebblestore.NewSwarmStore(f.db)
	f.server.topology = topologyruntime.NewService(pebblestore.NewTopologyStore(f.db), f.server.swarmStore)
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
		if active := task.ActiveAttempt(); active != nil {
			for _, inherited := range active.RepositoryContinuations { if inherited.Source.Path == repo { base.BaseCommit = inherited.HeadCommit } }
		}
		var lane worktree.Allocation
		programs, err := f.server.sessions.Store().ListTaskPrograms(task.SessionID)
		if err != nil { t.Fatal(err) }
		for _, previous := range programs {
			if retained, ok := previous.RepositoryLanes[repo]; ok {
				lane = worktree.Allocation{WorkspacePath: retained.WorkspacePath, BranchName: retained.Branch, BaseCommit: retained.BaseCommit}
			}
		}
		if lane.WorkspacePath == "" {
			lane, err = trees.AllocateTaskWorkspace(repo, base, seed, nil)
			if err != nil { t.Fatal(err) }
		}
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

func retainEmptyContinuationProgram(t *testing.T, f *matrixTestFixture, task *pebblestore.ProjectTaskRecord, repos []string, id string) pebblestore.TaskProgramRecord {
	t.Helper()
	trees := f.server.worktrees.(*worktree.Service)
	record := pebblestore.TaskProgramRecord{ParentSessionID: task.SessionID, ProgramID: id, DefinitionHash: id, ReservationRunID: task.ExecutionRunID(), State: pebblestore.TaskProgramStateFailed, RepositoryLanes: map[string]pebblestore.TaskProgramRepositoryLane{}, LaneHeads: map[string]string{}}
	record.Definition = pebblestore.TaskProgramDefinition{ID: id, Stages: []pebblestore.TaskProgramStageSpec{{ID: "build", DependencyEvidence: "source captured"}}}
	previous, err := f.server.sessions.Store().ListTaskPrograms(task.SessionID)
	if err != nil { t.Fatal(err) }
	for i, repo := range repos {
		base, err := trees.ResolveTaskBase(repo)
		if err != nil { t.Fatal(err) }
		var lane pebblestore.TaskProgramRepositoryLane
		for _, program := range previous { if existing, ok := program.RepositoryLanes[repo]; ok { lane = existing } }
		if lane.WorkspacePath == "" {
			digest := sha256.Sum256([]byte(task.SessionID + "\x00" + repo))
			allocated, err := trees.AllocateTaskWorkspace(repo, base, "program-lane-"+hex.EncodeToString(digest[:12]), nil)
			if err != nil { t.Fatal(err) }
			for _, source := range task.ProgramSources {
				if source.Path == repo { lane = pebblestore.TaskProgramRepositoryLane{SourcePath: repo, WorkspaceID: source.WorkspaceID, WorkspaceGeneration: source.WorkspaceGeneration, WorkspacePath: allocated.WorkspacePath, Branch: allocated.BranchName, BaseCommit: base.BaseCommit} }
			}
		}
		record.RepositoryLanes[repo], record.LaneHeads[repo] = lane, lane.BaseCommit
		jobID := fmt.Sprintf("empty-%d", i)
		record.Definition.Jobs = append(record.Definition.Jobs, pebblestore.TaskProgramJobSpec{ID: jobID, StageID: "build", AgentType: "coder", WorkspacePath: repo})
		record.Jobs = append(record.Jobs, pebblestore.TaskProgramJobRecord{JobID: jobID, StageID: "build", State: pebblestore.TaskProgramJobFailed, IntegrationState: "launch_rejected", AttemptNumber: 1, SourceWorkspacePath: repo})
	}
	record, _, err = f.server.sessions.Store().CreateTaskProgram(record)
	if err != nil { t.Fatal(err) }
	return record
}

// Purpose: only terminal failed launches with no child work may be bypassed.
// Resolver/store/Git rejects running, dirty, missing, unrecorded commit and child
// evidence before reservation, retaining all source and unfinished lane data.
func TestProjectTaskRepositoryContinuationFailedHistorySafety(t *testing.T) {
	for _, scenario := range []string{"running", "dirty", "missing", "uncommitted-result", "child"} {
		t.Run(scenario, func(t *testing.T) {
			f, p, project, task, repos := followupSourcesFixture(t)
			stopFollowupSourceRun(t, f, p, project.ID, task.ID)
			task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
			failed := retainEmptyContinuationProgram(t, f, task, repos[:2], "failed-launch")
			lane := failed.RepositoryLanes[repos[0]]
			switch scenario {
			case "running", "child":
				state := pebblestore.TaskProgramStateRunning
				transition := pebblestore.TaskProgramTransition{ExpectedRevision: failed.Revision, MutationID: "unfinished", State: &state}
				if scenario == "child" {
					state = pebblestore.TaskProgramStateFailed
					transition.Jobs = []pebblestore.TaskProgramJobTransition{{JobID: failed.Jobs[0].JobID, ExpectedState: failed.Jobs[0].State, State: failed.Jobs[0].State, ChildSessionID: "unintegrated-child"}}
				}
				if _, _, err := f.server.sessions.Store().TransitionTaskProgram(task.SessionID, failed.ProgramID, transition); err != nil { t.Fatal(err) }
			case "dirty":
				if err := os.WriteFile(filepath.Join(lane.WorkspacePath, "unfinished"), []byte("preserve"), 0600); err != nil { t.Fatal(err) }
			case "missing":
				if err := os.Rename(lane.WorkspacePath, filepath.Join(t.TempDir(), "preserved-lane")); err != nil { t.Fatal(err) }
			case "uncommitted-result":
				followupSourceGit(t, lane.WorkspacePath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "unrecorded work")
			}
			before := followupSourceGit(t, repos[0], "worktree", "list", "--porcelain")
			_, err := f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "must-reject", Revision: task.Revision, Feedback: "continue"})
			if err == nil { t.Fatal("unsafe failed history bypassed") }
			after, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
			if !reflect.DeepEqual(task, after) || followupSourceGit(t, repos[0], "worktree", "list", "--porcelain") != before { t.Fatal("rejection mutated task or source") }
		})
	}
}

// Purpose: a completed correction program supersedes its exact recorded input,
// not every older head by recency. Reopen and inspection must resolve both new
// lanes through durable attempt edges while catalog checkouts remain unchanged.
// Direct coordinator edits remain an explicit reconciliation boundary.
func TestProjectTaskRepositoryContinuationCompletedCorrectionAndInspection(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	first := retainContinuationProgram(t, f, task, repos[:2], "first", "sentinel")
	f.server.v3SessionExecutor = nil
	_, err := f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "correction", Revision: task.Revision, Feedback: "Correct both repository results"})
	if err == nil || !strings.Contains(err.Error(), "executor") { t.Fatalf("correction allocation: %v", err) }
	next, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	owner, found, err := f.server.sessions.Store().GetSession(next.SessionID)
	if err != nil || !found { t.Fatal(err) }
	if _, err := f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, project.ID, task.ID, func(current *pebblestore.ProjectTaskRecord) error {
		current.WorkspacePath, current.WorktreeBranch = owner.WorktreeRootPath, owner.WorktreeBranch
		return nil
	}); err != nil { t.Fatal(err) }
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	next, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	second := retainContinuationProgram(t, f, next, repos[:2], "corrected", "correction")
	for path, lane := range second.RepositoryLanes {
		if lane.BaseCommit != first.LaneHeads[path] { t.Fatal("correction did not inherit its exact recorded result") }
	}
	refs, err := f.server.resolveTaskRepositoryContinuations(p, next, next.ProgramSources)
	if err != nil || len(refs) != 2 { t.Fatalf("second correction selection: %+v %v", refs, err) }
	for _, ref := range refs {
		if ref.ProgramID != second.ProgramID || ref.HeadCommit != second.LaneHeads[ref.Source.Path] { t.Fatal("resolver retained old result") }
	}
	if _, err := f.server.sessions.Store().UpdateProjectTask(p.AccountScopeID, project.ID, task.ID, func(current *pebblestore.ProjectTaskRecord) error { current.Status = "needs_review"; return nil }); err != nil { t.Fatal(err) }
	for _, ref := range refs {
		req := tool.ProjectInspectionRequest{ProjectID: project.ID, TaskID: task.ID, AttemptID: next.ActiveAttemptID, SessionID: next.SessionID, WorkspaceID: ref.Source.WorkspaceID}
		target, err := f.server.ResolveProjectInspection(context.Background(), p, owner.ID, req)
		if err != nil || target.Root != ref.Lane.WorkspacePath || target.Reference.HeadCommit != ref.HeadCommit || target.Base != ref.Lane.BaseCommit { t.Fatalf("repository result inspection: %+v %v", target, err) }
		if _, err := os.Stat(filepath.Join(target.Root, "sentinel-"+fmt.Sprint(indexOfContinuationSource(repos[:2], ref.Source.Path))+".txt")); err != nil { t.Fatal("inspection lost original sentinel") }
		req.HeadCommit = first.LaneHeads[ref.Source.Path]
		if _, err := f.server.ResolveProjectInspection(context.Background(), p, owner.ID, req); err == nil { t.Fatal("stale result head accepted") }
	}
	next, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	_, err = f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "second-correction", Revision: next.Revision, Feedback: "Continue completed correction"})
	if err == nil || !strings.Contains(err.Error(), "executor") { t.Fatalf("subsequent program correction: %v", err) }
	for _, repo := range repos[:2] {
		if _, err := os.Stat(filepath.Join(repo, "correction-"+fmt.Sprint(indexOfContinuationSource(repos[:2], repo))+".txt")); !os.IsNotExist(err) { t.Fatal("source checkout received correction") }
	}
}

func indexOfContinuationSource(sources []string, path string) int {
	for i, source := range sources { if source == path { return i } }
	return -1
}

// Purpose: the canonical approved checkpoint run differs from ExecutionRunID.
// Same-task owner authentication must accept the durable plan/run binding and
// reject an unrelated run without changing either useful repository result.
func TestProjectTaskRepositoryContinuationCheckpointOwnerBinding(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	intent, found, err := f.server.sessions.Store().GetV3SessionActiveRunIntent(task.SessionID)
	if err != nil || !found { t.Fatalf("checkpoint intent: %v", err) }
	if intent.PlanID == "" || intent.CheckpointID == "" { t.Fatal("fixture did not execute an approved checkpoint") }
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	program := retainContinuationProgram(t, f, task, repos[:2], "checkpoint", "sentinel")
	// Create a second immutable program owned by the actual canonical run, on
	// the same authenticated lane. No caller-supplied hash grants access.
	program.ProgramID, program.Definition.ID, program.DefinitionHash = "checkpoint-owned", "checkpoint-owned", "checkpoint-owned"
	program.ReservationRunID = intent.RunID
	program.Revision = 0
	if _, _, err := f.server.sessions.Store().CreateTaskProgram(program); err != nil { t.Fatal(err) }
	// Read hydration may display the latest checkpoint run. The persisted task
	// reservation predates it, and must not be treated as static run equivalence.
	copyTask := *task
	copyTask.Attempts = append([]pebblestore.ProjectTaskAttempt(nil), task.Attempts...)
	copyTask.ActiveAttempt().RunID = "desktop-v3-run:task-" + task.ID
	if copyTask.ExecutionRunID() == intent.RunID { t.Fatal("checkpoint and reservation must differ") }
	if _, err := f.server.sessions.Store().AuthenticateTaskRepositoryProgram(&copyTask, p.UserID, copyTask.ActiveAttemptID, task.SessionID, program.ProgramID); err != nil { t.Fatalf("canonical checkpoint owner rejected: %v", err) }
	program.ProgramID, program.Definition.ID, program.DefinitionHash = "foreign-run", "foreign-run", "foreign-run"
	program.ReservationRunID = "unrelated-run"
	program.Revision = 0
	if _, _, err := f.server.sessions.Store().CreateTaskProgram(program); err != nil { t.Fatal(err) }
	if _, err := f.server.resolveTaskRepositoryContinuations(p, task, task.ProgramSources); err == nil { t.Fatal("unrelated program run accepted") }
	for path, lane := range program.RepositoryLanes {
		if followupSourceGit(t, lane.WorkspacePath, "rev-parse", "HEAD") != program.LaneHeads[path] { t.Fatal("run rejection changed result") }
	}
}

// Purpose: direct coordinator commits cannot be hidden by selecting an older
// program. The real retained correction worktree remains intact and reopening
// rejects before reservation; direct multi-repository reconciliation is explicit.
func TestProjectTaskRepositoryContinuationRejectsDirectCorrectionDelta(t *testing.T) {
	f, p, project, task, repos := followupSourcesFixture(t)
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	task, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	retainContinuationProgram(t, f, task, repos[:2], "useful", "sentinel")
	f.server.v3SessionExecutor = nil
	_, err := f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "correction", Revision: task.Revision, Feedback: "correct retained work"})
	if err == nil || !strings.Contains(err.Error(), "executor") { t.Fatalf("allocation: %v", err) }
	current, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	owner, found, err := f.server.sessions.Store().GetSession(current.SessionID)
	if err != nil || !found { t.Fatal(err) }
	stopFollowupSourceRun(t, f, p, project.ID, task.ID)
	current, _, _ = f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	followupSourceGit(t, owner.WorktreeRootPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "direct correction")
	head := followupSourceGit(t, owner.WorktreeRootPath, "rev-parse", "HEAD")
	before := followupSourceGit(t, repos[0], "worktree", "list", "--porcelain")
	_, err = f.server.ReopenProjectTask(context.Background(), p, project.ID, task.ID, tool.ProjectTaskFollowupInput{ClientRequestID: "unsafe-next", Revision: current.Revision, Feedback: "continue"})
	if err == nil || !strings.Contains(err.Error(), "reconcile") { t.Fatalf("direct delta boundary: %v", err) }
	after, _, _ := f.server.sessions.Store().GetProjectTask(p.AccountScopeID, project.ID, task.ID)
	if !reflect.DeepEqual(current, after) || followupSourceGit(t, repos[0], "worktree", "list", "--porcelain") != before || followupSourceGit(t, owner.WorktreeRootPath, "rev-parse", "HEAD") != head { t.Fatal("rejection lost direct work or allocated") }
}
