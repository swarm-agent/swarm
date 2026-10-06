package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktree "swarm/packages/swarmd/internal/worktree"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose: repositoryLaneForSource must capture the authenticated correction
// runtime HEAD returned by resolveTaskTargetWorkspace, not current catalog HEAD.
// Real two-repository Git allocation and scheduler admission is the narrowest
// layer proving downstream child bases; it does not invoke a provider.
func TestTaskProgramRepositoryLaneConsumesCorrectionBase(t *testing.T) {
	p, sources, bases := multiRepoProgramFixture(t, false)
	multiRepoCommitChild(t, p, sources[0], "job-a", "a.txt")
	multiRepoCommitChild(t, p, sources[1], "job-b", "b.txt")
	if err := p.integrateMultiRepositoryStage(0); err != nil { t.Fatal(err) }
	completed := pebblestore.TaskProgramStateCompleted
	old, _, err := p.service.sessions.TransitionTaskProgram(p.parentSession.ID, p.record.ProgramID, pebblestore.TaskProgramTransition{ExpectedRevision: p.record.Revision, MutationID: "finish", State: &completed})
	if err != nil { t.Fatal(err) }
	original := p.parentSession
	original.Metadata["project_id"], original.Metadata["task_id"] = "project", "task"
	original.Metadata["swarm_v3_source_workspace_id"] = old.RepositoryLanes[sources[0]].WorkspaceID
	original.Metadata["swarm_v3_source_workspace_generation"] = int64(1)
	if err := p.service.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil { t.Fatal(err) }
	if _, err := p.service.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: original.ID, UserID: original.UserID, AccountScopeID: original.AccountScopeID, Kind: sessionruntime.SessionMutationUpdateSettings, Session: &original,
		ClientRequestID: "original-context", IdempotencyKey: "original-context", PayloadHash: "original-context", RequestHash: "original-context",
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: original.WorktreeRootPath, SourcePath: sources[0], OwnerSessionID: original.ID, Branch: original.WorktreeBranch, AllocatedRuntimeRoot: true},
	}); err != nil { t.Fatal(err) }
	p.parentSession.Metadata = cloneGenericMap(original.Metadata)
	p.parentSession.ID = "correction-parent"
	p.parentSession.Metadata["swarm_v3_worktree_owner_session_id"] = p.parentSession.ID
	catalog := pebblestore.NewWorkspaceStore(p.service.sessions.Store().Underlying())
	for _, source := range sources { if _, err := catalog.AddForAccount(original.AccountScopeID, source, "Fixture"); err != nil { t.Fatal(err) } }
	allocator := worktree.NewService(pebblestore.NewWorktreeStore(p.service.sessions.Store().Underlying()), workspace.NewService(catalog), nil)
	var histories []any
	for i, source := range sources {
		p.req.Principal = identity.Principal{Type: "user", UserID: original.UserID, AccountScopeID: original.AccountScopeID}
		base, err := p.service.worktrees.ResolveTaskBase(old.RepositoryLanes[source].WorkspacePath)
		if err != nil { t.Fatal(err) }
		seed := "correction-a"
		if i == 1 { digest := sha256.Sum256([]byte(p.parentSession.ID + "\x00" + source)); seed = "followup-repository-" + hex.EncodeToString(digest[:12]) }
		correction, err := allocator.AllocateProjectTaskFollowup(p.req.Principal, source, p.parentSession.ID, "agent/"+seed, base.BaseCommit, "dev")
		if err != nil { t.Fatal(err) }
		if i == 0 {
			p.parentSession.WorktreeRootPath, p.parentSession.WorkspacePath, p.parentSession.WorktreeBranch = correction.WorkspacePath, correction.WorkspacePath, correction.BranchName
			p.parentSession.Metadata["swarm_v3_runtime_workspace_path"] = correction.WorkspacePath
			p.parentSession.Metadata["swarm_v3_worktree_base_commit"] = bases[i]
		} else {
			histories = append(histories, map[string]any{"path": correction.WorkspacePath, "workspace_id": old.RepositoryLanes[source].WorkspaceID, "workspace_generation": int64(1), "source_workspace_path": source, "owner_session_id": p.parentSession.ID, "branch": correction.BranchName, "base_branch": "dev", "base_commit": bases[i]})
		}
	}
	p.parentSession.Metadata["swarm_v3_worktree_history"] = histories
	p.parentSession.Metadata["task_attempt_id"] = "correction"
	p.parentSession.Metadata["swarm_v3_source_workspace_id"] = old.RepositoryLanes[sources[0]].WorkspaceID
	p.parentSession.Metadata["swarm_v3_source_workspace_generation"] = int64(1)
	task := &pebblestore.ProjectTaskRecord{ID: "task", ProjectID: "project", Title: "Correction", Agent: "swarm", AccountID: original.AccountScopeID, SessionID: p.parentSession.ID, ActiveAttemptID: "correction", SourceWorkspace: pebblestore.ProjectTaskSource{WorkspaceID: old.RepositoryLanes[sources[0]].WorkspaceID, WorkspaceGeneration: 1, Path: sources[0], Provenance: "explicit"}, Attempts: []pebblestore.ProjectTaskAttempt{{ID: "initial", SessionID: original.ID, RunID: old.ReservationRunID}, {ID: "correction", SessionID: p.parentSession.ID, UserID: original.UserID}}}
	for i, source := range sources {
		ref := pebblestore.ProjectTaskRepositoryContinuation{Source: pebblestore.ProjectTaskSource{WorkspaceID: old.RepositoryLanes[source].WorkspaceID, WorkspaceGeneration: 1, Path: source, Provenance: "explicit"}, AttemptID: "initial", SessionID: original.ID, ProgramID: old.ProgramID, ProgramRevision: old.Revision, Lane: old.RepositoryLanes[source], HeadCommit: old.LaneHeads[source], TargetBranch: "dev", TargetHead: bases[i]}
		task.Attempts[1].RepositoryContinuations = append(task.Attempts[1].RepositoryContinuations, ref)
	}
	if err := p.service.sessions.Store().PutProject(original.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil { t.Fatal(err) }
	if err := p.service.sessions.Store().PutProjectTask(original.AccountScopeID, task); err != nil { t.Fatal(err) }
	if err := p.service.validateTaskRepositoryContinuationBases(p.parentSession, identity.Principal{Type: "user", UserID: original.UserID, AccountScopeID: original.AccountScopeID}); err != nil { t.Fatal(err) }
	incomplete := p.parentSession
	incomplete.Metadata = cloneGenericMap(p.parentSession.Metadata)
	incomplete.Metadata["swarm_v3_worktree_history"] = []any{}
	if err := p.service.validateTaskRepositoryContinuationBases(incomplete, identity.Principal{Type: "user", UserID: original.UserID, AccountScopeID: original.AccountScopeID}); err == nil { t.Fatal("partial continuation allocation admitted") }
	if err := p.service.sessions.Store().CompleteRepositoryHistoryMaintenance(context.Background()); err != nil { t.Fatal(err) }
	if _, err := p.service.sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{
		SessionID: p.parentSession.ID, UserID: p.parentSession.UserID, AccountScopeID: p.parentSession.AccountScopeID,
		Kind: sessionruntime.SessionMutationCreateSession, Session: &p.parentSession,
		ClientRequestID: "create-correction", IdempotencyKey: "create-correction", PayloadHash: "create-correction", RequestHash: "create-correction",
		WorktreeAdmission: &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: p.parentSession.WorktreeRootPath, SourcePath: sources[0], OwnerSessionID: p.parentSession.ID, Branch: p.parentSession.WorktreeBranch, AllocatedRuntimeRoot: true},
	}); err != nil { t.Fatal(err) }
	definition := old.Definition
	definition.ID = "correction-program"
	record := pebblestore.TaskProgramRecord{ParentSessionID: p.parentSession.ID, ProgramID: definition.ID, DefinitionHash: "correction", Definition: definition, State: pebblestore.TaskProgramStateRunning}
	for _, def := range definition.Jobs { record.Jobs = append(record.Jobs, pebblestore.TaskProgramJobRecord{JobID: def.ID, StageID: def.StageID, State: pebblestore.TaskProgramJobDeclared}) }
	record, _, err = p.service.sessions.CreateTaskProgram(record)
	if err != nil { t.Fatal(err) }
	p.record = record
	if _, err := p.multiRepositoryWorkspacePath(); err != nil { t.Fatal(err) }
	for i, source := range sources {
		lane := p.record.RepositoryLanes[source]
		if lane.BaseCommit != old.LaneHeads[source] || lane.BaseCommit == bases[i] { t.Fatal("scheduler captured current checkout instead of correction") }
		base, err := p.service.worktrees.ResolveTaskBase(lane.WorkspacePath)
		if err != nil { t.Fatal(err) }
		child, err := p.service.worktrees.AllocateTaskWorkspace(lane.WorkspacePath, base, "correct-child-"+[]string{"a", "b"}[i], nil)
		if err != nil { t.Fatal(err) }
		file := []string{"a.txt", "b.txt"}[i]
		if bytes, err := os.ReadFile(filepath.Join(child.WorkspacePath, file)); err != nil || len(bytes) == 0 { t.Fatal("downstream child missing retained sentinel") }
		if programFixtureGit(t, source, "rev-parse", "HEAD") != bases[i] || programFixtureGit(t, source, "status", "--porcelain") != "" { t.Fatal("scheduler changed captured source") }
	}
}
