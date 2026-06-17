package api

import (
	"errors"
	"net/http"
	"sort"
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
	Snapshot  pebblestore.V3SyncSnapshotOptions
	Principal identity.Principal
	Surface   string
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
		Snapshot: pebblestore.V3SyncSnapshotOptions{
			AccountScopeID:        principal.AccountScopeID,
			Global:                selector.Global || strings.TrimSpace(selector.Kind) == "global",
			SessionIDs:            selector.SessionIDs,
			WorkspacePaths:        workspacePaths,
			RecentLimit:           selector.Recent.Limit,
			RecentBeforeUpdatedAt: selector.Recent.BeforeUpdatedAt,
			RecentBeforeSessionID: strings.TrimSpace(selector.Recent.BeforeSessionID),
			History:               history,
			IncludeRunIntents:     req.Resources.RunIntents || req.IncludeActive,
			IncludeActiveSessions: req.IncludeActive,
			IncludeActivePlan:     req.Resources.ActivePlan,
			IncludePlanRevisions:  req.Resources.PlanRevisions,
		},
		Principal: principal,
		Surface:   normalizeV3SyncSurface(req.Surface),
	}

	if strings.TrimSpace(selector.CWDPath) != "" || strings.TrimSpace(selector.Kind) == "tui" {
		options.Snapshot.RestrictSessionIDsToWorkspacePaths = true
	}

	return options, selector, sessionsV3SyncResourceSet(req.Resources, req.History, req.IncludeActive), nil
}

func sessionsV3SyncHydrateOptions(principal identity.Principal, req sessionsV3SyncHydrateRequest) (sessionsV3ResolvedSyncOptions, any, []string, error) {
	ids := canonicalV3SyncSessionIDs(req.SessionIDs)
	if len(ids) == 0 {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, errors.New("sync hydrate requires session_ids")
	}
	selector := sessionsV3SyncSelector{
		Kind:       "session_ids",
		SessionIDs: ids,
	}

	history, err := sessionsV3SyncHistoryOptionsFromRequest(req.History, req.Resources)
	if err != nil {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, err
	}

	options := sessionsV3ResolvedSyncOptions{
		Snapshot: pebblestore.V3SyncSnapshotOptions{
			AccountScopeID:        principal.AccountScopeID,
			SessionIDs:            ids,
			History:               history,
			IncludeRunIntents:     req.Resources.RunIntents || req.IncludeActive,
			IncludeActiveSessions: req.IncludeActive,
			IncludeActivePlan:     req.Resources.ActivePlan,
			IncludePlanRevisions:  req.Resources.PlanRevisions,
		},
		Principal: principal,
		Surface:   normalizeV3SyncSurface(req.Surface),
	}

	return options, selector, sessionsV3SyncResourceSet(req.Resources, req.History, req.IncludeActive), nil
}

func canonicalV3SyncSessionIDs(sessionIDs []string) []string {
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
	if strings.TrimSpace(selector.Kind) == "global" {
		selector.Global = true
	}
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
	snapshot, err := s.sessions.BuildSyncSnapshot(options.Snapshot)
	if err != nil {
		return nil, err
	}
	scope := v3SyncCursorScopeForSnapshot(options.Principal, options.Surface, "v3.sync.snapshot", selector, resources)
	if err := s.validateSessionsV3KnownEndpointCursors(scope, known); err != nil {
		return nil, err
	}
	snapshotEndpointCursor, err := s.signV3SyncEndpointCursor(scope, snapshot.Rev)
	if err != nil {
		return nil, err
	}

	permissionsBySession := map[string]any{}
	for sessionID, permissions := range snapshot.PermissionsBySession {
		permissionsBySession[sessionID] = permissions
	}

	usageBySession := map[string]any{}
	for sessionID, summary := range snapshot.UsageBySession {
		usageBySession[sessionID] = summary
	}

	preferencesBySession := map[string]any{}
	agentModelPolicyBySession := map[string]any{}
	for sessionID, session := range snapshot.SessionsByID {
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

	plansBySession := map[string]any{}
	for sessionID, plan := range snapshot.PlansBySession {
		plansBySession[sessionID] = plan
	}
	planRevisionsBySession := map[string]any{}
	for sessionID, revisions := range snapshot.PlanRevisionsBySession {
		planRevisionsBySession[sessionID] = revisions
	}

	response := map[string]any{
		"ok":                            true,
		"rev":                           snapshot.Rev,
		"snapshot_endpoint_cursor":      snapshotEndpointCursor,
		"sessions_by_id":                snapshot.SessionsByID,
		"projections_by_session":        snapshot.ProjectionsBySession,
		"messages_by_session":           snapshot.MessagesBySession,
		"events_by_session":             snapshot.EventsBySession,
		"plans_by_session":              plansBySession,
		"plan_revisions_by_session":     planRevisionsBySession,
		"permissions_by_session":        permissionsBySession,
		"usage_by_session":              usageBySession,
		"preferences_by_session":        preferencesBySession,
		"agent_model_policy_by_session": agentModelPolicyBySession,
		"run_intents_by_session":        snapshot.RunIntentsBySession,
		"history_manifests_by_session":  snapshot.HistoryManifestsBySession,
		"history_chunks_by_id":          snapshot.HistoryChunksByID,
		"omissions":                     snapshot.Omissions,
		"pagination":                    snapshot.Pagination,
		"watermarks":                    snapshot.Watermarks,
		"session_order":                 snapshot.SessionOrder,
	}

	response["sync_scope"] = map[string]any{
		"surface":              scope.Surface,
		"stream_kind":          scope.StreamKind,
		"selector_filter_hash": scope.SelectorFilterHash,
		"resource_set":         scope.ResourceSet,
	}
	response["scope_id"] = scope.SelectorFilterHash + ":" + scope.ResourceSet
	tombstones := map[string]any{}
	for sessionID, tombstone := range snapshot.TombstonesBySession {
		if !sessionsV3SyncTombstoneMatchesSelector(tombstone, selector.(sessionsV3SyncSelector)) {
			continue
		}
		tombstones[sessionID] = tombstone
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
	if resources.Messages || historyMode == pebblestore.V3SyncSnapshotHistoryModeTail || historyMode == pebblestore.V3SyncSnapshotHistoryModeFull {
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

func sessionsV3SyncHistoryOptionsFromRequest(req sessionsV3WorksetHistory, resources sessionsV3WorksetResources) (pebblestore.V3SyncSnapshotHistoryOptions, error) {
	mode := strings.TrimSpace(strings.ToLower(req.Mode))
	if mode == "" && resources.Messages {
		mode = pebblestore.V3SyncSnapshotHistoryModeTail
	}
	maxMessages := req.MaxMessagesPerSession
	if mode == "" {
		mode = pebblestore.V3SyncSnapshotHistoryModeNone
	}
	switch mode {
	case pebblestore.V3SyncSnapshotHistoryModeNone:
		maxMessages = 0
	case pebblestore.V3SyncSnapshotHistoryModeTail:
		if maxMessages <= 0 {
			maxMessages = sessionsV3WorksetMaxResourcePageSize
		}
	case pebblestore.V3SyncSnapshotHistoryModeFull:
		if maxMessages <= 0 {
			return pebblestore.V3SyncSnapshotHistoryOptions{}, errors.New("sync snapshot history.mode=full requires max_messages_per_session")
		}
	default:
		return pebblestore.V3SyncSnapshotHistoryOptions{}, errors.New("unsupported sync snapshot history mode " + mode)
	}
	if maxMessages > sessionsV3WorksetMaxResourcePageSize {
		return pebblestore.V3SyncSnapshotHistoryOptions{}, errors.New("sync snapshot max_messages_per_session cannot exceed 200")
	}
	includeEvents := req.IncludeEvents || resources.Events
	maxEvents := req.MaxEventsPerSession
	if includeEvents && maxEvents <= 0 {
		if resources.Events {
			maxEvents = sessionsV3WorksetMaxResourcePageSize
		} else {
			return pebblestore.V3SyncSnapshotHistoryOptions{}, errors.New("sync snapshot include_events requires max_events_per_session")
		}
	}
	if maxEvents > sessionsV3WorksetMaxResourcePageSize {
		return pebblestore.V3SyncSnapshotHistoryOptions{}, errors.New("sync snapshot max_events_per_session cannot exceed 200")
	}
	return pebblestore.V3SyncSnapshotHistoryOptions{
		Mode:                  mode,
		MaxMessagesPerSession: maxMessages,
		MaxEventsPerSession:   maxEvents,
		ManifestPolicy:        req.ManifestPolicy,
		IncludeMessages:       mode == pebblestore.V3SyncSnapshotHistoryModeTail || mode == pebblestore.V3SyncSnapshotHistoryModeFull,
		IncludeEvents:         includeEvents,
	}, nil
}
