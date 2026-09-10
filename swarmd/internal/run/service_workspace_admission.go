package run

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

// sessionLaneAdmission is issued only after the caller validates Git identity
// and ownership conflicts. It is trusted mutation evidence, not tool JSON.
func sessionLaneAdmission(next pebblestore.SessionSnapshot, allocated bool) *pebblestore.WorktreeAdmissionEvidence {
	if !next.WorktreeEnabled {
		return nil
	}
	kind := "legacy"
	if allocated {
		kind = "allocated"
	}
	return &pebblestore.WorktreeAdmissionEvidence{Kind: kind, Path: next.WorktreeRootPath, SourcePath: next.WorkspacePath, OwnerSessionID: next.ID, Branch: next.WorktreeBranch}
}
