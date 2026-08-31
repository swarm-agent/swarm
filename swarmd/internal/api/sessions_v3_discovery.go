package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3DiscoveryRequest struct {
	SessionIDs []string                   `json:"session_ids,omitempty"`
	Global     bool                       `json:"global,omitempty"`
	Workspace  sessionsV3WorksetWorkspace `json:"workspace,omitempty"`
	Recent     sessionsV3WorksetRecent    `json:"recent,omitempty"`
}

func (s *Server) handleSessionsV3Discovery(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3DiscoveryPrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3DiscoveryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options, err := sessionsV3DiscoveryOptionsFromRequest(principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workset, err := s.sessions.BuildSessionWorkset(options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	snapshotEndpointCursor, err := s.sessions.CurrentRealtimeOutboxCursor()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	signedSnapshotEndpointCursor, err := s.signV3SyncEndpointCursorFromLegacy(v3SyncCursorScopeForRealtime(principal, "desktop"), snapshotEndpointCursor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionsV3DiscoveryResponse(workset, signedSnapshotEndpointCursor))
}

func (s *Server) sessionsV3DiscoveryPrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return identity.Principal{}, false
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return identity.Principal{}, false
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return identity.Principal{}, false
	}
	return principal, true
}

func sessionsV3DiscoveryOptionsFromRequest(principal identity.Principal, req sessionsV3DiscoveryRequest) (pebblestore.V3SessionWorksetOptions, error) {
	workspacePaths, err := canonicalSessionsV3WorksetWorkspacePaths(req.Workspace)
	if err != nil {
		return pebblestore.V3SessionWorksetOptions{}, err
	}
	if req.Global && len(workspacePaths) > 0 {
		return pebblestore.V3SessionWorksetOptions{}, errors.New("session discovery global selector cannot be combined with workspace_path or workspace_paths")
	}
	if req.Recent.Limit > 0 && len(workspacePaths) == 0 && !req.Global {
		return pebblestore.V3SessionWorksetOptions{}, errors.New("session discovery recent selector requires explicit workspace_path, workspace_paths, or global=true")
	}
	return pebblestore.V3SessionWorksetOptions{
		AccountScopeID:        principal.AccountScopeID,
		UserID:                principal.UserID,
		SessionIDs:            req.SessionIDs,
		WorkspacePaths:        workspacePaths,
		RecentLimit:           req.Recent.Limit,
		RecentBeforeUpdatedAt: req.Recent.BeforeUpdatedAt,
		RecentBeforeSessionID: strings.TrimSpace(req.Recent.BeforeSessionID),
		History: pebblestore.V3SessionWorksetHistoryOptions{
			Mode:           pebblestore.V3SessionWorksetHistoryModeNone,
			ManifestPolicy: pebblestore.V3SessionWorksetManifestPolicyManifest,
		},
	}, nil
}

type sessionsV3DiscoverySession struct {
	ID                      string                                 `json:"id"`
	UserID                  string                                 `json:"user_id,omitempty"`
	AccountScopeID          string                                 `json:"account_scope_id,omitempty"`
	WorkspacePath           string                                 `json:"workspace_path"`
	WorkspaceName           string                                 `json:"workspace_name"`
	WorkspaceGrants         []pebblestore.WorkspaceGrant           `json:"workspace_grants,omitempty"`
	WorkspaceUsage          []pebblestore.WorkspaceUsageProjection `json:"workspace_usage,omitempty"`
	TemporaryWorkspaceRoots []string                               `json:"temporary_workspace_roots,omitempty"`
	Title                   string                                 `json:"title"`
	Mode                    string                                 `json:"mode"`
	WorktreeEnabled         bool                                   `json:"worktree_enabled,omitempty"`
	WorktreeRootPath        string                                 `json:"worktree_root_path,omitempty"`
	WorktreeBaseBranch      string                                 `json:"worktree_base_branch,omitempty"`
	WorktreeBranch          string                                 `json:"worktree_branch,omitempty"`
	Metadata                map[string]any                         `json:"metadata,omitempty"`
	CreatedAt               int64                                  `json:"created_at"`
	UpdatedAt               int64                                  `json:"updated_at"`
	MessageCount            int                                    `json:"message_count"`
	LastMessageAt           int64                                  `json:"last_message_at"`
	Lifecycle               *pebblestore.SessionLifecycleSnapshot  `json:"lifecycle,omitempty"`
}

func sessionsV3DiscoveryResponse(workset pebblestore.V3SessionWorksetResult, snapshotEndpointCursor string) map[string]any {
	return map[string]any{
		"ok":                       true,
		"rev":                      workset.Rev,
		"snapshot_endpoint_cursor": snapshotEndpointCursor,
		"sessions_by_id":           sessionsV3DiscoverySessions(workset.SessionsByID),
		"projections_by_session":   workset.ProjectionsBySession,
		"pagination":               workset.Pagination,
		"watermarks":               workset.Watermarks,
		"session_order":            workset.SessionOrder,
	}
}

func sessionsV3DiscoverySessions(source map[string]pebblestore.SessionSnapshot) map[string]sessionsV3DiscoverySession {
	out := make(map[string]sessionsV3DiscoverySession, len(source))
	for id, session := range source {
		out[id] = sessionsV3DiscoverySession{
			ID:                      session.ID,
			UserID:                  session.UserID,
			AccountScopeID:          session.AccountScopeID,
			WorkspacePath:           session.WorkspacePath,
			WorkspaceName:           session.WorkspaceName,
			WorkspaceGrants:         pebblestore.NormalizeSessionWorkspaceGrants(session),
			WorkspaceUsage:          pebblestore.WorkspaceUsageFromGrants(pebblestore.NormalizeSessionWorkspaceGrants(session)),
			TemporaryWorkspaceRoots: session.TemporaryWorkspaceRoots,
			Title:                   session.Title,
			Mode:                    session.Mode,
			WorktreeEnabled:         session.WorktreeEnabled,
			WorktreeRootPath:        session.WorktreeRootPath,
			WorktreeBaseBranch:      session.WorktreeBaseBranch,
			WorktreeBranch:          session.WorktreeBranch,
			Metadata:                session.Metadata,
			CreatedAt:               session.CreatedAt,
			UpdatedAt:               session.UpdatedAt,
			MessageCount:            session.MessageCount,
			LastMessageAt:           session.LastMessageAt,
			Lifecycle:               session.Lifecycle,
		}
	}
	return out
}
