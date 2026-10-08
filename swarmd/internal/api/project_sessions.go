package api

import (
	"errors"
	"net/http"
	"strings"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
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
	for _, key := range []string{"task_id", "task_attempt_id", "parent_session_id", "workspace_path", "worktree_root_path", "worktree_enabled"} {
		if _, supplied := req.Metadata[key]; supplied {
			return sessionsV3PrimaryBinding{}, errors.New("project conversation metadata cannot supply task or workspace authority")
		}
	}
	if req.AgentName == "" {
		req.AgentName = agentruntime.SwarmOrchestratorAgentID
	}
	if req.AgentName != agentruntime.SwarmOrchestratorAgentID {
		return sessionsV3PrimaryBinding{}, errors.New("project conversations require system-orchestrator")
	}
	if len(req.LegacyManagedWorktreeRequested) != 0 || req.WorkspacePath != "" || req.WorkspaceName != "" || req.WorkspaceID != "" || req.WorkspaceBindingID != "" || req.HostWorkspacePath != "" || req.RuntimeWorkspacePath != "" || req.TargetKind != "" || req.TargetRelationship != "" || req.WorktreeMode != "" && req.WorktreeMode != "off" || req.WorktreeUseCurrentBranch != nil || req.WorktreeBaseBranch != "" || req.WorktreeBranchName != "" || req.WorktreeExistingPath != "" || req.Purpose != "" {
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

// Project replies resolve one request only, never a persistent account policy.
// The store checks run freshness under the canonical session mutation lock.
func (s *Server) validateProjectPermissionReply(p identity.Principal, sessionID, action string) error {
	session, found, err := s.requireSessionV3Access(p, sessionID)
	if err != nil || !found {
		return errors.New("permission session unavailable")
	}
	if pebblestore.ProjectConversationID(session) != "" {
		switch strings.ToLower(strings.TrimSpace(action)) {
		case permission.ActionAllowOnce, permission.ActionDenyOnce, permission.ActionCancel, "allow", "deny", "approve", "reject", "yes", "no":
		default:
			return errors.New("project conversation permissions require a single-request decision")
		}
	}
	return nil
}

// The project route is an adapter to the canonical V3 create/list boundaries;
// it never writes a project primary-session pointer or archives a conversation.
func (s *Server) handleProjectConversations(w http.ResponseWriter, r *http.Request, p identity.Principal, projectID string, tail []string) {
	if len(tail) != 0 {
		http.NotFound(w, r)
		return
	}
	if !p.Valid() || s.sessions == nil || s.sessions.Store() == nil {
		writeError(w, http.StatusUnauthorized, errors.New("project session authority unavailable"))
		return
	}
	project, found, err := s.sessions.Store().GetProject(p.AccountScopeID, projectID)
	if err != nil || !found || project == nil || project.AccountID != p.AccountScopeID {
		writeError(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	switch r.Method {
	case http.MethodPost:
		if !s.requireScopeAny(w, r, "sessions:write") {
			return
		}
		var req sessionsV3CreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.ProjectID != "" && strings.TrimSpace(req.ProjectID) != projectID {
			writeError(w, http.StatusBadRequest, errors.New("project identity mismatch"))
			return
		}
		req.ProjectID = projectID
		s.createSessionsV3Primary(w, r, p, req)
	case http.MethodGet:
		if !s.requireScopeAny(w, r, "sessions:read") {
			return
		}
		if requested := r.URL.Query().Get("project_id"); requested != "" && requested != projectID {
			writeError(w, http.StatusBadRequest, errors.New("project identity mismatch"))
			return
		}
		request := r.Clone(r.Context())
		urlCopy := *r.URL
		query := urlCopy.Query()
		query.Set("project_id", projectID)
		urlCopy.RawQuery = query.Encode()
		request.URL = &urlCopy
		s.handleSessionsV3PrimaryList(w, request, p)
	default:
		methodNotAllowed(w)
	}
}
