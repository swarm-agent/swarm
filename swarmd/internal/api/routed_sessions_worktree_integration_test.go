package api

import (
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

func (f *fakeWorktreeService) RollbackAllocation(_ worktreeruntime.Allocation) error { return nil }

type routedWorktreeServiceStub struct {
	fakeWorktreeService
	repositoryFixture  *testing.T
	allocationErrs     []error
	allocationCalls    int
	allocationBranches []string
}

func (s *routedWorktreeServiceStub) AllocateDetachedWorkspace(workspacePath, nameSeed string) (worktreeruntime.Allocation, error) {
	s.lastWorkspace = workspacePath
	s.lastNameSeed = nameSeed
	call := s.allocationCalls
	s.allocationCalls++
	if call < len(s.allocationErrs) && s.allocationErrs[call] != nil {
		return worktreeruntime.Allocation{}, s.allocationErrs[call]
	}
	return s.fakeWorktreeService.AllocateDetachedWorkspace(workspacePath, nameSeed)
}

func (s *routedWorktreeServiceStub) AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	s.lastBaseBranch = baseBranch
	s.lastBranchName = branchName
	s.allocationBranches = append(s.allocationBranches, branchName)
	allocation, err := s.AllocateDetachedWorkspace(workspacePath, nameSeed)
	if err != nil {
		return worktreeruntime.Allocation{}, err
	}
	if baseBranch != "" {
		allocation.BaseBranch = baseBranch
	}
	allocation.BranchName = branchName
	if s.repositoryFixture != nil {
		// Preserve real source/lane Git authority for deployment identity checks.
		runGitCommitTestCommand(s.repositoryFixture, workspacePath, "worktree", "add", "-b", branchName, allocation.WorkspacePath, "HEAD")
		allocation.BaseCommit = strings.TrimSpace(runGitCommitTestCommand(s.repositoryFixture, workspacePath, "rev-parse", "HEAD"))
	}
	return allocation, nil
}

func (s *routedWorktreeServiceStub) AllocateDetachedWorkspaceRequestedForPrincipal(_ identity.Principal, workspacePath, nameSeed, baseBranch, branchName string) (worktreeruntime.Allocation, error) {
	return s.AllocateDetachedWorkspaceRequested(workspacePath, nameSeed, baseBranch, branchName)
}
