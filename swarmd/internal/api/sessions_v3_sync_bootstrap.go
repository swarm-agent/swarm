package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

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
	Attention     sessionsV3WorksetAttention      `json:"attention,omitempty"`
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

const sessionsV3SyncHydrateMaxSessionViews = 8

type sessionsV3SyncSelector struct {
	Kind           string                     `json:"kind,omitempty"`
	Global         bool                       `json:"global,omitempty"`
	WorkspacePath  string                     `json:"workspace_path,omitempty"`
	WorkspacePaths []string                   `json:"workspace_paths,omitempty"`
	CWDPath        string                     `json:"cwd_path,omitempty"`
	SessionIDs     []string                   `json:"session_ids,omitempty"`
	Recent         sessionsV3WorksetRecent    `json:"recent,omitempty"`
	Attention      sessionsV3WorksetAttention `json:"attention,omitempty"`
}

type sessionsV3WorksetAttention struct {
	PendingPermissions bool `json:"pending_permissions,omitempty"`
}

type sessionsV3KnownState struct {
	AppliedSeq     uint64 `json:"applied_seq,omitempty"`
	HighWatermark  uint64 `json:"high_watermark,omitempty"`
	EndpointCursor string `json:"endpoint_cursor,omitempty"`
}

type sessionsV3ResolvedSyncOptions struct {
	Snapshot                          pebblestore.V3SyncSnapshotOptions
	Principal                         identity.Principal
	Surface                           string
	IncludePermissionSummaryAttention bool
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
	response, err := s.sessionsV3SyncSnapshotResponse(r.Context(), options, selector, resources, req.KnownSessions)
	if err != nil {
		writeV3SyncCursorHTTPError(w, err)
		return
	}
	writeSessionsV3SyncBootstrapJSON(w, response)
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
	response, err := s.sessionsV3SyncSnapshotResponse(r.Context(), options, selector, resources, req.KnownSessions)
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
	selector, workspacePaths, err := canonicalSessionsV3SyncBootstrapSelector(req)
	if err != nil {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, err
	}

	if req.Resources.SessionView {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, errors.New("sync bootstrap does not support resources.session_view; use /v3/sync/hydrate")
	}
	if selector.Attention.PendingPermissions {
		req.Resources.PermissionSummaries = true
	}
	history, err := sessionsV3SyncHistoryOptionsFromRequest(req.History, req.Resources)
	if err != nil {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, err
	}

	global := selector.Global || strings.TrimSpace(selector.Kind) == "global"
	options := sessionsV3ResolvedSyncOptions{
		Snapshot: pebblestore.V3SyncSnapshotOptions{
			AccountScopeID:                   principal.AccountScopeID,
			UserID:                           principal.UserID,
			Global:                           global && selector.Recent.Limit <= 0,
			SessionIDs:                       selector.SessionIDs,
			WorkspacePaths:                   workspacePaths,
			RecentLimit:                      selector.Recent.Limit,
			RecentBeforeUpdatedAt:            selector.Recent.BeforeUpdatedAt,
			RecentBeforeSessionID:            strings.TrimSpace(selector.Recent.BeforeSessionID),
			History:                          history,
			IncludeRunIntents:                req.Resources.RunIntents,
			IncludeCurrentRunState:           req.Resources.CurrentRunState || req.IncludeActive,
			IncludeActivePlan:                req.Resources.ActivePlan,
			IncludeActiveSessions:            req.IncludeActive,
			IncludePinnedSidebarSessions:     normalizeV3SyncSurface(req.Surface) == "desktop",
			IncludeUnresolvedSidebarSessions: normalizeV3SyncSurface(req.Surface) == "desktop" && req.Resources.ActivePlan,
		},
		Principal:                         principal,
		Surface:                           normalizeV3SyncSurface(req.Surface),
		IncludePermissionSummaryAttention: req.Resources.PermissionSummaries,
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
	if req.Resources.SessionView && len(ids) > sessionsV3SyncHydrateMaxSessionViews {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, errors.New("sync hydrate resources.session_view cannot target more than 8 sessions")
	}
	if err := validateSessionsV3SyncHydrateSelector(req, ids); err != nil {
		return sessionsV3ResolvedSyncOptions{}, nil, nil, err
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
			AccountScopeID:         principal.AccountScopeID,
			UserID:                 principal.UserID,
			SessionIDs:             ids,
			History:                history,
			IncludeRunIntents:      req.Resources.RunIntents,
			IncludeCurrentRunState: req.Resources.CurrentRunState || req.IncludeActive || req.Resources.SessionView,
			IncludeSessionView:     req.Resources.SessionView,
			IncludeActivePlan:      req.Resources.ActivePlan,
			// Hydrate is session_ids-targeted. include_active may request compact
			// active run state for requested sessions, but must never widen
			// membership beyond the explicit session_ids selector.
			IncludeActiveSessions: false,
		},
		Principal:                         principal,
		Surface:                           normalizeV3SyncSurface(req.Surface),
		IncludePermissionSummaryAttention: false,
	}

	return options, selector, sessionsV3SyncHydrateResourceSet(req), nil
}

func sessionsV3SyncHydrateResourceSet(req sessionsV3SyncHydrateRequest) []string {
	resources := sessionsV3SyncResourceSet(req.Resources, req.History, req.IncludeActive)
	if req.Resources.SessionView && req.Resources.ActivePlan {
		resources = append(resources, "active_plan")
	}
	return resources
}

func sessionsV3SelectedSessionHydrateRequest(sessionID string) sessionsV3SyncHydrateRequest {
	return sessionsV3SyncHydrateRequest{
		Surface:    "desktop",
		SessionIDs: []string{strings.TrimSpace(sessionID)},
		History: sessionsV3WorksetHistory{
			Mode:                  pebblestore.V3SyncSnapshotHistoryModeTail,
			MaxMessagesPerSession: sessionsV3WorksetMaxResourcePageSize,
			MaxEventsPerSession:   sessionsV3WorksetMaxResourcePageSize,
			ManifestPolicy:        "manifest",
		},
		Resources: sessionsV3WorksetResources{
			Messages:        true,
			Events:          true,
			RunIntents:      true,
			CurrentRunState: true,
			SessionView:     true,
			ActivePlan:      true,
		},
		IncludeActive: true,
	}
}

func sessionsV3SelectedSessionHydrateCursorScope(principal identity.Principal, sessionID string) (v3SyncCursorScope, error) {
	req := sessionsV3SelectedSessionHydrateRequest(sessionID)
	options, selector, resources, err := sessionsV3SyncHydrateOptions(principal, req)
	if err != nil {
		return v3SyncCursorScope{}, err
	}
	return v3SyncCursorScopeForSnapshot(options.Principal, options.Surface, "v3.sync.snapshot", selector, resources), nil
}

func sessionsV3SelectedSessionHydrateResources(sessionID string) ([]string, error) {
	req := sessionsV3SelectedSessionHydrateRequest(sessionID)
	_, _, resources, err := sessionsV3SyncHydrateOptions(identity.Principal{}, req)
	if err != nil {
		return nil, err
	}
	return resources, nil
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

func canonicalSessionsV3SyncBootstrapSelector(req sessionsV3SyncBootstrapRequest) (sessionsV3SyncSelector, []string, error) {
	selector := normalizeSessionsV3SyncSelector(req.SelectorKind, req.Selector, req.SessionIDs, req.Global, req.Workspace, req.Recent, req.Attention)
	if err := validateSessionsV3SyncBootstrapSelectorConflicts(req, selector); err != nil {
		return sessionsV3SyncSelector{}, nil, err
	}
	selector.Kind = strings.TrimSpace(selector.Kind)
	if !sessionsV3SyncSelectorKindAllowed(selector.Kind) {
		return sessionsV3SyncSelector{}, nil, errors.New("unsupported sync selector kind " + selector.Kind)
	}
	selector.SessionIDs = canonicalV3SyncSessionIDs(selector.SessionIDs)
	selector.Recent.BeforeSessionID = strings.TrimSpace(selector.Recent.BeforeSessionID)

	var (
		workspacePaths []string
		err            error
	)
	if strings.TrimSpace(selector.CWDPath) != "" || selector.Kind == "tui" {
		workspacePaths, err = canonicalSessionsV3TUIWorksetPaths(sessionsV3TUIWorksetScope{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths, CWDPath: selector.CWDPath})
	} else {
		workspacePaths, err = canonicalSessionsV3WorksetWorkspacePaths(sessionsV3WorksetWorkspace{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths})
	}
	if err != nil {
		return sessionsV3SyncSelector{}, nil, err
	}
	selector.WorkspacePath = ""
	selector.WorkspacePaths = nil
	if len(workspacePaths) == 1 {
		selector.WorkspacePath = workspacePaths[0]
	} else if len(workspacePaths) > 1 {
		selector.WorkspacePaths = workspacePaths
	}

	global := selector.Global || selector.Kind == "global"
	selector.Global = global
	if global && len(workspacePaths) > 0 {
		return sessionsV3SyncSelector{}, nil, errors.New("workset global selector cannot be combined with workspace_path or workspace_paths")
	}
	if selector.Kind == "workspace" && selector.Recent.Limit <= 0 {
		return sessionsV3SyncSelector{}, nil, errors.New("workset workspace selector requires recent.limit for bounded deterministic selection")
	}
	if (selector.Kind == "recent" || selector.Recent.Limit > 0) && len(workspacePaths) == 0 && !global {
		return sessionsV3SyncSelector{}, nil, errors.New("workset recent selector requires explicit workspace_path, workspace_paths, or global=true")
	}
	return selector, workspacePaths, nil
}

func sessionsV3SyncSelectorKindAllowed(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "", "global", "recent", "session_ids", "workspace", "tui":
		return true
	default:
		return false
	}
}

func validateSessionsV3SyncBootstrapSelectorConflicts(req sessionsV3SyncBootstrapRequest, canonical sessionsV3SyncSelector) error {
	selectorProvided := sessionsV3SyncSelectorHasAnyField(req.Selector)
	if selectorProvided {
		if strings.TrimSpace(req.SelectorKind) != "" && strings.TrimSpace(req.SelectorKind) != strings.TrimSpace(canonical.Kind) {
			return errors.New("sync selector_kind conflicts with selector.kind")
		}
		if len(req.SessionIDs) > 0 && !sameV3SyncSessionIDs(canonicalV3SyncSessionIDs(req.SessionIDs), canonical.SessionIDs) {
			return errors.New("sync selector conflicts with top-level session_ids")
		}
		if req.Global && !canonical.Global {
			return errors.New("sync selector conflicts with top-level global")
		}
		if sessionsV3WorksetWorkspaceHasAnyField(req.Workspace) {
			selectorPaths, selectorErr := canonicalSessionsV3WorksetWorkspacePaths(sessionsV3WorksetWorkspace{WorkspacePath: canonical.WorkspacePath, WorkspacePaths: canonical.WorkspacePaths})
			topPaths, topErr := canonicalSessionsV3WorksetWorkspacePaths(req.Workspace)
			if selectorErr != nil || topErr != nil || !sameV3SyncStrings(selectorPaths, topPaths) {
				return errors.New("sync selector conflicts with top-level workspace")
			}
		}
		if sessionsV3WorksetRecentHasAnyField(req.Recent) && !sameV3SyncRecent(req.Recent, canonical.Recent) {
			return errors.New("sync selector conflicts with top-level recent")
		}
	}
	return nil
}

func validateSessionsV3SyncHydrateSelector(req sessionsV3SyncHydrateRequest, ids []string) error {
	if strings.TrimSpace(req.SelectorKind) != "" && strings.TrimSpace(req.SelectorKind) != "session_ids" {
		return errors.New("sync hydrate selector_kind conflicts with session_ids selector")
	}
	if sessionsV3SyncSelectorHasAnyField(req.Selector) {
		selector := req.Selector
		selector.SessionIDs = canonicalV3SyncSessionIDs(selector.SessionIDs)
		if strings.TrimSpace(selector.Kind) != "" && strings.TrimSpace(selector.Kind) != "session_ids" {
			return errors.New("sync hydrate selector conflicts with session_ids selector")
		}
		if selector.Global || strings.TrimSpace(selector.WorkspacePath) != "" || len(selector.WorkspacePaths) > 0 || strings.TrimSpace(selector.CWDPath) != "" || sessionsV3WorksetRecentHasAnyField(selector.Recent) {
			return errors.New("sync hydrate selector conflicts with session_ids selector")
		}
		if len(selector.SessionIDs) > 0 && !sameV3SyncSessionIDs(selector.SessionIDs, ids) {
			return errors.New("sync hydrate selector conflicts with top-level session_ids")
		}
	}
	return nil
}

func sessionsV3SyncSelectorHasAnyField(selector sessionsV3SyncSelector) bool {
	return strings.TrimSpace(selector.Kind) != "" || selector.Global || strings.TrimSpace(selector.WorkspacePath) != "" || len(selector.WorkspacePaths) > 0 || strings.TrimSpace(selector.CWDPath) != "" || len(selector.SessionIDs) > 0 || sessionsV3WorksetRecentHasAnyField(selector.Recent)
}

func sessionsV3WorksetWorkspaceHasAnyField(workspace sessionsV3WorksetWorkspace) bool {
	return strings.TrimSpace(workspace.WorkspacePath) != "" || len(workspace.WorkspacePaths) > 0
}

func sessionsV3WorksetRecentHasAnyField(recent sessionsV3WorksetRecent) bool {
	return recent.Limit != 0 || recent.BeforeUpdatedAt != nil || strings.TrimSpace(recent.BeforeSessionID) != ""
}

func sameV3SyncSessionIDs(a, b []string) bool {
	return sameV3SyncStrings(canonicalV3SyncSessionIDs(a), canonicalV3SyncSessionIDs(b))
}

func sameV3SyncStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}

func sameV3SyncRecent(a, b sessionsV3WorksetRecent) bool {
	if a.Limit != b.Limit || strings.TrimSpace(a.BeforeSessionID) != strings.TrimSpace(b.BeforeSessionID) {
		return false
	}
	if (a.BeforeUpdatedAt == nil) != (b.BeforeUpdatedAt == nil) {
		return false
	}
	if a.BeforeUpdatedAt != nil && b.BeforeUpdatedAt != nil && *a.BeforeUpdatedAt != *b.BeforeUpdatedAt {
		return false
	}
	return true
}

func normalizeSessionsV3SyncSelector(kind string, selector sessionsV3SyncSelector, sessionIDs []string, global bool, workspace sessionsV3WorksetWorkspace, recent sessionsV3WorksetRecent, attention sessionsV3WorksetAttention) sessionsV3SyncSelector {
	selector.Kind = strings.TrimSpace(selector.Kind)
	if selector.Kind == "" {
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
	selector.Attention.PendingPermissions = selector.Attention.PendingPermissions || attention.PendingPermissions
	selector.Global = selector.Global || global
	if selector.Kind == "global" {
		selector.Global = true
	}
	if selector.Kind == "" {
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

type sessionsV3SyncSnapshotResponseBody struct {
	OK                           bool                                                     `json:"ok"`
	Rev                          uint64                                                   `json:"rev"`
	SnapshotEndpointCursor       string                                                   `json:"snapshot_endpoint_cursor"`
	SessionsByID                 map[string]pebblestore.SessionSnapshot                   `json:"sessions_by_id"`
	ProjectionsBySession         map[string]pebblestore.V3SessionProjection               `json:"projections_by_session"`
	MessagesBySession            map[string][]pebblestore.MessageSnapshot                 `json:"messages_by_session"`
	EventsBySession              map[string][]pebblestore.V3SessionEvent                  `json:"events_by_session"`
	RunIntentsBySession          map[string][]pebblestore.V3SessionRunIntent              `json:"run_intents_by_session"`
	CurrentRunStateBySession     map[string]pebblestore.V3SessionRunState                 `json:"current_run_state_by_session,omitempty"`
	PermissionSummariesBySession map[string]sessionsV3PermissionSummary                   `json:"permission_summaries_by_session,omitempty"`
	Notifications                []pebblestore.NotificationRecord                         `json:"notifications,omitempty"`
	NotificationSummary          *pebblestore.NotificationSummary                         `json:"notification_summary,omitempty"`
	ActiveSessionIDs             []string                                                 `json:"active_session_ids,omitempty"`
	SessionViewsByID             map[string]sessionsV3SessionView                         `json:"session_views_by_id,omitempty"`
	Realtime                     *sessionsV3RealtimeBootstrap                             `json:"realtime,omitempty"`
	HistoryManifestsBySession    map[string][]pebblestore.V3SessionHistoryChunkDescriptor `json:"history_manifests_by_session"`
	HistoryChunksByID            map[string]pebblestore.V3SessionHistoryChunk             `json:"history_chunks_by_id"`
	Omissions                    []pebblestore.V3SyncSnapshotOmission                     `json:"omissions"`
	Pagination                   pebblestore.V3SyncSnapshotPagination                     `json:"pagination"`
	Watermarks                   pebblestore.V3SyncSnapshotWatermarks                     `json:"watermarks"`
	SessionOrder                 []string                                                 `json:"session_order"`
	SyncScope                    sessionsV3SyncScopeResponse                              `json:"sync_scope"`
	ScopeID                      string                                                   `json:"scope_id"`
	Selector                     any                                                      `json:"selector"`
	KnownSessions                map[string]sessionsV3KnownState                          `json:"known_sessions"`
	TombstonesBySession          map[string]pebblestore.V3SessionTombstone                `json:"tombstones_by_session"`
	ReplayInstructions           sessionsV3SyncReplayInstructionsResponse                 `json:"replay_instructions"`
	logTimings                   *sessionsV3SyncBootstrapTimings                          `json:"-"`
}

type sessionsV3RealtimeBootstrap struct {
	StreamPath string            `json:"stream_path"`
	Resume     V3RealtimeMessage `json:"resume"`
}

type sessionsV3SyncScopeResponse struct {
	Surface            string `json:"surface"`
	StreamKind         string `json:"stream_kind"`
	SelectorFilterHash string `json:"selector_filter_hash"`
	ResourceSet        string `json:"resource_set"`
}

type sessionsV3SyncReplayInstructionsResponse struct {
	StreamPath                     string `json:"stream_path"`
	Transport                      string `json:"transport"`
	AfterEndpointCursor            string `json:"after_endpoint_cursor"`
	BootstrapRequiredOnCursorError bool   `json:"bootstrap_required_on_cursor_error"`
}

type sessionsV3AgenticSettings struct {
	Mode                string                      `json:"mode"`
	AgentName           string                      `json:"agent_name"`
	ResolvedAgentName   string                      `json:"resolved_agent_name"`
	RuntimeMode         string                      `json:"runtime_mode,omitempty"`
	StoredPreference    pebblestore.ModelPreference `json:"stored_preference"`
	EffectivePreference pebblestore.ModelPreference `json:"effective_preference"`
	AgentModelPolicy    sessionsV3AgentModelPolicy  `json:"agent_model_policy"`
	ContextWindow       int                         `json:"context_window"`
	MaxOutputTokens     int                         `json:"max_output_tokens"`
	ProjectionSeq       uint64                      `json:"projection_seq"`
}

type sessionsV3SessionView struct {
	AgenticSettings    sessionsV3AgenticSettings        `json:"agentic_settings"`
	PendingPermissions []pebblestore.PermissionRecord   `json:"pending_permissions"`
	UsageSummary       *pebblestore.SessionUsageSummary `json:"usage_summary,omitempty"`
	CurrentRunState    *pebblestore.V3SessionRunState   `json:"current_run_state,omitempty"`
	HasActivePlan      *bool                            `json:"has_active_plan,omitempty"`
	ActivePlan         *pebblestore.SessionPlanSnapshot `json:"active_plan,omitempty"`
}

type sessionsV3PermissionSummary struct {
	SessionID            string `json:"session_id"`
	PendingApprovalCount int    `json:"pending_approval_count"`
	OldestPendingAt      int64  `json:"oldest_pending_at"`
	NewestPendingAt      int64  `json:"newest_pending_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

type sessionsV3SyncResolvedPreference struct {
	Preference      pebblestore.ModelPreference
	ContextWindow   int
	MaxOutputTokens int
}

type sessionsV3SyncPreferenceCacheKey struct {
	Provider    string
	Model       string
	Thinking    string
	ServiceTier string
	ContextMode string
}

func sessionsV3RealtimeBootstrapForSnapshot(endpointCursor string, surface string, selector sessionsV3SyncSelector, resources []string, activeSessionIDs []string) *sessionsV3RealtimeBootstrap {
	endpointCursor = strings.TrimSpace(endpointCursor)
	if endpointCursor == "" {
		return nil
	}
	resourceSet := make([]string, 0, len(resources))
	for _, resource := range resources {
		if v3RealtimeWorksetResourceAllowed(resource) {
			resourceSet = append(resourceSet, resource)
		}
	}
	resourceSet, err := canonicalV3RealtimeWorksetResources(resourceSet)
	if err != nil {
		resourceSet = []string{"membership", "projections", "sessions", "tombstones"}
	}
	subscriptions := sessionsV3RealtimeBootstrapSubscriptions(endpointCursor, selector, activeSessionIDs)
	worksetSelector := sessionsV3RealtimeBootstrapWorksetSelector(selector)
	worksetID := "sync:" + v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(selector))
	return &sessionsV3RealtimeBootstrap{
		StreamPath: V3RealtimeStreamPath,
		Resume: V3RealtimeMessage{
			Protocol:        V3RealtimeProtocol,
			ProtocolVersion: V3RealtimeProtocolVersion,
			Kind:            V3RealtimeKindResume,
			EndpointCursor:  endpointCursor,
			Subscriptions:   subscriptions,
			Worksets: []V3RealtimeWorksetSubscriptionRequest{{
				WorksetID:             worksetID,
				SubscriptionID:        "sync:" + worksetID,
				Surface:               normalizeV3SyncSurface(surface),
				Selector:              worksetSelector,
				Resources:             resourceSet,
				AutoSubscribeSessions: false,
			}},
		},
	}
}

func sessionsV3RealtimeBootstrapSubscriptions(endpointCursor string, selector sessionsV3SyncSelector, activeSessionIDs []string) []V3RealtimeSubscriptionRequest {
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(activeSessionIDs)+len(selector.SessionIDs))
	appendID := func(sessionID string) {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return
		}
		if _, ok := seen[sessionID]; ok {
			return
		}
		seen[sessionID] = struct{}{}
		ids = append(ids, sessionID)
	}
	for _, sessionID := range activeSessionIDs {
		appendID(sessionID)
	}
	for _, sessionID := range selector.SessionIDs {
		appendID(sessionID)
	}
	subscriptions := make([]V3RealtimeSubscriptionRequest, 0, len(ids))
	for _, sessionID := range ids {
		subscriptions = append(subscriptions, V3RealtimeSubscriptionRequest{SessionID: sessionID, SubscriptionID: "sync:session:" + sessionID, EndpointCursor: endpointCursor})
	}
	return subscriptions
}

func sessionsV3RealtimeBootstrapWorksetSelector(selector sessionsV3SyncSelector) V3RealtimeWorksetSelector {
	kind := strings.TrimSpace(selector.Kind)
	out := V3RealtimeWorksetSelector{
		Kind:           kind,
		Global:         selector.Global,
		WorkspacePath:  selector.WorkspacePath,
		WorkspacePaths: selector.WorkspacePaths,
		SessionIDs:     selector.SessionIDs,
		Recent:         selector.Recent,
		Attention:      selector.Attention,
	}
	switch kind {
	case "", "tui":
		if len(out.SessionIDs) > 0 {
			out.Kind = "session_ids"
		} else if out.Recent.Limit > 0 {
			out.Kind = "recent"
		} else if out.Global {
			out.Kind = "global"
		} else {
			out.Kind = "workspace"
		}
	case "workspace":
		if out.Recent.Limit > 0 {
			out.Kind = "recent"
		}
	}
	return out
}

func (s *Server) sessionsV3SyncSnapshotResponse(ctx context.Context, options sessionsV3ResolvedSyncOptions, selector any, resources []string, known map[string]sessionsV3KnownState) (sessionsV3SyncSnapshotResponseBody, error) {
	timings := newSessionsV3SyncBootstrapTimings()
	totalStart := time.Now()
	phaseStart := totalStart
	scope := v3SyncCursorScopeForSnapshot(options.Principal, options.Surface, "v3.sync.snapshot", selector, resources)
	if err := s.validateSessionsV3KnownState(scope, known); err != nil {
		return sessionsV3SyncSnapshotResponseBody{}, err
	}
	if timings != nil {
		timings.scopeDur = time.Since(phaseStart)
	}

	var permissionSummaries map[string]sessionsV3PermissionSummary
	if sessionsV3SyncResourcesInclude(resources, "permission_summaries") {
		var err error
		permissionSummaries, err = s.sessionsV3PermissionSummaries(options.Principal)
		if err != nil {
			return sessionsV3SyncSnapshotResponseBody{}, err
		}
		if options.IncludePermissionSummaryAttention {
			for sessionID := range permissionSummaries {
				options.Snapshot.SessionIDs = append(options.Snapshot.SessionIDs, sessionID)
			}
		}
	}

	phaseStart = time.Now()
	snapshot, err := s.sessions.BuildSyncSnapshotWithContext(ctx, options.Snapshot)
	if err != nil {
		return sessionsV3SyncSnapshotResponseBody{}, err
	}
	if timings != nil {
		timings.snapshotDur = time.Since(phaseStart)
		timings.sessions = len(snapshot.SessionsByID)
		for _, messages := range snapshot.MessagesBySession {
			timings.messages += len(messages)
		}
		for _, intents := range snapshot.RunIntentsBySession {
			timings.runIntents += len(intents)
		}
	}

	phaseStart = time.Now()
	snapshotEndpointCursor, err := s.signV3SyncEndpointCursor(scope, snapshot.Rev)
	if err != nil {
		return sessionsV3SyncSnapshotResponseBody{}, err
	}
	if timings != nil {
		timings.cursorDur = time.Since(phaseStart)
	}

	phaseStart = time.Now()
	tombstones := make(map[string]pebblestore.V3SessionTombstone, len(snapshot.TombstonesBySession))
	selectorValue, _ := selector.(sessionsV3SyncSelector)
	for sessionID, tombstone := range snapshot.TombstonesBySession {
		if !sessionsV3SyncTombstoneMatchesSelector(tombstone, selectorValue) {
			continue
		}
		tombstone.Session = sessionsV3SyncSessionShell(tombstone.Session)
		tombstones[sessionID] = tombstone
	}
	if timings != nil {
		timings.decorateDur = time.Since(phaseStart)
		timings.totalDur = time.Since(totalStart)
	}

	response := sessionsV3SyncSnapshotResponseBody{
		OK:                           true,
		Rev:                          snapshot.Rev,
		SnapshotEndpointCursor:       snapshotEndpointCursor,
		SessionsByID:                 sessionsV3SyncSessionShells(snapshot.SessionsByID),
		ProjectionsBySession:         snapshot.ProjectionsBySession,
		MessagesBySession:            snapshot.MessagesBySession,
		EventsBySession:              snapshot.EventsBySession,
		RunIntentsBySession:          snapshot.RunIntentsBySession,
		CurrentRunStateBySession:     snapshot.CurrentRunStateBySession,
		PermissionSummariesBySession: nil,
		Notifications:                nil,
		NotificationSummary:          nil,
		ActiveSessionIDs:             snapshot.ActiveSessionIDs,
		SessionViewsByID:             nil,
		HistoryManifestsBySession:    snapshot.HistoryManifestsBySession,
		HistoryChunksByID:            snapshot.HistoryChunksByID,
		Omissions:                    snapshot.Omissions,
		Pagination:                   snapshot.Pagination,
		Watermarks:                   snapshot.Watermarks,
		SessionOrder:                 snapshot.SessionOrder,
		SyncScope: sessionsV3SyncScopeResponse{
			Surface:            scope.Surface,
			StreamKind:         scope.StreamKind,
			SelectorFilterHash: scope.SelectorFilterHash,
			ResourceSet:        scope.ResourceSet,
		},
		ScopeID:             scope.SelectorFilterHash + ":" + scope.ResourceSet,
		Selector:            selector,
		KnownSessions:       known,
		TombstonesBySession: tombstones,
		ReplayInstructions: sessionsV3SyncReplayInstructionsResponse{
			StreamPath:                     V3SyncStreamPath,
			Transport:                      "http_post",
			AfterEndpointCursor:            snapshotEndpointCursor,
			BootstrapRequiredOnCursorError: true,
		},
		Realtime: sessionsV3RealtimeBootstrapForSnapshot(snapshotEndpointCursor, options.Surface, selectorValue, resources, snapshot.ActiveSessionIDs),
	}
	if permissionSummaries != nil {
		response.PermissionSummariesBySession = sessionsV3PermissionSummariesVisibleInSnapshot(permissionSummaries, snapshot.SessionsByID)
	}
	if sessionsV3SyncResourcesInclude(resources, "notifications") || sessionsV3SyncResourcesInclude(resources, "notification_summary") {
		notifications, summary, err := s.sessionsV3NotificationResources(options.Principal, sessionsV3SyncResourcesInclude(resources, "notifications"), sessionsV3SyncResourcesInclude(resources, "notification_summary"))
		if err != nil {
			return sessionsV3SyncSnapshotResponseBody{}, err
		}
		response.Notifications = notifications
		response.NotificationSummary = summary
	}
	if options.Snapshot.IncludeSessionView {
		if len(snapshot.SessionOrder) > sessionsV3SyncHydrateMaxSessionViews {
			return sessionsV3SyncSnapshotResponseBody{}, errors.New("sync hydrate resources.session_view cannot target more than 8 sessions")
		}
		views, err := s.sessionsV3SyncSessionViews(options, snapshot)
		if err != nil {
			return sessionsV3SyncSnapshotResponseBody{}, err
		}
		response.SessionViewsByID = views
	} else if options.Snapshot.IncludeActivePlan {
		views, err := s.sessionsV3SyncActivePlanViews(snapshot)
		if err != nil {
			return sessionsV3SyncSnapshotResponseBody{}, err
		}
		response.SessionViewsByID = views
	}
	if timings != nil {
		response.logTimings = timings
	}
	return response, nil
}

type sessionsV3SyncBootstrapTimings struct {
	scopeDur    time.Duration
	snapshotDur time.Duration
	cursorDur   time.Duration
	decorateDur time.Duration
	encodeDur   time.Duration
	totalDur    time.Duration
	sessions    int
	messages    int
	runIntents  int
	bytes       int
}

func newSessionsV3SyncBootstrapTimings() *sessionsV3SyncBootstrapTimings {
	if strings.TrimSpace(os.Getenv("SWARM_V3_SYNC_BOOTSTRAP_TIMING")) == "" {
		return nil
	}
	return &sessionsV3SyncBootstrapTimings{}
}

func (t *sessionsV3SyncBootstrapTimings) log() {
	if t == nil {
		return
	}
	log.Printf("v3 sync bootstrap api timings sessions=%d messages=%d run_intents=%d bytes=%d scope=%s snapshot=%s cursor=%s decorate=%s encode=%s total_before_encode=%s total_with_encode=%s", t.sessions, t.messages, t.runIntents, t.bytes, t.scopeDur, t.snapshotDur, t.cursorDur, t.decorateDur, t.encodeDur, t.totalDur, t.totalDur+t.encodeDur)
}

func writeSessionsV3SyncBootstrapJSON(w http.ResponseWriter, response sessionsV3SyncSnapshotResponseBody) {
	timings := response.logTimings
	response.logTimings = nil
	if timings == nil {
		writeJSON(w, http.StatusOK, response)
		return
	}
	start := time.Now()
	payload, err := json.Marshal(response)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	timings.encodeDur = time.Since(start)
	timings.bytes = len(payload)
	header := w.Header()
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if header.Get("Cache-Control") == "" {
		header.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(payload, '\n'))
	timings.log()
}

func (s *Server) resolveSessionsV3SyncPreference(preference pebblestore.ModelPreference, cache map[sessionsV3SyncPreferenceCacheKey]sessionsV3SyncResolvedPreference) sessionsV3SyncResolvedPreference {
	preference = normalizeSessionsV3ModelPreference(preference)
	cacheKey := sessionsV3SyncPreferenceCacheKey{
		Provider:    preference.Provider,
		Model:       preference.Model,
		Thinking:    preference.Thinking,
		ServiceTier: preference.ServiceTier,
		ContextMode: preference.ContextMode,
	}
	if cached, ok := cache[cacheKey]; ok {
		return cached
	}
	resolvedPreference := sessionsV3SyncResolvedPreference{Preference: preference}
	if s != nil && s.model != nil {
		if resolved, err := s.model.ResolvePreference(preference); err == nil {
			resolvedPreference.Preference = normalizeSessionsV3ModelPreference(resolved.Preference)
			resolvedPreference.ContextWindow = resolved.ContextWindow
			resolvedPreference.MaxOutputTokens = resolved.MaxOutputTokens
		}
	}
	cache[cacheKey] = resolvedPreference
	return resolvedPreference
}

func (s *Server) sessionsV3AgentModelPolicyWithResolver(session pebblestore.SessionSnapshot, defaultPreference pebblestore.ModelPreference, defaultContextWindow, defaultMaxOutputTokens int, resolvePreference func(pebblestore.ModelPreference) sessionsV3SyncResolvedPreference) sessionsV3AgentModelPolicy {
	policy := sessionsV3AgentModelPolicy{
		AgentName:       sessionsV3MetadataString(session.Metadata, "agent_name"),
		ResolvedAgent:   sessionsV3MetadataString(session.Metadata, "resolved_agent_name"),
		Source:          "default",
		Locked:          false,
		Preference:      normalizeSessionsV3ModelPreference(defaultPreference),
		ContextWindow:   defaultContextWindow,
		MaxOutputTokens: defaultMaxOutputTokens,
	}
	if policy.AgentName == "" {
		policy.AgentName = policy.ResolvedAgent
	}
	if policy.ResolvedAgent == "" {
		policy.ResolvedAgent = policy.AgentName
	}
	profile, err := sessionV3AgentProfileFromMetadata(session.Metadata)
	if err != nil {
		return policy
	}
	if strings.TrimSpace(profile.Name) != "" {
		policy.AgentName = strings.TrimSpace(profile.Name)
		policy.ResolvedAgent = strings.TrimSpace(profile.Name)
	}
	agentPref := sessionsV3AgentPresetPreference(profile)
	if pebblestore.AgentModelMode(profile) == "split" && pebblestore.AgentSupportsSplitModel(profile) && strings.TrimSpace(profile.PlanProvider) != "" && strings.TrimSpace(profile.PlanModel) != "" {
		agentPref = pebblestore.ModelPreference{
			Provider:    strings.ToLower(strings.TrimSpace(profile.PlanProvider)),
			Model:       strings.TrimSpace(profile.PlanModel),
			Thinking:    normalizeSessionV3ThinkingWithProvider(profile.PlanProvider, profile.PlanThinking),
			ServiceTier: strings.TrimSpace(profile.PlanServiceTier),
			UpdatedAt:   profile.UpdatedAt,
		}
	}
	if strings.TrimSpace(agentPref.Provider) == "" || strings.TrimSpace(agentPref.Model) == "" {
		return policy
	}
	policy.Source = "agent_preset"
	policy.Locked = true
	policy.Reason = "Agent model is set in agent settings; update the agent model in agent settings to choose a different model."
	if pebblestore.AgentModelMode(profile) == "split" && pebblestore.AgentSupportsSplitModel(profile) {
		policy.Source = "agent_plan_preset"
		policy.Reason = "Agent plan model is set in agent settings; exit plan mode uses the configured auto model."
	}
	policy.Preference = normalizeSessionsV3ModelPreference(agentPref)
	policy.ContextWindow = 0
	policy.MaxOutputTokens = 0
	if resolvePreference != nil {
		resolved := resolvePreference(policy.Preference)
		policy.Preference = resolved.Preference
		policy.ContextWindow = resolved.ContextWindow
		policy.MaxOutputTokens = resolved.MaxOutputTokens
	}
	return policy
}

func sessionsV3SyncSessionShells(sessions map[string]pebblestore.SessionSnapshot) map[string]pebblestore.SessionSnapshot {
	if len(sessions) == 0 {
		return sessions
	}
	out := make(map[string]pebblestore.SessionSnapshot, len(sessions))
	for sessionID, session := range sessions {
		out[sessionID] = sessionsV3SyncSessionShell(session)
	}
	return out
}

func sessionsV3SyncSessionShell(session pebblestore.SessionSnapshot) pebblestore.SessionSnapshot {
	session.Preference = pebblestore.ModelPreference{}
	session.Lifecycle = nil
	session.Metadata = sessionsV3SyncShellMetadata(session.Metadata)
	return session
}

func sessionsV3SyncShellMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if !sessionsV3SyncShellMetadataKeyAllowed(key) {
			continue
		}
		out[key] = cloneSessionsV3MetadataValue(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sessionsV3SyncShellMetadataKeyAllowed(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "agent_name",
		"resolved_agent_name",
		"agent_mode",
		"runtime_mode",
		"exit_plan_mode_enabled",
		"swarm_v3_execution_class",
		"swarm_v3_runtime_swarm_id",
		"swarm_v3_runtime_kind",
		"swarm_v3_authority_host_swarm_id",
		"swarm_v3_authority_container_id",
		"swarm_v3_workspace_binding_id",
		"swarm_v3_source_workspace_id",
		"swarm_v3_source_workspace_generation",
		"swarm_v3_source_workspace_name",
		"swarm_v3_source_workspace_path",
		"swarm_v3_runtime_workspace_path",
		"swarm_v3_placement_generation",
		"swarm_v3_binding_generation",
		"swarm_v3_tui_directory_session",
		"swarm_v3_tui_cwd_path",
		"swarm_v3_tui_original_cwd_path",
		"swarm_v3_desktop_sidebar_pinned",
		"workspace_id",
		"local_workspace_binding_id",
		"parent_session_id",
		"lineage_kind",
		"lineage_label",
		"assignment_label",
		"subagent",
		"requested_subagent",
		"background",
		"background_agent",
		"requested_background_agent",
		"launch_mode",
		"target_kind",
		"swarm_target_name",
		"target_display_name",
		"source",
		"flow_id",
		"owner_transport":
		return true
	default:
		return false
	}
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

func (s *Server) validateSessionsV3KnownState(scope v3SyncCursorScope, known map[string]sessionsV3KnownState) error {
	for _, state := range known {
		if state.AppliedSeq != 0 || state.HighWatermark != 0 {
			return newV3SyncCursorError("known_sessions_sequence_state_unsupported", errors.New("known_sessions.applied_seq and known_sessions.high_watermark are not supported; omit them or send zero values"))
		}
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

func sessionsV3PermissionSummariesVisibleInSnapshot(summaries map[string]sessionsV3PermissionSummary, sessions map[string]pebblestore.SessionSnapshot) map[string]sessionsV3PermissionSummary {
	if len(summaries) == 0 || len(sessions) == 0 {
		return nil
	}
	out := make(map[string]sessionsV3PermissionSummary, len(summaries))
	for sessionID, summary := range summaries {
		if _, ok := sessions[sessionID]; ok {
			out[sessionID] = summary
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Server) sessionsV3NotificationResources(principal identity.Principal, includeNotifications, includeSummary bool) ([]pebblestore.NotificationRecord, *pebblestore.NotificationSummary, error) {
	if s == nil || s.notifications == nil {
		return nil, nil, nil
	}
	svc := notificationServiceForAccount(s.notifications, principal.AccountScopeID)
	swarmID := svc.LocalSwarmID()
	var records []pebblestore.NotificationRecord
	if includeNotifications {
		var err error
		records, err = svc.ListNotifications(swarmID, sessionsV3WorksetMaxResourcePageSize)
		if err != nil {
			return nil, nil, err
		}
		records = s.enrichNotificationRecords(records)
	}
	if !includeSummary {
		return records, nil, nil
	}
	summary, err := svc.Summary(swarmID)
	if err != nil {
		return nil, nil, err
	}
	return records, &summary, nil
}

func sessionsV3SyncResourcesInclude(resources []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, resource := range resources {
		if strings.TrimSpace(resource) == target {
			return true
		}
	}
	return false
}

func (s *Server) sessionsV3PermissionSummaries(principal identity.Principal) (map[string]sessionsV3PermissionSummary, error) {
	out := map[string]sessionsV3PermissionSummary{}
	if s == nil || s.perm == nil {
		return out, nil
	}
	summaries, err := s.perm.ListPendingSummaries(principal.AccountScopeID, principal.UserID, 100000)
	if err != nil {
		return nil, err
	}
	for _, summary := range summaries {
		if strings.TrimSpace(summary.SessionID) == "" || summary.PendingCount <= 0 {
			continue
		}
		out[summary.SessionID] = sessionsV3PermissionSummary{
			SessionID:            summary.SessionID,
			PendingApprovalCount: summary.PendingCount,
			OldestPendingAt:      summary.OldestPendingAt,
			NewestPendingAt:      summary.NewestPendingAt,
			UpdatedAt:            summary.UpdatedAt,
		}
	}
	return out, nil
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
	if resources.RunIntents {
		out = append(out, "run_intents")
	}
	if resources.CurrentRunState || includeActive {
		out = append(out, "current_run_state")
	}
	if resources.SessionView {
		out = append(out, "session_view")
	}
	if resources.PermissionSummaries {
		out = append(out, "permission_summaries")
	}
	if resources.Notifications {
		out = append(out, "notifications")
	}
	if resources.NotificationSummary {
		out = append(out, "notification_summary")
	}
	if resources.ActivePlan {
		out = append(out, "active_plan")
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
