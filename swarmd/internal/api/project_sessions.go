package api

import (
	"errors"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Project conversations carry runtime identity, not filesystem authority. An
// execution target is admitted separately when a repository task is deployed.
func (s *Server) resolveProjectConversationBinding(p identity.Principal, req *sessionsV3CreateRequest) (sessionsV3PrimaryBinding, error) {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if !p.Valid() || s.sessions == nil || s.sessions.Store() == nil {
		return sessionsV3PrimaryBinding{}, errors.New("project session authority unavailable")
	}
	project, found, err := s.sessions.Store().GetProject(p.AccountScopeID, req.ProjectID)
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	if !found || project == nil || project.AccountID != p.AccountScopeID {
		return sessionsV3PrimaryBinding{}, errors.New("project not found")
	}
	if req.AgentName == "" {
		req.AgentName = agentruntime.SwarmOrchestratorAgentID
	}
	if req.AgentName != agentruntime.SwarmOrchestratorAgentID {
		return sessionsV3PrimaryBinding{}, errors.New("project conversations require system-orchestrator")
	}
	if req.WorkspacePath != "" || req.WorkspaceName != "" || req.WorkspaceID != "" || req.WorkspaceBindingID != "" || req.HostWorkspacePath != "" || req.RuntimeWorkspacePath != "" || req.TargetKind != "" || req.TargetRelationship != "" || req.WorktreeMode != "" && req.WorktreeMode != "off" || req.WorktreeUseCurrentBranch != nil || req.WorktreeBaseBranch != "" || req.WorktreeBranchName != "" || req.WorktreeExistingPath != "" || req.Purpose != "" {
		return sessionsV3PrimaryBinding{}, errors.New("project conversations cannot bind a workspace or worktree")
	}
	local, found, err := s.swarmLocalNode()
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	if !found || strings.TrimSpace(local.SwarmID) == "" {
		return sessionsV3PrimaryBinding{}, errors.New("project conversation local runtime unavailable")
	}
	if req.SwarmID != "" && req.SwarmID != local.SwarmID {
		return sessionsV3PrimaryBinding{}, errors.New("project conversations require the local runtime")
	}
	return sessionsV3PrimaryBinding{RuntimeSwarmID: local.SwarmID}, nil
}

// A request ID alone must not approve a retained request from an older run.
// Pending records remain the reconnect authority; nothing is auto-approved.
func (s *Server) validateProjectPermissionReply(p identity.Principal, sessionID, permissionID string) error {
	session, found, err := s.requireSessionV3Access(p, sessionID)
	if err != nil || !found {
		return errors.New("permission session unavailable")
	}
	if pebblestore.ProjectConversationID(session) == "" {
		return nil
	}
	pending, err := s.perm.ListPending(sessionID, 1000)
	if err != nil {
		return err
	}
	active, found, err := s.sessions.GetSessionActiveRunIntent(sessionID)
	if err != nil || !found {
		return errors.New("permission run is no longer active")
	}
	for _, record := range pending {
		if record.ID == permissionID && record.SessionID == sessionID && record.RunID != "" && record.RunID == active.RunID && record.CallID != "" {
			return nil
		}
	}
	return errors.New("permission request is stale or belongs to another run")
}
