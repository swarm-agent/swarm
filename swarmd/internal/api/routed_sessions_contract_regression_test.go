package api

import (
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type routedContractRollbackWorktree struct {
	routedWorktreeServiceStub
	rollbacks []worktreeruntime.Allocation
}

func (s *routedContractRollbackWorktree) RollbackAllocation(allocation worktreeruntime.Allocation) error {
	s.rollbacks = append(s.rollbacks, allocation)
	return nil
}
