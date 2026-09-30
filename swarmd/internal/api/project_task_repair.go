package api

import (
	"errors"
	"fmt"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// validateProjectTaskRecovery does not forge delegated parentage or widen tool
// recovery. Only the retained task attempt and captured catalog source admit it.
func (s *Server) validateProjectTaskRecovery(p identity.Principal, task *pebblestore.ProjectTaskRecord, source *pebblestore.ProjectTaskRecoverySource) error {
	if !p.Valid() || p.Type != "user" || task == nil || source == nil || task.AccountID != p.AccountScopeID || source.HeadCommit == "" || source.BaseCommit == "" || source.HeadCommit == source.BaseCommit {
		return errors.New("incomplete task repair identity")
	}
	task.EnsureTaskAttempts()
	associated := false
	for _, attempt := range task.Attempts {
		if attempt.SessionID == source.SessionID && attempt.WorkspacePath == source.WorkspacePath && attempt.WorktreeBranch == source.Branch && attempt.BaseCommit == source.BaseCommit && attempt.BaseBranch == source.TargetBranch {
			associated = true
		}
	}
	if !associated {
		return errors.New("repair source is not an exact retained task attempt")
	}
	owned, found, err := s.sessions.Store().GetSession(source.SessionID)
	if err != nil {
		return err
	}
	binding := task.SourceWorkspace
	project, exists, err := s.sessions.Store().GetProject(p.AccountScopeID, task.ProjectID)
	if err != nil || !exists {
		return errors.New("repair project unavailable")
	}
	if err := s.revalidateProjectTaskSource(p, project, task); err != nil {
		return err
	}
	if !found || owned.ID != source.SessionID || owned.UserID != p.UserID || owned.AccountScopeID != p.AccountScopeID || owned.Metadata["project_id"] != task.ProjectID || owned.Metadata["task_id"] != task.ID || owned.Metadata["swarm_v3_source_workspace_path"] != binding.Path || owned.Metadata["swarm_v3_source_workspace_id"] != binding.WorkspaceID || fmt.Sprint(owned.Metadata["swarm_v3_source_workspace_generation"]) != fmt.Sprint(binding.WorkspaceGeneration) || !owned.WorktreeEnabled || owned.WorktreeRootPath != source.WorkspacePath || owned.WorktreeBranch != source.Branch || owned.WorktreeBaseBranch != source.TargetBranch || owned.Metadata["swarm_v3_worktree_owner_session_id"] != owned.ID || owned.Metadata["base_commit"] != source.BaseCommit {
		return errors.New("repair originating session ownership/source mismatch")
	}
	claims, err := s.sessions.Store().InspectWorktreeOwnership(p.AccountScopeID, p.UserID, []string{source.WorkspacePath})
	if err != nil || len(claims) != 1 || claims[0].OwnerSessionID != source.SessionID || claims[0].ClaimantSessionID != "" {
		return errors.New("repair source ownership is missing, changed or reserved")
	}
	validator, ok := s.worktrees.(interface {
		ValidateSessionRepositoryLane(string, string, string, string) error
		TaskCommitDescendsFrom(string, string, string) (bool, error)
	})
	if !ok {
		return errors.New("repair Git ownership validator unavailable")
	}
	if err := validator.ValidateSessionRepositoryLane(binding.Path, source.WorkspacePath, source.SessionID, source.Branch); err != nil {
		return err
	}
	state, err := s.worktrees.InspectTaskWorkspace(source.WorkspacePath)
	if err != nil || !state.Clean || state.HeadCommit != source.HeadCommit || state.BranchName != source.Branch {
		return errors.New("repair committed source changed or dirty")
	}
	descends, err := validator.TaskCommitDescendsFrom(source.WorkspacePath, source.BaseCommit, source.HeadCommit)
	if err != nil || !descends {
		return errors.New("repair source does not descend from captured base")
	}
	target, err := s.worktrees.InspectTaskWorkspace(binding.Path)
	if err != nil || !target.Clean || target.BranchName != source.TargetBranch || target.HeadCommit != source.TargetHead {
		return errors.New("repair captured target changed or dirty")
	}
	return nil
}
