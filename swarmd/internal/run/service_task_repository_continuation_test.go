package run

import (
	"os"
	"path/filepath"

	worktree "swarm/packages/swarmd/internal/worktree"
)

// Fault injection happens only after real integration. The production remover
// must reject a genuinely dirty checkout; no cleanup receipt or head is spoofed.
type continuationDirtyCleanupWorktrees struct{ *worktree.Service }

func (s *continuationDirtyCleanupWorktrees) RemoveIntegratedTaskWorkspace(parent, child, owner, branch, base, head string) error {
	if err := os.WriteFile(filepath.Join(child, "unfinished-cleanup.txt"), []byte("preserve"), 0600); err != nil {
		return err
	}
	return s.Service.RemoveIntegratedTaskWorkspace(parent, child, owner, branch, base, head)
}
