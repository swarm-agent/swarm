package api

import (
	"strings"
	"swarm/packages/swarmd/internal/identity"
)

// Resolve only an authenticated immediate parent and its exact current lane.
// This is read-only provenance, never a grant or guessed path-ancestry fallback.
func (s *Server) resolveRepositoryParentIdentity(principal identity.Principal, item sessionRepositoryItem) sessionRepositoryItem {
	owner, ok, err := s.sessions.GetSession(item.SessionID)
	if err != nil || !ok || owner.AccountScopeID != principal.AccountScopeID || owner.UserID != principal.UserID {
		return item
	}
	parentID := strings.TrimSpace(sessionsV3MetadataString(owner.Metadata, "parent_session_id"))
	if item.Kind == "lane" {
		parentID = owner.ID
	}
	if parentID == "" {
		return item
	}
	parent, ok, err := s.sessions.GetSession(parentID)
	if err != nil || !ok || parent.AccountScopeID != principal.AccountScopeID || parent.UserID != principal.UserID || !parent.WorktreeEnabled {
		return item
	}
	programID := sessionsV3MetadataString(owner.Metadata, "task_program_id")
	jobID := sessionsV3MetadataString(owner.Metadata, "task_program_job_id")
	if programID != "" && jobID != "" {
		if program, found, err := s.sessions.InspectTaskProgram(parentID, programID); err == nil && found && program.ParentSessionID == parentID {
			for _, job := range program.Jobs {
				if job.JobID == jobID && job.ChildSessionID == owner.ID {
					item.Lifecycle = job.State
					if job.IntegrationState != "" {
						item.Lifecycle = job.IntegrationState
					}
					if item.BaseCommit == "" {
						item.BaseCommit = job.ImmutableStageBase
					}
					break
				}
			}
		}
	}
	source := sessionsV3MetadataString(parent.Metadata, "swarm_v3_source_workspace_path")
	if source == "" {
		return item
	}
	if item.SourcePath == parent.WorktreeRootPath {
		item.SourcePath = source
		item.WorkspaceID = ""
		item.WorkspaceGeneration = 0
	}
	if item.WorkspacePath == parent.WorktreeRootPath && item.SourcePath == source {
		item.laneOwnerID = parent.ID
		item.Branch = parent.WorktreeBranch
		item.BaseCommit = firstNonEmpty(sessionsV3MetadataString(parent.Metadata, "swarm_v3_worktree_base_commit"), sessionsV3MetadataString(parent.Metadata, "base_commit"))
	}
	return item
}
