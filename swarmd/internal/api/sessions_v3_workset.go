package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type sessionsV3WorksetRequest struct {
	SessionIDs []string                   `json:"session_ids,omitempty"`
	Workspace  sessionsV3WorksetWorkspace `json:"workspace,omitempty"`
	Recent     sessionsV3WorksetRecent    `json:"recent,omitempty"`
	History    sessionsV3WorksetHistory   `json:"history,omitempty"`
}

type sessionsV3WorksetWorkspace struct {
	WorkspacePath  string   `json:"workspace_path,omitempty"`
	WorkspacePaths []string `json:"workspace_paths,omitempty"`
}

type sessionsV3WorksetRecent struct {
	Limit           int    `json:"limit,omitempty"`
	BeforeUpdatedAt *int64 `json:"before_updated_at,omitempty"`
	BeforeSessionID string `json:"before_session_id,omitempty"`
}

type sessionsV3WorksetHistory struct {
	Mode                  string `json:"mode,omitempty"`
	MaxMessagesPerSession int    `json:"max_messages_per_session,omitempty"`
	MaxEventsPerSession   int    `json:"max_events_per_session,omitempty"`
	ManifestPolicy        string `json:"manifest_policy,omitempty"`
	IncludeEvents         bool   `json:"include_events,omitempty"`
}

func (s *Server) handleSessionsV3Workset(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req sessionsV3WorksetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options := pebblestore.V3SessionWorksetOptions{
		AccountScopeID:        principal.AccountScopeID,
		SessionIDs:            req.SessionIDs,
		WorkspacePath:         req.Workspace.WorkspacePath,
		WorkspacePaths:        req.Workspace.WorkspacePaths,
		RecentLimit:           req.Recent.Limit,
		RecentBeforeUpdatedAt: req.Recent.BeforeUpdatedAt,
		RecentBeforeSessionID: strings.TrimSpace(req.Recent.BeforeSessionID),
		History: pebblestore.V3SessionWorksetHistoryOptions{
			Mode:                  req.History.Mode,
			MaxMessagesPerSession: req.History.MaxMessagesPerSession,
			MaxEventsPerSession:   req.History.MaxEventsPerSession,
			ManifestPolicy:        req.History.ManifestPolicy,
			IncludeEvents:         req.History.IncludeEvents,
		},
	}
	workset, err := s.sessions.BuildSessionWorkset(options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	permissionsBySession, err := s.sessionsV3WorksetPendingPermissions(workset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionsV3WorksetResponse(workset, permissionsBySession))
}

func (s *Server) sessionsV3WorksetPendingPermissions(workset pebblestore.V3SessionWorksetResult) (map[string]any, error) {
	permissionsBySession := map[string]any{}
	if s == nil || s.perm == nil {
		return permissionsBySession, nil
	}
	for sessionID := range workset.SessionsByID {
		permissions, err := s.perm.ListPending(sessionID, 200)
		if err != nil {
			return nil, err
		}
		permissionsBySession[sessionID] = permissions
	}
	return permissionsBySession, nil
}

func sessionsV3WorksetResponse(workset pebblestore.V3SessionWorksetResult, permissionsBySession map[string]any) map[string]any {
	if permissionsBySession == nil {
		permissionsBySession = map[string]any{}
	}
	return map[string]any{
		"ok":                            true,
		"sessions_by_id":                workset.SessionsByID,
		"projections_by_session":        workset.ProjectionsBySession,
		"messages_by_session":           workset.MessagesBySession,
		"events_by_session":             workset.EventsBySession,
		"plans_by_session":              map[string]any{},
		"plan_revisions_by_session":     map[string]any{},
		"permissions_by_session":        permissionsBySession,
		"usage_by_session":              map[string]any{},
		"preferences_by_session":        map[string]any{},
		"agent_model_policy_by_session": map[string]any{},
		"run_intents_by_session":        workset.RunIntentsBySession,
		"history_manifests_by_session":  workset.HistoryManifestsBySession,
		"history_chunks_by_id":          workset.HistoryChunksByID,
		"omissions":                     workset.Omissions,
		"pagination":                    workset.Pagination,
		"watermarks":                    workset.Watermarks,
		"session_order":                 workset.SessionOrder,
	}
}
