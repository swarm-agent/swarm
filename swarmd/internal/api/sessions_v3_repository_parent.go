package api

import (
	"strings"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Resolve only an authenticated immediate parent and its exact recorded lane.
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
	// Regular Coder terminal facts live in the parent's exact task-call lineage.
	callID := sessionsV3MetadataString(owner.Metadata, "parent_task_call_id")
	if calls, ok := parent.Metadata["task_launches"].(map[string]any); ok && callID != "" {
		if call, ok := calls[callID].(map[string]any); ok {
			if rows, ok := call["launches"].([]any); ok && len(rows) <= 50 {
				for _, raw := range rows {
					row, ok := raw.(map[string]any)
					if !ok || firstNonEmpty(sessionsV3MetadataString(row, "child_session_id"), sessionsV3MetadataString(row, "session_id")) != owner.ID {
						continue
					}
					if state := firstNonEmpty(sessionsV3MetadataString(row, "child_state"), sessionsV3MetadataString(row, "phase")); state != "" {
						item.Lifecycle = state
					}
					break
				}
			}
		}
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
	// Adoption must not relabel workers or integration lanes captured against an
	// older parent lane. Resolve exact durable evidence, never path ancestry.
	candidate := item.SourcePath
	if item.Kind == "lane" {
		candidate = item.WorkspacePath
	}
	if candidate != parent.WorktreeRootPath {
		page, err := s.sessions.ExactRepositoryHistory(pebblestore.RepositoryHistoryQuery{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
			ParentSessionID: parent.ID, Limit: 1,
		}, candidate)
		if err == nil {
			for _, row := range page.Sessions {
				if row.Session.ID == parent.ID && row.Session.WorktreeEnabled && row.Session.WorktreeRootPath == candidate {
					parent = row.Session
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
		item.WorkspaceID = sessionsV3MetadataString(parent.Metadata, "swarm_v3_source_workspace_id")
		item.WorkspaceGeneration = 0
		for _, grant := range parent.WorkspaceGrants {
			if grant.Path == source && grant.WorkspaceID == item.WorkspaceID {
				item.WorkspaceGeneration = grant.WorkspaceGeneration
			}
		}
	}
	if item.WorkspacePath == parent.WorktreeRootPath && item.SourcePath == source {
		item.laneOwnerID = parent.ID
		item.Branch = parent.WorktreeBranch
		item.BaseCommit = firstNonEmpty(sessionsV3MetadataString(parent.Metadata, "swarm_v3_worktree_base_commit"), sessionsV3MetadataString(parent.Metadata, "base_commit"))
	}
	return item
}
