package api

import (
	"errors"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const v3SyncDefaultSurface = "desktop"

type sessionsV3SyncBootstrapRequest struct {
	Surface       string                          `json:"surface,omitempty"`
	SelectorKind  string                          `json:"selector_kind,omitempty"`
	Selector      sessionsV3SyncSelector          `json:"selector,omitempty"`
	SessionIDs    []string                        `json:"session_ids,omitempty"`
	Global        bool                            `json:"global,omitempty"`
	Workspace     sessionsV3WorksetWorkspace      `json:"workspace,omitempty"`
	Recent        sessionsV3WorksetRecent         `json:"recent,omitempty"`
	History       sessionsV3WorksetHistory        `json:"history,omitempty"`
	Resources     sessionsV3WorksetResources      `json:"resources,omitempty"`
	IncludeActive bool                            `json:"include_active,omitempty"`
	KnownSessions map[string]sessionsV3KnownState `json:"known_sessions,omitempty"`
}

type sessionsV3SyncHydrateRequest struct {
	Surface       string                          `json:"surface,omitempty"`
	SelectorKind  string                          `json:"selector_kind,omitempty"`
	Selector      sessionsV3SyncSelector          `json:"selector,omitempty"`
	SessionIDs    []string                        `json:"session_ids"`
	History       sessionsV3WorksetHistory        `json:"history,omitempty"`
	Resources     sessionsV3WorksetResources      `json:"resources,omitempty"`
	IncludeActive bool                            `json:"include_active,omitempty"`
	KnownSessions map[string]sessionsV3KnownState `json:"known_sessions,omitempty"`
}

type sessionsV3SyncSelector struct {
	Kind           string                  `json:"kind,omitempty"`
	Global         bool                    `json:"global,omitempty"`
	WorkspacePath  string                  `json:"workspace_path,omitempty"`
	WorkspacePaths []string                `json:"workspace_paths,omitempty"`
	CWDPath        string                  `json:"cwd_path,omitempty"`
	SessionIDs     []string                `json:"session_ids,omitempty"`
	Recent         sessionsV3WorksetRecent `json:"recent,omitempty"`
}

type sessionsV3KnownState struct {
	AppliedSeq     uint64 `json:"applied_seq,omitempty"`
	HighWatermark  uint64 `json:"high_watermark,omitempty"`
	EndpointCursor string `json:"endpoint_cursor,omitempty"`
}

type sessionsV3ResolvedSyncOptions struct {
	Store                pebblestore.V3SessionWorksetOptions
	Principal            identity.Principal
	Surface              string
	IncludeActivePlan    bool
	IncludePlanRevisions bool
}

func (s *Server) handleSessionsV3SyncBootstrap(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3SyncPrincipal(w, r, http.MethodPost)
	if !ok {
		return
	}
	var req sessionsV3SyncBootstrapRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options, selector, resources, err := sessionsV3SyncBootstrapOptions(principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, err := s.sessionsV3SyncSnapshotResponse(options, selector, resources, req.KnownSessions)
	if err != nil {
		writeV3SyncCursorHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionsV3SyncHydrate(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3SyncPrincipal(w, r, http.MethodPost)
	if !ok {
		return
	}
	var req sessionsV3SyncHydrateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options, selector, resources, err := sessionsV3SyncHydrateOptions(principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, err := s.sessionsV3SyncSnapshotResponse(options, selector, resources, req.KnownSessions)
	if err != nil {
		writeV3SyncCursorHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) sessionsV3SyncPrincipal(w http.ResponseWriter, r *http.Request, method string) (identity.Principal, bool) {
	if s == nil || s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return identity.Principal{}, false
	}
	if r.Method != method {
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

func sessionsV3SyncBootstrapOptions(principal identity.Principal, req sessionsV3SyncBootstrapRequest) (sessionsV3ResolvedSyncOptions, any, []string, error) {
	selector := normalizeSessionsV3SyncSelector(req.SelectorKind, req.Selector, req.SessionIDs, req.Global, req.Workspace, req.Recent)

	// Resolve workspace paths natively
	workspacePaths, err := canonicalSessionsV3WorksetWorkspacePaths(req.Workspace)
	if err != nil {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, err
	}
	if req.Global && len(workspacePaths) > 0 {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, errors.New("workset global selector cannot be combined with workspace_path or workspace_paths")
	}
	if req.Recent.Limit > 0 && len(workspacePaths) == 0 && !req.Global {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, errors.New("workset recent selector requires explicit workspace_path, workspace_paths, or global=true")
	}

	if strings.TrimSpace(selector.CWDPath) != "" {
		paths, err := canonicalSessionsV3TUIWorksetPaths(sessionsV3TUIWorksetScope{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths, CWDPath: selector.CWDPath})
		if err != nil {
			return sessionsV3ResolvedSyncOptions{}, nil, nil, err
		}
		workspacePaths = paths
	}

	history, err := sessionsV3SyncHistoryOptionsFromRequest(req.History, req.Resources)
	if err != nil {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, err
	}

	options := sessionsV3ResolvedSyncOptions{
		Store: pebblestore.V3SessionWorksetOptions{
			AccountScopeID:        principal.AccountScopeID,
			SessionIDs:            selector.SessionIDs,
			WorkspacePaths:        workspacePaths,
			RecentLimit:           selector.Recent.Limit,
			RecentBeforeUpdatedAt: selector.Recent.BeforeUpdatedAt,
			RecentBeforeSessionID: strings.TrimSpace(selector.Recent.BeforeSessionID),
			History:               history,
			IncludeRunIntents:     req.Resources.RunIntents || req.IncludeActive,
			IncludeActiveSessions: req.IncludeActive,
		},
		Principal:            principal,
		Surface:              normalizeV3SyncSurface(req.Surface),
		IncludeActivePlan:    req.Resources.ActivePlan,
		IncludePlanRevisions: req.Resources.PlanRevisions,
	}

	if strings.TrimSpace(selector.CWDPath) != "" || strings.TrimSpace(selector.Kind) == "tui" {
		options.Store.RestrictSessionIDsToWorkspacePaths = true
	}

	return options, selector, sessionsV3SyncResourceSet(req.Resources, req.History, req.IncludeActive), nil
}

func sessionsV3SyncHydrateOptions(principal identity.Principal, req sessionsV3SyncHydrateRequest) (sessionsV3ResolvedSyncOptions, any, []string, error) {
	if len(req.SessionIDs) == 0 {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, errors.New("sync hydrate requires session_ids")
	}
	selector := normalizeSessionsV3SyncSelector(req.SelectorKind, req.Selector, req.SessionIDs, false, sessionsV3WorksetWorkspace{}, sessionsV3WorksetRecent{})
	selector.Kind = "session_ids"
	selector.SessionIDs = req.SessionIDs

	history, err := sessionsV3SyncHistoryOptionsFromRequest(req.History, req.Resources)
	if err != nil {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, err
	}

	options := sessionsV3ResolvedSyncOptions{
		Store: pebblestore.V3SessionWorksetOptions{
			AccountScopeID:        principal.AccountScopeID,
			SessionIDs:            req.SessionIDs,
			History:               history,
			IncludeRunIntents:     req.Resources.RunIntents || req.IncludeActive,
			IncludeActiveSessions: req.IncludeActive,
		},
		Principal:            principal,
		Surface:              normalizeV3SyncSurface(req.Surface),
		IncludeActivePlan:    req.Resources.ActivePlan,
		IncludePlanRevisions: req.Resources.PlanRevisions,
	}

	return options, selector, sessionsV3SyncResourceSet(req.Resources, req.History, req.IncludeActive), nil
}

func normalizeSessionsV3SyncSelector(kind string, selector sessionsV3SyncSelector, sessionIDs []string, global bool, workspace sessionsV3WorksetWorkspace, recent sessionsV3WorksetRecent) sessionsV3SyncSelector {
	if strings.TrimSpace(selector.Kind) == "" {
		selector.Kind = strings.TrimSpace(kind)
	}
	if len(selector.SessionIDs) == 0 {
		selector.SessionIDs = sessionIDs
	}
	if selector.WorkspacePath == "" && len(selector.WorkspacePaths) == 0 {
		selector.WorkspacePath = workspace.WorkspacePath
		selector.WorkspacePaths = workspace.WorkspacePaths
	}
	if selector.Recent.Limit == 0 && recent.Limit != 0 {
		selector.Recent = recent
	}
	selector.Global = selector.Global || global
	if strings.TrimSpace(selector.Kind) == "" {
		switch {
		case selector.Global:
			selector.Kind = "global"
		case len(selector.SessionIDs) > 0:
			selector.Kind = "session_ids"
		case strings.TrimSpace(selector.CWDPath) != "":
			selector.Kind = "tui"
		case selector.Recent.Limit > 0:
			selector.Kind = "recent"
		case strings.TrimSpace(selector.WorkspacePath) != "" || len(selector.WorkspacePaths) > 0:
			selector.Kind = "workspace"
		}
	}
	return selector
}

func (s *Server) sessionsV3SyncSnapshotResponse(options sessionsV3ResolvedSyncOptions, selector any, resources []string, known map[string]sessionsV3KnownState) (map[string]any, error) {
	workset, err := s.sessions.BuildSessionWorkset(options.Store)
	if err != nil {
		return nil, err
	}
	scope := v3SyncCursorScopeForSnapshot(options.Principal, options.Surface, "v3.sync.snapshot", selector, resources)
	if err := s.validateSessionsV3KnownEndpointCursors(scope, known); err != nil {
		return nil, err
	}
	snapshotEndpointCursor, err := s.signV3SyncEndpointCursor(scope, workset.Rev)
	if err != nil {
		return nil, err
	}

	permissionsBySession := map[string]any{}
	if s.perm != nil {
		for sessionID := range workset.SessionsByID {
			permissions, err := s.perm.ListPending(sessionID, 200)
			if err != nil {
				return nil, err
			}
			permissionsBySession[sessionID] = permissions
		}
	}

	usageBySession := map[string]any{}
	if s.sessions != nil {
		for sessionID := range workset.SessionsByID {
			summary, ok, err := s.sessions.GetUsageSummary(sessionID)
			if err != nil {
				return nil, err
			}
			if ok {
				usageBySession[sessionID] = summary
			}
		}
	}

	preferencesBySession := map[string]any{}
	agentModelPolicyBySession := map[string]any{}
	for sessionID, session := range workset.SessionsByID {
		session.Preference = normalizeSessionsV3ModelPreference(session.Preference)
		preference := session.Preference
		contextWindow := 0
		maxOutputTokens := 0
		if s.model != nil {
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

	plansBySession, planRevisionsBySession, err := s.sessionsV3SyncPlans(options, workset)
	if err != nil {
		return nil, err
	}

	response := map[string]any{
		"ok":                            true,
		"rev":                           workset.Rev,
		"snapshot_endpoint_cursor":      snapshotEndpointCursor,
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

	response["sync_scope"] = map[string]any{
		"surface":              scope.Surface,
		"stream_kind":          scope.StreamKind,
		"selector_filter_hash": scope.SelectorFilterHash,
		"resource_set":         scope.ResourceSet,
	}
	response["scope_id"] = scope.SelectorFilterHash + ":" + scope.ResourceSet
	tombstones, err := s.sessionsV3SyncTombstonesBySession(options.Principal.AccountScopeID, selector)
	if err != nil {
		return nil, err
	}
	response["selector"] = selector
	response["known_sessions"] = known
	response["tombstones_by_session"] = tombstones
	response["replay_instructions"] = map[string]any{
		"stream_path":                        V3SyncStreamPath,
		"transport":                          "http_post",
		"after_endpoint_cursor":              snapshotEndpointCursor,
		"bootstrap_required_on_cursor_error": true,
	}
	return response, nil
}

func (s *Server) sessionsV3SyncTombstonesBySession(accountScopeID string, selector any) (map[string]any, error) {
	out := map[string]any{}
	if s == nil || s.sessions == nil {
		return out, nil
	}
	tombstones, err := s.sessions.ListSessionTombstonesForAccount(accountScopeID, 1000)
	if err != nil {
		return nil, err
	}
	resolvedSelector, _ := selector.(sessionsV3SyncSelector)
	for _, tombstone := range tombstones {
		if !sessionsV3SyncTombstoneMatchesSelector(tombstone, resolvedSelector) {
			continue
		}
		out[tombstone.SessionID] = tombstone
	}
	return out, nil
}

func sessionsV3SyncTombstoneMatchesSelector(tombstone pebblestore.V3SessionTombstone, selector sessionsV3SyncSelector) bool {
	if strings.TrimSpace(tombstone.SessionID) == "" {
		return false
	}
	switch strings.TrimSpace(selector.Kind) {
	case "session_ids":
		for _, id := range selector.SessionIDs {
			if strings.TrimSpace(id) == tombstone.SessionID {
				return true
			}
		}
		return false
	case "workspace", "tui":
		paths, _ := canonicalSessionsV3TUIWorksetPaths(sessionsV3TUIWorksetScope{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths, CWDPath: selector.CWDPath})
		if len(paths) == 0 {
			paths, _ = canonicalSessionsV3WorksetWorkspacePaths(sessionsV3WorksetWorkspace{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths})
		}
		if len(paths) == 0 {
			return true
		}
		for _, path := range paths {
			if strings.TrimSpace(tombstone.WorkspacePath) != "" && strings.TrimSpace(path) != "" && strings.TrimSpace(tombstone.WorkspacePath) == strings.TrimSpace(path) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func (s *Server) validateSessionsV3KnownEndpointCursors(scope v3SyncCursorScope, known map[string]sessionsV3KnownState) error {
	for _, state := range known {
		if strings.TrimSpace(state.EndpointCursor) == "" {
			continue
		}
		if _, legacy, err := s.parseV3SyncEndpointCursor(state.EndpointCursor, scope); err != nil {
			return err
		} else if legacy {
			return newV3SyncCursorError("endpoint_cursor_legacy_unsupported", errors.New("known_sessions.endpoint_cursor requires a signed scoped sync cursor"))
		}
	}
	return nil
}

func normalizeV3SyncSurface(surface string) string {
	surface = strings.TrimSpace(surface)
	if surface == "" {
		return v3SyncDefaultSurface
	}
	return surface
}

func sessionsV3SyncResourceSet(resources sessionsV3WorksetResources, history sessionsV3WorksetHistory, includeActive bool) []string {
	out := []string{"sessions", "projections", "membership", "tombstones"}
	historyMode := strings.TrimSpace(strings.ToLower(history.Mode))
	if resources.Messages || historyMode == pebblestore.V3SessionWorksetHistoryModeTail || historyMode == pebblestore.V3SessionWorksetHistoryModeFull {
		out = append(out, "messages")
	}
	if resources.Events || history.IncludeEvents {
		out = append(out, "events")
	}
	if resources.RunIntents || includeActive {
		out = append(out, "run_intents")
	}
	if resources.ActivePlan {
		out = append(out, "active_plan")
	}
	if resources.PlanRevisions {
		out = append(out, "plan_revisions")
	}
	return out
}

func sessionsV3SyncHistoryOptionsFromRequest(req sessionsV3WorksetHistory, resources sessionsV3WorksetResources) (pebblestore.V3SessionWorksetHistoryOptions, error) {
	mode := strings.TrimSpace(strings.ToLower(req.Mode))
	if mode == "" && resources.Messages {
		mode = pebblestore.V3SessionWorksetHistoryModeTail
	}
	maxMessages := req.MaxMessagesPerSession
	if mode == "" {
		mode = pebblestore.V3SessionWorksetHistoryModeNone
	}
	switch mode {
	case pebblestore.V3SessionWorksetHistoryModeNone:
		maxMessages = 0
	case pebblestore.V3SessionWorksetHistoryModeTail:
		if maxMessages <= 0 {
			maxMessages = sessionsV3WorksetMaxResourcePageSize
		}
	case pebblestore.V3SessionWorksetHistoryModeFull:
		if maxMessages <= 0 {
			return pebblestore.V3SessionWorksetHistoryOptions{}, errors.New("workset history.mode=full requires max_messages_per_session")
		}
	default:
		return pebblestore.V3SessionWorksetHistoryOptions{}, errors.New("unsupported workset history mode " + mode)
	}
	if maxMessages > sessionsV3WorksetMaxResourcePageSize {
		return pebblestore.V3SessionWorksetHistoryOptions{}, errors.New("workset max_messages_per_session cannot exceed 200")
	}
	includeEvents := req.IncludeEvents || resources.Events
	maxEvents := req.MaxEventsPerSession
	if includeEvents && maxEvents <= 0 {
		if resources.Events {
			maxEvents = sessionsV3WorksetMaxResourcePageSize
		} else {
			return pebblestore.V3SessionWorksetHistoryOptions{}, errors.New("workset include_events requires max_events_per_session")
		}
	}
	if maxEvents > sessionsV3WorksetMaxResourcePageSize {
		return pebblestore.V3SessionWorksetHistoryOptions{}, errors.New("workset max_events_per_session cannot exceed 200")
	}
	return pebblestore.V3SessionWorksetHistoryOptions{
		Mode:                  mode,
		MaxMessagesPerSession: maxMessages,
		MaxEventsPerSession:   maxEvents,
		ManifestPolicy:        req.ManifestPolicy,
		IncludeMessages:       mode == pebblestore.V3SessionWorksetHistoryModeTail || mode == pebblestore.V3SessionWorksetHistoryModeFull,
		IncludeEvents:         includeEvents,
	}, nil
}

func (s *Server) sessionsV3SyncPlans(options sessionsV3ResolvedSyncOptions, workset pebblestore.V3SessionWorksetResult) (map[string]any, map[string]any, error) {
	plansBySession := map[string]any{}
	planRevisionsBySession := map[string]any{}
	if !options.IncludeActivePlan && !options.IncludePlanRevisions {
		return plansBySession, planRevisionsBySession, nil
	}
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
		if options.IncludeActivePlan {
			plansBySession[sessionID] = plan
		}
		if options.IncludePlanRevisions {
			revisions, err := s.sessions.ListPlanRevisions(sessionID, plan.ID, 100)
			if err != nil {
				return nil, nil, err
			}
			planRevisionsBySession[sessionID] = revisions
		}
	}
	return plansBySession, planRevisionsBySession, nil
}

var _ = pebblestore.V3SessionWorksetOptions{}
