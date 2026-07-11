package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const sessionsV3SearchMaxLimit = 50

type sessionsV3SearchRequest struct {
	Query           string                     `json:"query,omitempty"`
	Queries         []string                   `json:"queries,omitempty"`
	State           string                     `json:"state,omitempty"`
	ArchivedMode    string                     `json:"archived_mode,omitempty"`
	Archived        *bool                      `json:"archived,omitempty"`
	Global          bool                       `json:"global,omitempty"`
	Workspace       sessionsV3WorksetWorkspace `json:"workspace,omitempty"`
	WorkspacePath   string                     `json:"workspace_path,omitempty"`
	WorkspacePaths  []string                   `json:"workspace_paths,omitempty"`
	FromUpdatedAt   *int64                     `json:"from_updated_at,omitempty"`
	ToUpdatedAt     *int64                     `json:"to_updated_at,omitempty"`
	BeforeUpdatedAt *int64                     `json:"before_updated_at,omitempty"`
	BeforeSessionID string                     `json:"before_session_id,omitempty"`
	Cursor          string                     `json:"cursor,omitempty"`
	Limit           int                        `json:"limit,omitempty"`
}

func (s *Server) handleSessionsV3Search(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3SearchPrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3SearchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options, err := sessionsV3SearchOptionsFromRequest(principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.sessions.SearchSessions(options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"items":      result.Items,
		"pagination": result.Pagination,
		"summary":    result.Summary,
	})
}

func (s *Server) sessionsV3SearchPrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
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

func sessionsV3SearchOptionsFromRequest(principal identity.Principal, req sessionsV3SearchRequest) (pebblestore.V3SessionSearchOptions, error) {
	workspaceReq := req.Workspace
	if strings.TrimSpace(req.WorkspacePath) != "" {
		workspaceReq.WorkspacePath = req.WorkspacePath
	}
	if len(req.WorkspacePaths) > 0 {
		workspaceReq.WorkspacePaths = append(workspaceReq.WorkspacePaths, req.WorkspacePaths...)
	}
	workspacePaths, err := canonicalSessionsV3WorksetWorkspacePaths(workspaceReq)
	if err != nil {
		return pebblestore.V3SessionSearchOptions{}, err
	}
	if req.Global && len(workspacePaths) > 0 {
		return pebblestore.V3SessionSearchOptions{}, errors.New("session search global selector cannot be combined with workspace_path or workspace_paths")
	}
	if !req.Global && len(workspacePaths) == 0 {
		return pebblestore.V3SessionSearchOptions{}, errors.New("session search requires explicit workspace_path, workspace_paths, or global=true")
	}
	limit := req.Limit
	if limit <= 0 || limit > sessionsV3SearchMaxLimit {
		limit = sessionsV3SearchMaxLimit
	}
	beforeUpdatedAt := req.BeforeUpdatedAt
	beforeSessionID := strings.TrimSpace(req.BeforeSessionID)
	if strings.TrimSpace(req.Cursor) != "" {
		cursorUpdatedAt, cursorSessionID, err := pebblestore.DecodeV3SessionSearchCursor(req.Cursor)
		if err != nil {
			return pebblestore.V3SessionSearchOptions{}, err
		}
		beforeUpdatedAt = cursorUpdatedAt
		beforeSessionID = cursorSessionID
	}
	archivedMode := strings.TrimSpace(strings.ToLower(req.ArchivedMode))
	if archivedMode == "" && req.Archived != nil {
		if *req.Archived {
			archivedMode = "only"
		} else {
			archivedMode = "exclude"
		}
	}
	return pebblestore.V3SessionSearchOptions{
		AccountScopeID:  principal.AccountScopeID,
		UserID:          principal.UserID,
		Query:           req.Query,
		Queries:         req.Queries,
		State:           req.State,
		ArchivedMode:    archivedMode,
		Global:          req.Global,
		WorkspacePaths:  workspacePaths,
		FromUpdatedAt:   req.FromUpdatedAt,
		ToUpdatedAt:     req.ToUpdatedAt,
		BeforeUpdatedAt: beforeUpdatedAt,
		BeforeSessionID: beforeSessionID,
		Limit:           limit,
	}, nil
}
