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

func sessionsV3SyncBootstrapOptions(principal identity.Principal, req sessionsV3SyncBootstrapRequest) (sessionsV3ResolvedWorksetOptions, any, []string, error) {
	selector := normalizeSessionsV3SyncSelector(req.SelectorKind, req.Selector, req.SessionIDs, req.Global, req.Workspace, req.Recent)
	worksetReq := sessionsV3WorksetRequest{
		SessionIDs:    selector.SessionIDs,
		Global:        selector.Global,
		Workspace:     sessionsV3WorksetWorkspace{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths},
		Recent:        selector.Recent,
		History:       req.History,
		Resources:     req.Resources,
		IncludeActive: req.IncludeActive,
	}
	if strings.TrimSpace(selector.CWDPath) != "" {
		paths, err := canonicalSessionsV3TUIWorksetPaths(sessionsV3TUIWorksetScope{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths, CWDPath: selector.CWDPath})
		if err != nil {
			return sessionsV3ResolvedWorksetOptions{}, nil, nil, err
		}
		worksetReq.Workspace = sessionsV3WorksetWorkspace{WorkspacePaths: paths}
	}
	options, err := sessionsV3WorksetOptionsFromRequest(principal, worksetReq)
	if err != nil {
		return sessionsV3ResolvedWorksetOptions{}, nil, nil, err
	}
	options.Principal = principal
	options.Surface = normalizeV3SyncSurface(req.Surface)
	if strings.TrimSpace(selector.CWDPath) != "" || strings.TrimSpace(selector.Kind) == "tui" {
		options.Store.RestrictSessionIDsToWorkspacePaths = true
	}
	return options, selector, sessionsV3SyncResourceSet(req.Resources, req.History, req.IncludeActive), nil
}

func sessionsV3SyncHydrateOptions(principal identity.Principal, req sessionsV3SyncHydrateRequest) (sessionsV3ResolvedWorksetOptions, any, []string, error) {
	if len(req.SessionIDs) == 0 {
		return sessionsV3ResolvedWorksetOptions{}, nil, nil, errors.New("sync hydrate requires session_ids")
	}
	selector := normalizeSessionsV3SyncSelector(req.SelectorKind, req.Selector, req.SessionIDs, false, sessionsV3WorksetWorkspace{}, sessionsV3WorksetRecent{})
	selector.Kind = "session_ids"
	selector.SessionIDs = req.SessionIDs
	options, err := sessionsV3WorksetOptionsFromRequest(principal, sessionsV3WorksetRequest{
		SessionIDs:    req.SessionIDs,
		History:       req.History,
		Resources:     req.Resources,
		IncludeActive: req.IncludeActive,
	})
	if err != nil {
		return sessionsV3ResolvedWorksetOptions{}, nil, nil, err
	}
	options.Principal = principal
	options.Surface = normalizeV3SyncSurface(req.Surface)
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

func (s *Server) sessionsV3SyncSnapshotResponse(options sessionsV3ResolvedWorksetOptions, selector any, resources []string, known map[string]sessionsV3KnownState) (map[string]any, error) {
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
	response, err := s.sessionsV3WorksetResponseForResult(options, workset, snapshotEndpointCursor)
	if err != nil {
		return nil, err
	}
	response["sync_scope"] = map[string]any{
		"surface":              scope.Surface,
		"stream_kind":          scope.StreamKind,
		"selector_filter_hash": scope.SelectorFilterHash,
		"resource_set":         scope.ResourceSet,
	}
	response["scope_id"] = scope.SelectorFilterHash + ":" + scope.ResourceSet
	response["selector"] = selector
	response["known_sessions"] = known
	response["tombstones_by_session"] = map[string]any{}
	response["replay_instructions"] = map[string]any{
		"stream_path":                        V3SyncStreamPath,
		"transport":                          "http_post",
		"after_endpoint_cursor":              snapshotEndpointCursor,
		"bootstrap_required_on_cursor_error": true,
	}
	return response, nil
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

var _ = pebblestore.V3SessionWorksetOptions{}
