package api

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Only server-allocated, Git-validated lanes may receive allocation evidence.
// Existing-path requests must use recovery, never masquerade as fresh allocation.
func sessionsV3AllocatedLaneAdmission(next pebblestore.SessionSnapshot) (*pebblestore.WorktreeAdmissionEvidence, error) {
	if !next.WorktreeEnabled {
		return nil, nil
	}
	base, _ := next.Metadata["base_commit"].(string)
	if err := worktreeruntime.ValidateOwnedIdentity(next.WorkspacePath, next.WorktreeRootPath, next.WorktreeBranch, base); err != nil {
		return nil, err
	}
	return &pebblestore.WorktreeAdmissionEvidence{Kind: "allocated", Path: next.WorktreeRootPath, SourcePath: next.WorkspacePath, OwnerSessionID: next.ID, Branch: next.WorktreeBranch}, nil
}
