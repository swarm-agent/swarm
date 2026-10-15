package api

import (
	"swarm/packages/swarmd/internal/identity"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type distinctFixtureWorktrees struct{ testMockWorktreeService }

func (m *distinctFixtureWorktrees) AllocateDetachedWorkspaceRequestedForPrincipal(p identity.Principal, workspace, seed, base, branch string) (worktreeruntime.Allocation, error) {
	return worktreeruntime.Allocation{WorkspacePath: "/mock/worktrees/" + branch, BranchName: branch, BaseBranch: "dev", BaseCommit: "base-commit-sha-001", RepoRoot: workspace}, nil
}
