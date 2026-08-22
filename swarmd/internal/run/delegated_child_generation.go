package run

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// deterministicDelegatedChildSessionID gives each logical Task generation one
// canonical V3 session identity. Retries recover this identity instead of
// allocating a second writer.
func deterministicDelegatedChildSessionID(accountScopeID, logicalTaskID string, generation int) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(accountScopeID) + "\x00" + strings.TrimSpace(logicalTaskID) + "\x00" + fmt.Sprint(generation)))
	raw := hex.EncodeToString(digest[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:32])
}

func delegatedGenerationRecordFromCurrentLaunch(launch taskLaunchPrepared, session pebblestore.SessionSnapshot) pebblestore.DelegatedChildGenerationRecord {
	metadata := session.Metadata
	worktreeBranch := ""
	if launch.TaskBase != nil {
		worktreeBranch = strings.TrimSpace(session.WorktreeBranch)
	}
	record := pebblestore.DelegatedChildGenerationRecord{
		AccountScopeID:                 strings.TrimSpace(session.AccountScopeID),
		LogicalTaskID:                  strings.TrimSpace(launch.LogicalTaskID),
		ProgramID:                      strings.TrimSpace(launch.ProgramID),
		JobID:                          strings.TrimSpace(launch.ProgramJobID),
		SessionID:                      strings.TrimSpace(session.ID),
		ParentSessionID:                strings.TrimSpace(mapString(metadata, "parent_session_id")),
		ParentRunID:                    strings.TrimSpace(launch.ParentRunID),
		PermissionPrincipalID:          strings.TrimSpace(launch.PermissionSessionID),
		PermissionScopeID:              strings.TrimSpace(session.AccountScopeID),
		ReservationSessionID:           strings.TrimSpace(launch.ReservationSessionID),
		ReservationRunID:               strings.TrimSpace(launch.ParentRunID),
		ReservationCallID:              strings.TrimSpace(launch.TaskCallID),
		WorkspacePath:                  strings.TrimSpace(session.WorkspacePath),
		WorktreeBranch:                 worktreeBranch,
		ParentBranch:                   strings.TrimSpace(mapString(metadata, "parent_branch")),
		ImmutableBaseCommit:            strings.TrimSpace(mapString(metadata, "base_commit")),
		ManagedArtifactParentSessionID: strings.TrimSpace(mapString(metadata, "managed_artifact_parent_session_id")),
		ManagedArtifactCollectionID:    strings.TrimSpace(mapString(metadata, "managed_artifact_collection_id")),
		ManagedArtifactVariantID:       strings.TrimSpace(mapString(metadata, "managed_artifact_variant_id")),
		ManagedArtifactTaskCallID:      strings.TrimSpace(mapString(metadata, "managed_artifact_task_call_id")),
		ManagedArtifactProgramID:       strings.TrimSpace(mapString(metadata, "managed_artifact_program_id")),
		ManagedArtifactProgramJobID:    strings.TrimSpace(mapString(metadata, "managed_artifact_program_job_id")),
	}
	if record.WorkspacePath == "" {
		record.WorkspacePath = strings.TrimSpace(session.WorktreeRootPath)
	}
	return record
}
