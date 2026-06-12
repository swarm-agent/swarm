package api

import (
	"errors"
	"net/http"
	"path/filepath"
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

type sessionsV3TUIWorksetRequest struct {
	SessionIDs []string                  `json:"session_ids,omitempty"`
	Scope      sessionsV3TUIWorksetScope `json:"scope"`
	Recent     sessionsV3WorksetRecent   `json:"recent,omitempty"`
	History    sessionsV3WorksetHistory  `json:"history,omitempty"`
}

type sessionsV3TUIWorksetScope struct {
	WorkspacePath  string   `json:"workspace_path,omitempty"`
	WorkspacePaths []string `json:"workspace_paths,omitempty"`
	CWDPath        string   `json:"cwd_path,omitempty"`
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
	principal, ok := s.sessionsV3WorksetPrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3WorksetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options, err := sessionsV3WorksetOptionsFromRequest(principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.writeSessionsV3Workset(w, options)
}

func (s *Server) handleSessionsV3TUIWorkset(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3WorksetPrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3TUIWorksetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	workspacePaths, err := canonicalSessionsV3TUIWorksetPaths(req.Scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options, err := sessionsV3WorksetOptionsFromRequest(principal, sessionsV3WorksetRequest{
		SessionIDs: req.SessionIDs,
		Workspace:  sessionsV3WorksetWorkspace{WorkspacePaths: workspacePaths},
		Recent:     req.Recent,
		History:    req.History,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options.RestrictSessionIDsToWorkspacePaths = true
	s.writeSessionsV3Workset(w, options)
}

func (s *Server) sessionsV3WorksetPrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
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

func sessionsV3WorksetOptionsFromRequest(principal identity.Principal, req sessionsV3WorksetRequest) (pebblestore.V3SessionWorksetOptions, error) {
	workspacePaths, err := canonicalSessionsV3WorksetWorkspacePaths(req.Workspace)
	if err != nil {
		return pebblestore.V3SessionWorksetOptions{}, err
	}
	if req.Recent.Limit > 0 && len(workspacePaths) == 0 {
		return pebblestore.V3SessionWorksetOptions{}, errors.New("workset recent selector requires explicit workspace_path or workspace_paths")
	}
	options := pebblestore.V3SessionWorksetOptions{
		AccountScopeID:        principal.AccountScopeID,
		SessionIDs:            req.SessionIDs,
		WorkspacePaths:        workspacePaths,
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
	return options, nil
}

func (s *Server) writeSessionsV3Workset(w http.ResponseWriter, options pebblestore.V3SessionWorksetOptions) {
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
	usageBySession, err := s.sessionsV3WorksetUsageSummaries(workset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	preferencesBySession, agentModelPolicyBySession := s.sessionsV3WorksetPreferencesAndPolicies(workset)
	plansBySession, planRevisionsBySession, err := s.sessionsV3WorksetPlans(workset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionsV3WorksetResponse(workset, permissionsBySession, usageBySession, preferencesBySession, agentModelPolicyBySession, plansBySession, planRevisionsBySession))
}

func canonicalSessionsV3WorksetWorkspacePaths(workspace sessionsV3WorksetWorkspace) ([]string, error) {
	paths := make([]string, 0, 1+len(workspace.WorkspacePaths))
	seen := map[string]struct{}{}
	var err error
	paths, err = appendCanonicalSessionsV3WorksetPath(paths, seen, "workspace_path", workspace.WorkspacePath)
	if err != nil {
		return nil, err
	}
	for _, path := range workspace.WorkspacePaths {
		paths, err = appendCanonicalSessionsV3WorksetPath(paths, seen, "workspace_paths", path)
		if err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func canonicalSessionsV3TUIWorksetPaths(scope sessionsV3TUIWorksetScope) ([]string, error) {
	paths, err := canonicalSessionsV3WorksetWorkspacePaths(sessionsV3WorksetWorkspace{
		WorkspacePath:  scope.WorkspacePath,
		WorkspacePaths: scope.WorkspacePaths,
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, path := range paths {
		seen[path] = struct{}{}
	}
	paths, err = appendCanonicalSessionsV3WorksetPath(paths, seen, "cwd_path", scope.CWDPath)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("tui workset requires at least one explicit workspace_path or cwd_path selector")
	}
	return paths, nil
}

func appendCanonicalSessionsV3WorksetPath(paths []string, seen map[string]struct{}, kind, value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return paths, nil
	}
	if !filepath.IsAbs(value) {
		return nil, errors.New("workset " + kind + " must be an absolute canonical path")
	}
	cleaned := filepath.Clean(value)
	if cleaned != value {
		return nil, errors.New("workset " + kind + " must be canonical")
	}
	if _, ok := seen[cleaned]; ok {
		return paths, nil
	}
	seen[cleaned] = struct{}{}
	return append(paths, cleaned), nil
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

func (s *Server) sessionsV3WorksetUsageSummaries(workset pebblestore.V3SessionWorksetResult) (map[string]any, error) {
	usageBySession := map[string]any{}
	if s == nil || s.sessions == nil {
		return usageBySession, nil
	}
	for sessionID := range workset.SessionsByID {
		summary, ok, err := s.sessions.GetUsageSummary(sessionID)
		if err != nil {
			return nil, err
		}
		if ok {
			usageBySession[sessionID] = summary
		}
	}
	return usageBySession, nil
}

func (s *Server) sessionsV3WorksetPreferencesAndPolicies(workset pebblestore.V3SessionWorksetResult) (map[string]any, map[string]any) {
	preferencesBySession := map[string]any{}
	agentModelPolicyBySession := map[string]any{}
	for sessionID, session := range workset.SessionsByID {
		session.Preference = normalizeSessionsV3ModelPreference(session.Preference)
		preference := session.Preference
		contextWindow := 0
		maxOutputTokens := 0
		if s != nil && s.model != nil {
			if resolved, err := s.model.ResolvePreference(session.Preference); err == nil {
				preference = normalizeSessionsV3ModelPreference(resolved.Preference)
				contextWindow = resolved.ContextWindow
				maxOutputTokens = resolved.MaxOutputTokens
			}
		}
		agentModelPolicy := s.sessionsV3AgentModelPolicy(session, preference, contextWindow, maxOutputTokens)
		if agentModelPolicy.Locked {
			preference = agentModelPolicy.Preference
			contextWindow = agentModelPolicy.ContextWindow
			maxOutputTokens = agentModelPolicy.MaxOutputTokens
		}
		preferencesBySession[sessionID] = map[string]any{
			"preference":        preference,
			"context_window":    contextWindow,
			"max_output_tokens": maxOutputTokens,
		}
		agentModelPolicyBySession[sessionID] = agentModelPolicy
	}
	return preferencesBySession, agentModelPolicyBySession
}

func (s *Server) sessionsV3WorksetPlans(workset pebblestore.V3SessionWorksetResult) (map[string]any, map[string]any, error) {
	plansBySession := map[string]any{}
	planRevisionsBySession := map[string]any{}
	if s == nil || s.sessions == nil {
		return plansBySession, planRevisionsBySession, nil
	}
	for sessionID := range workset.SessionsByID {
		plan, ok, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		plansBySession[sessionID] = plan
		revisions, err := s.sessions.ListPlanRevisions(sessionID, plan.ID, 100)
		if err != nil {
			return nil, nil, err
		}
		planRevisionsBySession[sessionID] = revisions
	}
	return plansBySession, planRevisionsBySession, nil
}

func sessionsV3WorksetResponse(workset pebblestore.V3SessionWorksetResult, permissionsBySession map[string]any, usageBySession map[string]any, preferencesBySession map[string]any, agentModelPolicyBySession map[string]any, plansBySession map[string]any, planRevisionsBySession map[string]any) map[string]any {
	if permissionsBySession == nil {
		permissionsBySession = map[string]any{}
	}
	if usageBySession == nil {
		usageBySession = map[string]any{}
	}
	if preferencesBySession == nil {
		preferencesBySession = map[string]any{}
	}
	if agentModelPolicyBySession == nil {
		agentModelPolicyBySession = map[string]any{}
	}
	if plansBySession == nil {
		plansBySession = map[string]any{}
	}
	if planRevisionsBySession == nil {
		planRevisionsBySession = map[string]any{}
	}
	return map[string]any{
		"ok":                            true,
		"rev":                           workset.Rev,
		"sessions_by_id":                workset.SessionsByID,
		"projections_by_session":        workset.ProjectionsBySession,
		"messages_by_session":           workset.MessagesBySession,
		"events_by_session":             workset.EventsBySession,
		"plans_by_session":              plansBySession,
		"plan_revisions_by_session":     planRevisionsBySession,
		"permissions_by_session":        permissionsBySession,
		"usage_by_session":              usageBySession,
		"preferences_by_session":        preferencesBySession,
		"agent_model_policy_by_session": agentModelPolicyBySession,
		"run_intents_by_session":        workset.RunIntentsBySession,
		"history_manifests_by_session":  workset.HistoryManifestsBySession,
		"history_chunks_by_id":          workset.HistoryChunksByID,
		"omissions":                     workset.Omissions,
		"pagination":                    workset.Pagination,
		"watermarks":                    workset.Watermarks,
		"session_order":                 workset.SessionOrder,
	}
}
