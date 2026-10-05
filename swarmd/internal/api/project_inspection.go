package api

import (
	"context"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// ResolveProjectInspection reauthorizes each call, including resumed turns.
// Project membership, tool arguments and previous inspection receipts grant no
// authority without the catalog, durable attempt and Git ownership checks.
func (s *Server) ResolveProjectInspection(ctx context.Context, p identity.Principal, parentID string, req tool.ProjectInspectionRequest) (tool.ProjectInspectionTarget, error) {
	var result tool.ProjectInspectionTarget
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !p.Valid() || p.Type != identity.PrincipalTypeUser || parentID == "" {
		return result, errors.New("project inspection requires an authenticated project conversation")
	}
	parent, found, err := s.sessions.GetSession(parentID)
	if err != nil || !found {
		return result, errors.New("project inspection parent unavailable")
	}
	if err := s.validateProjectInspectionCaller(p, parent, req.ProjectID); err != nil {
		return result, err
	}
	proj, found, err := s.sessions.Store().GetProject(p.AccountScopeID, req.ProjectID)
	if err != nil || !found {
		return result, errors.New("inspection project unavailable")
	}
	if req.TaskID == "" {
		if req.SessionID != "" || req.AttemptID != "" || req.HeadCommit != "" {
			return result, errors.New("task_id required for committed-result inspection")
		}
		if req.WorkspacePath == "" && req.WorkspaceID == "" {
			return result, errors.New("select an explicit workspace_id or workspace_path using list_sources")
		}
		source, err := s.resolveProjectTaskSource(p, proj, req.WorkspacePath, req.WorkspaceID, req.WorkspaceGeneration, false)
		if err != nil {
			return result, err
		}
		req.WorkspacePath, req.WorkspaceID, req.WorkspaceGeneration = source.Path, source.WorkspaceID, source.WorkspaceGeneration
		return tool.ProjectInspectionTarget{Reference: req, Root: source.Path}, nil
	}
	if req.AttemptID == "" || req.SessionID == "" {
		return result, errors.New("task inspection requires exact attempt_id and source_session_id from wait_tasks/get_task")
	}
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, req.ProjectID, req.TaskID)
	if err != nil || !found || task.Archived {
		return result, errors.New("linked task unavailable or archived")
	}
	task.EnsureTaskAttempts()
	a := task.ActiveAttempt()
	if a == nil || a.ID != req.AttemptID || a.SessionID != req.SessionID || task.SessionID != req.SessionID {
		return result, errors.New("task attempt/session reference is stale or mismatched; inspect get_task before selecting a replacement")
	}
	if task.Status != "needs_review" && task.Status != "completed" {
		return result, errors.New("task result is not quiescent for inspection")
	}
	source := task.SourceWorkspace
	if source.Path == "" || source.WorkspaceID == "" || source.WorkspaceGeneration <= 0 {
		return result, errors.New("task result has no durable catalog source binding")
	}
	if req.WorkspacePath != "" && req.WorkspacePath != source.Path || req.WorkspaceID != "" && req.WorkspaceID != source.WorkspaceID || req.WorkspaceGeneration != 0 && req.WorkspaceGeneration != source.WorkspaceGeneration {
		return result, errors.New("task source identity mismatch")
	}
	if _, err := s.resolveProjectTaskSource(p, proj, source.Path, source.WorkspaceID, source.WorkspaceGeneration, true); err != nil {
		return result, err
	}
	child, found, err := s.sessions.GetSession(req.SessionID)
	if err != nil || !found || child.UserID != p.UserID || !child.WorktreeEnabled {
		return result, errors.New("task result session unavailable or not isolated")
	}
	if err := verifyProjectTaskSession(task, child, p.AccountScopeID); err != nil {
		return result, err
	}
	if state, found, err := s.sessions.GetSessionRunState(child.ID); err != nil {
		return result, err
	} else if found && (state.Status == pebblestore.V3RunIntentRunning || state.Status == pebblestore.V3RunIntentPendingExecutor || state.Status == pebblestore.V3RunIntentWaitingTasks) {
		return result, errors.New("task result session still has an active run; wait for quiescence before inspecting")
	}
	claims, err := s.sessions.Store().InspectWorktreeOwnership(p.AccountScopeID, p.UserID, []string{child.WorktreeRootPath})
	if err != nil || len(claims) != 1 || claims[0].OwnerSessionID != child.ID || claims[0].ClaimantSessionID != "" {
		return result, errors.New("task result ownership missing, changed or reserved")
	}
	validator, ok := s.worktrees.(interface {
		ValidateSessionRepositoryLane(string, string, string, string) error
		TaskCommitDescendsFrom(string, string, string) (bool, error)
	})
	if !ok {
		return result, errors.New("task result Git ownership validator unavailable")
	}
	if err := validator.ValidateSessionRepositoryLane(source.Path, child.WorktreeRootPath, child.ID, child.WorktreeBranch); err != nil {
		return result, err
	}
	state, err := s.worktrees.InspectTaskWorkspace(child.WorktreeRootPath)
	if err != nil || !state.Clean || state.HeadCommit == "" || state.BranchName != child.WorktreeBranch {
		return result, errors.New("task result is dirty, missing or changed")
	}
	if req.HeadCommit != "" && req.HeadCommit != state.HeadCommit {
		return result, errors.New("task result head_commit mismatch")
	}
	base, _ := child.Metadata["base_commit"].(string)
	if strings.TrimSpace(base) == "" || base != task.BaseCommit || base == state.HeadCommit {
		return result, errors.New("task result has no committed changes beyond its captured base")
	}
	if ok, err := validator.TaskCommitDescendsFrom(child.WorktreeRootPath, base, state.HeadCommit); err != nil || !ok {
		return result, errors.New("task result does not descend from captured base")
	}
	req.WorkspacePath, req.WorkspaceID, req.WorkspaceGeneration = source.Path, source.WorkspaceID, source.WorkspaceGeneration
	req.HeadCommit = state.HeadCommit
	return tool.ProjectInspectionTarget{Reference: req, Root: child.WorktreeRootPath, Base: base, Branch: state.BranchName}, nil
}

// validateProjectInspectionCaller grants only source inspection admission, not
// project mutation authority. Task conversations prove their own durable current
// attempt; they need not be the conversation that produced the selected result.
func (s *Server) validateProjectInspectionCaller(p identity.Principal, caller pebblestore.SessionSnapshot, projectID string) error {
	if projectID == "" || caller.AccountScopeID != p.AccountScopeID || caller.UserID != p.UserID {
		return errors.New("project inspection caller ownership mismatch")
	}
	if pebblestore.ProjectConversationID(caller) != "" {
		if err := s.sessions.Store().ValidateProjectConversation(caller, p.AccountScopeID, p.UserID); err != nil {
			return err
		}
		if pebblestore.ProjectConversationID(caller) != projectID {
			return errors.New("inspection project does not match the current conversation")
		}
		return nil
	}
	taskID, _ := caller.Metadata["task_id"].(string)
	if taskID == "" || caller.Metadata["project_id"] != projectID {
		return errors.New("project inspection requires a bound project task conversation")
	}
	task, found, err := s.sessions.Store().GetProjectTask(p.AccountScopeID, projectID, taskID)
	if err != nil || !found || task.Archived {
		return errors.New("project inspection caller task unavailable or archived")
	}
	task.EnsureTaskAttempts()
	attempt := task.ActiveAttempt()
	if attempt == nil || attempt.SessionID != caller.ID || task.SessionID != caller.ID {
		return errors.New("project inspection caller attempt is stale or mismatched")
	}
	if attemptID, _ := caller.Metadata["task_attempt_id"].(string); attemptID != "" && attemptID != attempt.ID {
		return errors.New("project inspection caller attempt is stale or mismatched")
	}
	return verifyProjectTaskSession(task, caller, p.AccountScopeID)
}
