package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func (s *Server) validateTaskRepositoryContinuation(p identity.Principal, task *pebblestore.ProjectTaskRecord, ref pebblestore.ProjectTaskRepositoryContinuation) error {
	if task.AccountID != p.AccountScopeID { return errors.New("continuation account mismatch") }
	_, err := s.sessions.Store().AuthenticateTaskRepositoryContinuation(task, p.UserID, ref)
	if err != nil { return err }
	if s.workspace == nil { return errors.New("continuation catalog unavailable") }
	canonical, err := s.workspace.ScopeForPathForPrincipal(p, ref.Source.Path)
	if err != nil || !canonical.Matched || canonical.WorkspacePath != ref.Source.Path || canonical.ResolvedPath != ref.Source.Path || canonical.WorkspaceID != ref.Source.WorkspaceID || canonical.WorkspaceGeneration != ref.Source.WorkspaceGeneration { return errors.New("continuation catalog identity changed") }
	target, err := s.worktrees.InspectTaskWorkspace(ref.Source.Path)
	if err != nil || !target.Clean || target.BranchName != ref.TargetBranch { return errors.New("continuation captured destination changed or dirty") }
	validator, ok := s.worktrees.(interface {
		ValidateTaskRepositoryLane(string, string, string, string) error
		TaskCommitDescendsFrom(string, string, string) (bool, error)
	})
	if !ok { return errors.New("repository continuation Git authority unavailable") }
	digest := sha256.Sum256([]byte(ref.SessionID + "\x00" + ref.Source.Path))
	seed := "program-lane-" + hex.EncodeToString(digest[:12])
	if err := validator.ValidateTaskRepositoryLane(ref.Source.Path, ref.Lane.WorkspacePath, seed, ref.Lane.Branch); err != nil { return err }
	state, err := s.worktrees.InspectTaskWorkspace(ref.Lane.WorkspacePath)
	if err != nil || !state.Clean || state.BranchName != ref.Lane.Branch || state.HeadCommit != ref.HeadCommit { return errors.New("retained repository lane unavailable, dirty or changed") }
	targetContains, err := validator.TaskCommitDescendsFrom(ref.Source.Path, ref.TargetHead, target.HeadCommit)
	if err != nil || !targetContains { return errors.New("continuation captured destination head unavailable or divergent") }
	descends, err := validator.TaskCommitDescendsFrom(ref.Source.Path, ref.Lane.BaseCommit, ref.HeadCommit)
	if err != nil || !descends { return errors.New("retained repository result ancestry mismatch") }
	// Canonical integration may cherry-pick child commits. LaneHeads is the
	// persisted resulting Git head; child hashes need not be its ancestors.
	return nil
}

// Search recorded attempts, including a useful program preceding a no-op retry.
// Different useful histories are not ordered by recency: ambiguity fails closed.
func (s *Server) resolveTaskRepositoryContinuations(p identity.Principal, task *pebblestore.ProjectTaskRecord, sources []pebblestore.ProjectTaskSource) ([]pebblestore.ProjectTaskRepositoryContinuation, error) {
	copyTask := *task
	copyTask.Attempts = append([]pebblestore.ProjectTaskAttempt(nil), task.Attempts...)
	copyTask.EnsureTaskAttempts()
	copyTask.CaptureActiveAttempt()
	if active := copyTask.ActiveAttempt(); active != nil && len(active.RepositoryContinuations) > 0 {
		owner, found, err := s.sessions.Store().GetSession(active.SessionID)
		if err != nil || !found { return nil, errors.New("retained correction owner unavailable") }
		for _, ref := range active.RepositoryContinuations {
			path := owner.WorktreeRootPath
			if !ref.Source.SameIdentity(task.SourceWorkspace) {
				path = ""
				rows, _ := owner.Metadata["swarm_v3_worktree_history"].([]any)
				for _, row := range rows {
					item, ok := row.(map[string]any)
					if ok && item["source_workspace_path"] == ref.Source.Path { path, _ = item["path"].(string) }
				}
			}
			state, err := s.worktrees.InspectTaskWorkspace(path)
			if err != nil || !state.Clean || state.HeadCommit != ref.HeadCommit { return nil, errors.New("retained correction changed; reconcile its result before another follow-up") }
		}
	}
	selected := map[string]pebblestore.ProjectTaskRepositoryContinuation{}
	for _, attempt := range copyTask.Attempts {
		programs, err := s.sessions.Store().ListTaskPrograms(attempt.SessionID)
		if err != nil { return nil, err }
		for _, program := range programs {
			if len(program.RepositoryLanes) == 0 { continue }
			if program.State != pebblestore.TaskProgramStateCompleted { return nil, errors.New("retained repository program requires explicit reconciliation") }
			for path, lane := range program.RepositoryLanes {
				head := program.LaneHeads[path]
				if head == "" || head == lane.BaseCommit { return nil, errors.New("completed repository program has no authenticated committed result") }
				if len(selected) >= 64 { return nil, errors.New("retained repository result inventory exceeds bound") }
				var source pebblestore.ProjectTaskSource
				for _, admitted := range sources { if admitted.Path == path { source = admitted } }
				if source.Path == "" { return nil, errors.New("retained result has no admitted catalog source") }
				target, err := s.worktrees.InspectTaskWorkspace(source.Path)
				if err != nil || !target.Clean || target.BranchName == "" { return nil, errors.New("continuation target unavailable or dirty") }
				if source.SameIdentity(task.SourceWorkspace) && attempt.BaseBranch != "" && attempt.BaseBranch != target.BranchName { return nil, errors.New("retained program intended destination branch changed") }
				ref := pebblestore.ProjectTaskRepositoryContinuation{Source: source, AttemptID: attempt.ID, SessionID: attempt.SessionID, ProgramID: program.ProgramID, ProgramRevision: program.Revision, Lane: lane, HeadCommit: head, TargetBranch: target.BranchName, TargetHead: target.HeadCommit}
				if err := s.validateTaskRepositoryContinuation(p, &copyTask, ref); err != nil { return nil, err }
				if prior, ok := selected[path]; ok && prior.HeadCommit != head { return nil, fmt.Errorf("ambiguous retained repository results for %q", path) }
				if _, ok := selected[path]; !ok { selected[path] = ref }
			}
		}
	}
	paths := make([]string, 0, len(selected))
	for path := range selected { paths = append(paths, path) }
	sort.Strings(paths)
	refs := make([]pebblestore.ProjectTaskRepositoryContinuation, 0, len(paths))
	for _, path := range paths { refs = append(refs, selected[path]) }
	return refs, nil
}

// All evidence is persisted by reservation before allocation. Deterministic
// session-owned branches recover partial allocation without touching old lanes.
func (s *Server) allocateTaskRepositoryContinuations(p identity.Principal, task *pebblestore.ProjectTaskRecord, snapshot *pebblestore.SessionSnapshot) error {
	a := task.ActiveAttempt()
	if a == nil || len(a.RepositoryContinuations) == 0 { return nil }
	allocator, ok := s.worktrees.(interface {
		AllocateProjectTaskFollowup(identity.Principal, string, string, string, string, string) (worktreeruntime.Allocation, error)
	})
	if !ok { return errors.New("repository continuation allocator unavailable") }
	for _, ref := range a.RepositoryContinuations {
		if err := s.validateTaskRepositoryContinuation(p, task, ref); err != nil { return err }
	}
	history := []any{}
	for _, ref := range a.RepositoryContinuations {
		if ref.Source.SameIdentity(task.SourceWorkspace) { continue }
		digest := sha256.Sum256([]byte(task.SessionID + "\x00" + ref.Source.Path))
		branch := "agent/followup-repository-" + hex.EncodeToString(digest[:12])
		alloc, err := allocator.AllocateProjectTaskFollowup(p, ref.Source.Path, task.SessionID, branch, ref.HeadCommit, ref.TargetBranch)
		if err != nil { return err }
		history = append(history, map[string]any{"path": alloc.WorkspacePath, "workspace_id": ref.Source.WorkspaceID, "workspace_generation": ref.Source.WorkspaceGeneration, "source_workspace_path": ref.Source.Path, "owner_session_id": task.SessionID, "branch": alloc.BranchName, "base_branch": ref.TargetBranch, "base_commit": ref.Lane.BaseCommit})
		available := true
		snapshot.WorkspaceGrants = append(snapshot.WorkspaceGrants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantWorktree, WorkspaceID: ref.Source.WorkspaceID, WorkspaceGeneration: ref.Source.WorkspaceGeneration, Path: alloc.WorkspacePath, Available: &available})
	}
	snapshot.Metadata["swarm_v3_worktree_history"] = history
	snapshot.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(snapshot.WorkspaceGrants)
	return nil
}
