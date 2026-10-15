package run

import (
	"errors"

	"swarm/packages/swarmd/internal/identity"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Requirement: a primary Swarm session starts in the user's current checkout,
// can move its primary workspace through the canonical V3 mutation boundary,
// and may explicitly adopt a managed worktree without creating a child or
// replacement session. These tests exercise the narrowest production control
// plane that can prove durable grants, immediate runtime scope refresh, and
// rollback with zero partial mutation on a dirty move.
type failingWorkspaceMapService struct{}

func (failingWorkspaceMapService) GetOrCreateDefault(string) (pebblestore.WorkspaceMap, error) {
	return pebblestore.WorkspaceMap{}, errors.New("workspace map unavailable")
}

func (failingWorkspaceMapService) Update(string, int64, string) (pebblestore.WorkspaceMap, error) {
	return pebblestore.WorkspaceMap{}, errors.New("workspace map unavailable")
}

type sameSessionWorktreeStub struct {
	allocation        worktreeruntime.Allocation
	allocateErr       error
	states            map[string]worktreeruntime.TaskWorkspaceState
	rolledBack        []string
	requestedBranches []string
	conflictOnce      bool
}

func (s *sameSessionWorktreeStub) AttachBranch(_, _, _ string) (string, error) { return "", nil }
func (s *sameSessionWorktreeStub) ResolveTaskBase(string) (worktreeruntime.TaskBase, error) {
	return worktreeruntime.TaskBase{}, nil
}
func (s *sameSessionWorktreeStub) AllocateTaskWorkspace(string, worktreeruntime.TaskBase, string, []string) (worktreeruntime.Allocation, error) {
	return worktreeruntime.Allocation{}, nil
}
func (s *sameSessionWorktreeStub) InspectTaskWorkspace(path string) (worktreeruntime.TaskWorkspaceState, error) {
	state, ok := s.states[path]
	if !ok {
		return worktreeruntime.TaskWorkspaceState{}, errors.New("unknown worktree")
	}
	return state, nil
}
func (s *sameSessionWorktreeStub) TaskCommitDescendsFrom(string, string, string) (bool, error) {
	return true, nil
}
func (s *sameSessionWorktreeStub) TaskCommitRangeIntegratedInto(string, string, string, string) (bool, error) {
	return false, nil
}
func (s *sameSessionWorktreeStub) RemoveIntegratedTaskWorkspace(string, string, string, string, string, string) error {
	return nil
}
func (s *sameSessionWorktreeStub) GetConfigForPrincipal(identity.Principal, string) (worktreeruntime.Config, error) {
	return worktreeruntime.Config{Enabled: true, UseCurrentBranch: true, BranchName: "agent/<id>"}, nil
}
func (s *sameSessionWorktreeStub) AllocateDetachedWorkspaceRequestedForPrincipal(_ identity.Principal, _, _, _, branch string) (worktreeruntime.Allocation, error) {
	s.requestedBranches = append(s.requestedBranches, branch)
	if s.conflictOnce {
		s.conflictOnce = false
		return worktreeruntime.Allocation{}, &worktreeruntime.RequestedWorktreeNameConflictError{WorktreeName: branch, Cause: errors.New("occupied")}
	}
	return s.allocation, s.allocateErr
}
func (s *sameSessionWorktreeStub) RollbackAllocation(allocation worktreeruntime.Allocation) error {
	s.rolledBack = append(s.rolledBack, allocation.WorkspacePath)
	return nil
}
