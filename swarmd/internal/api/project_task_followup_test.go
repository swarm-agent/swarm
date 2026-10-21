package api

import (
	"errors"

	"swarm/packages/swarmd/internal/identity"

	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

type failOnceFollowupAllocator struct {
	*worktreeruntime.Service
	failed bool
}

func (s *failOnceFollowupAllocator) AllocateProjectTaskFollowup(p identity.Principal, source, owner, branch, head, target string) (worktreeruntime.Allocation, error) {
	if !s.failed {
		s.failed = true
		return worktreeruntime.Allocation{}, errors.New("injected allocation failure")
	}
	return s.Service.AllocateProjectTaskFollowup(p, source, owner, branch, head, target)
}
