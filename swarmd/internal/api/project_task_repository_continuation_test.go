package api

import (
	"errors"

	"strings"

	"swarm/packages/swarmd/internal/identity"

	worktree "swarm/packages/swarmd/internal/worktree"
)

// Fault injection only: allocations/Git remain real; no provider outcome is faked.
type failSecondaryContinuationAllocator struct{ *worktree.Service }

func (s *failSecondaryContinuationAllocator) AllocateProjectTaskFollowup(p identity.Principal, source, owner, branch, head, target string) (worktree.Allocation, error) {
	if strings.HasPrefix(branch, "agent/followup-repository-") {
		return worktree.Allocation{}, errors.New("injected allocation failure")
	}
	return s.Service.AllocateProjectTaskFollowup(p, source, owner, branch, head, target)
}
