package run

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Validate the recorded facts without silently rewriting historical provenance.
func validateSessionRepositoryIdentity(session pebblestore.SessionSnapshot) error {
	if !session.WorktreeEnabled {
		return nil
	}
	if owner := mapString(session.Metadata, "swarm_v3_worktree_owner_session_id"); owner != "" && owner != session.ID {
		return errors.New("session worktree owner identity mismatch")
	}
	source := mapString(session.Metadata, "swarm_v3_source_workspace_path")
	if source == "" || session.WorktreeRootPath == "" {
		return errors.New("session repository identity is incomplete")
	}
	if runtime := mapString(session.Metadata, "swarm_v3_runtime_workspace_path"); runtime != "" && filepath.Clean(runtime) != filepath.Clean(session.WorktreeRootPath) {
		return errors.New("session runtime worktree identity mismatch")
	}
	for _, item := range sessionWorktreeHistory(session.Metadata["swarm_v3_worktree_history"]) {
		if filepath.Clean(mapString(item, "path")) != filepath.Clean(session.WorktreeRootPath) {
			continue
		}
		if mapString(item, "owner_session_id") != session.ID || mapString(item, "workspace_id") != mapString(session.Metadata, "swarm_v3_source_workspace_id") || mapString(item, "branch") != session.WorktreeBranch {
			return errors.New("session worktree history contradicts current identity")
		}
	}
	return worktreeruntime.ValidateOwnedIdentity(source, session.WorktreeRootPath, session.WorktreeBranch, firstNonEmptyString(mapString(session.Metadata, "swarm_v3_worktree_base_commit"), mapString(session.Metadata, "base_commit")))
}

// A target transition must not retarget a worker or abandon a scheduler lane.
// Retained descendants remain inspectable; this conservative gate requires their
// explicit integration before changing the parent's execution/attachment contract.
func (s *Service) ensureWorkspaceTransitionIdle(session pebblestore.SessionSnapshot, principal identity.Principal) error {
	if mapString(session.Metadata, "lineage_kind") == "delegated_subagent" {
		return errors.New("delegated worker workspace assignment is immutable")
	}
	lanes, err := s.sessions.TaskProgramRepositoryLanes(session.ID)
	if err != nil {
		return err
	}
	for _, lane := range lanes {
		if lane.WorkspacePath != "" && lane.WorkspacePath != session.WorktreeRootPath {
			return errors.New("workspace transition requires resolving retained Task Program integration lanes first")
		}
	}
	children, err := s.sessions.ListSessionsForAccountUser(principal.AccountScopeID, principal.UserID, 10000)
	if err != nil {
		return err
	}
	if len(children) >= 10000 {
		return errors.New("workspace transition ownership inventory exceeds bound")
	}
	for _, child := range children {
		if mapString(child.Metadata, "parent_session_id") == session.ID && mapString(child.Metadata, "lineage_kind") == "delegated_subagent" {
			return errors.New("workspace transition is pinned by retained delegated work; preserve its source and integration target")
		}
	}
	return nil
}

// prepareSessionWorkspaceLane operates only after all requested saved identities
// have been authorized. Allocation is returned to the caller for rollback if the
// canonical compare-and-swap mutation fails.
func (s *Service) prepareSessionWorkspaceLane(session pebblestore.SessionSnapshot, primary SessionWorkspaceCanonicalization, next *pebblestore.SessionSnapshot) (*worktreeruntime.Allocation, error) {
	if !session.WorktreeEnabled {
		return nil, nil
	}
	if err := validateSessionRepositoryIdentity(session); err != nil {
		return nil, err
	}
	oldID := mapString(session.Metadata, "swarm_v3_source_workspace_id")
	oldGeneration, _ := strconv.ParseInt(mapString(session.Metadata, "swarm_v3_source_workspace_generation"), 10, 64)
	old := SessionWorkspaceCanonicalization{WorkspaceID: oldID, WorkspaceGeneration: oldGeneration, SourceWorkspacePath: mapString(session.Metadata, "swarm_v3_source_workspace_path")}
	oldAllocation := worktreeruntime.Allocation{WorkspacePath: session.WorktreeRootPath, BranchName: session.WorktreeBranch, BaseBranch: session.WorktreeBaseBranch, BaseCommit: firstNonEmptyString(mapString(session.Metadata, "swarm_v3_worktree_base_commit"), mapString(session.Metadata, "base_commit"))}
	next.Metadata["swarm_v3_worktree_history"] = appendSessionWorktreeHistory(session.Metadata["swarm_v3_worktree_history"], old, oldAllocation, session.ID)
	if oldID == primary.WorkspaceID {
		return nil, nil
	}
	if s.worktrees == nil {
		return nil, errors.New("workspace transition requires isolated worktree allocation")
	}
	state, err := s.worktrees.InspectTaskWorkspace(session.WorktreeRootPath)
	if err != nil {
		return nil, err
	}
	if !state.Clean {
		return nil, errors.New("workspace transition cannot leave a dirty worktree")
	}
	for _, item := range sessionWorktreeHistory(next.Metadata["swarm_v3_worktree_history"]) {
		if mapString(item, "workspace_id") != primary.WorkspaceID {
			continue
		}
		allocation, err := s.resolveOwnedSessionWorktree(session, primary, mapString(item, "path"))
		if err != nil {
			return nil, err
		}
		applySessionWorkspaceAllocation(next, primary, allocation)
		return nil, nil
	}
	if len(sessionWorktreeHistory(next.Metadata["swarm_v3_worktree_history"])) >= 64 {
		return nil, errors.New("session worktree history limit reached; existing provenance is retained")
	}
	base, err := s.worktrees.ResolveTaskBase(primary.SourceWorkspacePath)
	if err != nil {
		return nil, fmt.Errorf("workspace target preflight: %w", err)
	}
	allocation, err := s.worktrees.AllocateTaskWorkspace(primary.SourceWorkspacePath, base, "session-"+compactManageWorkspaceSessionID(session.ID), nil)
	if err != nil {
		return nil, err
	}
	applySessionWorkspaceAllocation(next, primary, allocation)
	if err := validateSessionRepositoryIdentity(*next); err != nil {
		return nil, errors.Join(err, s.worktrees.RollbackAllocation(allocation))
	}
	return &allocation, nil
}

func applySessionWorkspaceAllocation(next *pebblestore.SessionSnapshot, primary SessionWorkspaceCanonicalization, allocation worktreeruntime.Allocation) {
	next.WorktreeEnabled, next.WorktreeRootPath = true, allocation.WorkspacePath
	next.WorktreeBranch, next.WorktreeBaseBranch = allocation.BranchName, allocation.BaseBranch
	next.Metadata["swarm_v3_runtime_workspace_path"] = allocation.WorkspacePath
	next.Metadata["swarm_v3_worktree_owner_session_id"] = next.ID
	next.Metadata["swarm_v3_worktree_base_commit"] = allocation.BaseCommit
	next.Metadata["base_commit"] = allocation.BaseCommit
	next.Metadata["swarm_v3_worktree_history"] = appendSessionWorktreeHistory(next.Metadata["swarm_v3_worktree_history"], primary, allocation, next.ID)
}
