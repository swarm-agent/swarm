package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const sessionsV3ReconnectRunIntentListLimit = 100000

type sessionsV3ReconnectRequest struct {
	Surface  string                            `json:"surface,omitempty"`
	ClientID string                            `json:"client_id,omitempty"`
	Workset  sessionsV3ReconnectWorksetRequest `json:"workset,omitempty"`
}

type sessionsV3ReconnectWorksetRequest struct {
	WorksetID             string                     `json:"workset_id,omitempty"`
	Selector              V3RealtimeWorksetSelector  `json:"selector,omitempty"`
	SessionIDs            []string                   `json:"session_ids,omitempty"`
	Global                bool                       `json:"global,omitempty"`
	Workspace             sessionsV3WorksetWorkspace `json:"workspace,omitempty"`
	Recent                sessionsV3WorksetRecent    `json:"recent,omitempty"`
	History               sessionsV3WorksetHistory   `json:"history,omitempty"`
	Resources             sessionsV3WorksetResources `json:"resources,omitempty"`
	IncludeActive         bool                       `json:"include_active,omitempty"`
	AutoSubscribeSessions bool                       `json:"auto_subscribe_sessions,omitempty"`
}

type sessionsV3ReconnectSubscription struct {
	Protocol        string `json:"protocol"`
	ProtocolVersion int    `json:"protocol_version"`
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id"`
	SubscriptionID  string `json:"subscription_id"`
	EndpointCursor  string `json:"endpoint_cursor"`
}

type sessionsV3ReconnectDiagnostic struct {
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	RunIDs   []string `json:"run_ids,omitempty"`
	Statuses []string `json:"statuses,omitempty"`
}

func (s *Server) handleSessionsV3Reconnect(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3ReconnectPrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3ReconnectRequest
	if err := decodeOptionalSessionsV3ReconnectRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if sessionsV3ReconnectHasWorkset(req) && strings.TrimSpace(req.ClientID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("v3 reconnect with workset requires client_id"))
		return
	}

	response, err := s.sessionsV3ReconnectResponse(principal, req)
	if err != nil {
		writeError(w, sessionsV3ReconnectErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) sessionsV3ReconnectPrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	if s == nil || s.sessions == nil {
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

func decodeOptionalSessionsV3ReconnectRequest(r *http.Request, out *sessionsV3ReconnectRequest) error {
	if r == nil || r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	return decodeJSONObject(decoder, out)
}

func (s *Server) sessionsV3ReconnectResponse(principal identity.Principal, req sessionsV3ReconnectRequest) (map[string]any, error) {
	if sessionsV3ReconnectHasWorkset(req) {
		return s.sessionsV3ReconnectWorksetResponse(principal, req)
	}
	activeIntents, err := s.sessionsV3ReconnectActiveRunIntents(principal)
	if err != nil {
		return nil, err
	}
	bySession := map[string][]sessionruntime.SessionRunIntent{}
	for _, intent := range activeIntents {
		sessionID := strings.TrimSpace(intent.SessionID)
		if sessionID == "" {
			return nil, errors.New("active v3 run intent is missing session_id")
		}
		bySession[sessionID] = append(bySession[sessionID], intent)
	}

	sessionsByID := map[string]pebblestore.SessionSnapshot{}
	projectionsBySession := map[string]sessionruntime.SessionProjection{}
	runIntentsBySession := map[string][]sessionruntime.SessionRunIntent{}
	currentRunIntentBySession := map[string]sessionruntime.SessionRunIntent{}
	diagnosticsBySession := map[string][]sessionsV3ReconnectDiagnostic{}
	eligible := make([]sessionsV3ReconnectSessionCandidate, 0, len(bySession))

	for sessionID := range bySession {
		hydrated, found, err := s.sessions.HydrateSessionSnapshot(sessionID, 0, 0)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("active v3 run intent references missing session %q", sessionID)
		}
		if !sessionsV3ReconnectSessionVisible(hydrated.Session, principal.AccountScopeID) {
			continue
		}
		current := bySession[sessionID][0]
		allIntents, err := s.sessions.ListSessionRunIntents(sessionID, 0, sessionsV3ReconnectRunIntentListLimit)
		if err != nil {
			return nil, err
		}
		sessionsByID[sessionID] = hydrated.Session
		projectionsBySession[sessionID] = hydrated.Projection
		runIntentsBySession[sessionID] = allIntents
		currentRunIntentBySession[sessionID] = current
		eligible = append(eligible, sessionsV3ReconnectSessionCandidate{SessionID: sessionID, Session: hydrated.Session, Current: current})
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		if sessionsV3ReconnectRunIntentLess(eligible[j].Current, eligible[i].Current) {
			return true
		}
		if sessionsV3ReconnectRunIntentLess(eligible[i].Current, eligible[j].Current) {
			return false
		}
		if eligible[i].Session.UpdatedAt != eligible[j].Session.UpdatedAt {
			return eligible[i].Session.UpdatedAt > eligible[j].Session.UpdatedAt
		}
		return eligible[i].SessionID < eligible[j].SessionID
	})
	sessionOrder := make([]string, 0, len(eligible))
	for _, candidate := range eligible {
		sessionOrder = append(sessionOrder, candidate.SessionID)
	}

	rev, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		return nil, err
	}
	snapshotEndpointCursor, err := s.sessions.CurrentRealtimeOutboxCursor()
	if err != nil {
		return nil, err
	}
	signedSnapshotEndpointCursor, err := s.signV3SyncEndpointCursorFromLegacy(v3SyncCursorScopeForRealtime(principal, "desktop"), snapshotEndpointCursor)
	if err != nil {
		return nil, err
	}
	subscriptions := make([]sessionsV3ReconnectSubscription, 0, len(sessionOrder))
	for _, sessionID := range sessionOrder {
		subscriptions = append(subscriptions, sessionsV3ReconnectSubscription{
			Protocol:        V3RealtimeProtocol,
			ProtocolVersion: V3RealtimeProtocolVersion,
			Kind:            V3RealtimeKindSubscribe,
			SessionID:       sessionID,
			SubscriptionID:  "reconnect:" + sessionID,
			EndpointCursor:  signedSnapshotEndpointCursor,
		})
	}

	return sessionsV3ReconnectResponseMap(sessionsV3ReconnectResponseInput{
		Rev:                       rev,
		SnapshotEndpointCursor:    signedSnapshotEndpointCursor,
		SessionsByID:              sessionsByID,
		ProjectionsBySession:      projectionsBySession,
		RunIntentsBySession:       runIntentsBySession,
		CurrentRunIntentBySession: currentRunIntentBySession,
		Subscriptions:             subscriptions,
		SessionOrder:              sessionOrder,
		DiagnosticsBySession:      diagnosticsBySession,
	}), nil
}

type sessionsV3ReconnectResponseInput struct {
	Rev                       uint64
	ClientID                  string
	Surface                   string
	WorksetID                 string
	SnapshotEndpointCursor    string
	SessionsByID              any
	ProjectionsBySession      any
	MessagesBySession         any
	EventsBySession           any
	PlansBySession            any
	PlanRevisionsBySession    any
	PermissionsBySession      any
	UsageBySession            any
	PreferencesBySession      any
	AgentModelPolicyBySession any
	RunIntentsBySession       any
	CurrentRunIntentBySession any
	HistoryManifestsBySession any
	HistoryChunksByID         any
	Omissions                 any
	Pagination                any
	Watermarks                any
	Subscriptions             []sessionsV3ReconnectSubscription
	Worksets                  []V3RealtimeWorksetSubscriptionRequest
	SessionOrder              []string
	DiagnosticsBySession      map[string][]sessionsV3ReconnectDiagnostic
}

func (s *Server) sessionsV3ReconnectWorksetResponse(principal identity.Principal, req sessionsV3ReconnectRequest) (map[string]any, error) {
	surface := normalizeV3SyncSurface(req.Surface)
	worksetReq, selector := sessionsV3ReconnectWorksetRequestForOptions(req.Workset)
	options, err := sessionsV3WorksetOptionsFromRequest(principal, worksetReq)
	if err != nil {
		return nil, err
	}
	options.Principal = principal
	options.Surface = surface
	workset, err := s.sessions.BuildSessionWorkset(options.Store)
	if err != nil {
		return nil, err
	}
	signedSnapshotEndpointCursor, err := s.signV3SyncEndpointCursor(
		v3SyncCursorScopeForRealtime(principal, surface),
		workset.Rev,
	)
	if err != nil {
		return nil, err
	}
	response, err := s.sessionsV3WorksetResponseForResult(options, workset, signedSnapshotEndpointCursor)
	if err != nil {
		return nil, err
	}
	var messagesBySession any
	if options.Store.History.IncludeMessages {
		messagesBySession = response["messages_by_session"]
	}

	var eventsBySession any
	if options.Store.History.IncludeEvents {
		eventsBySession = response["events_by_session"]
	}

	var runIntentsBySession any
	if options.Store.IncludeRunIntents {
		runIntentsBySession = response["run_intents_by_session"]
	}

	var plansBySession any
	if options.IncludeActivePlan {
		plansBySession = response["plans_by_session"]
	}

	var planRevisionsBySession any
	if options.IncludePlanRevisions {
		planRevisionsBySession = response["plan_revisions_by_session"]
	}

	var historyManifestsBySession any
	var historyChunksByID any
	if options.Store.History.IncludeMessages || options.Store.History.IncludeEvents {
		historyManifestsBySession = response["history_manifests_by_session"]
		historyChunksByID = response["history_chunks_by_id"]
	}

	worksetID := strings.TrimSpace(req.Workset.WorksetID)
	if worksetID == "" {
		worksetID = "workset:" + v3SyncDeterministicSelectorHash(v3SyncCanonicalSelector(selector))
	}
	clientID := strings.TrimSpace(req.ClientID)
	activeIntents, err := s.sessionsV3ReconnectActiveRunIntents(principal)
	if err != nil {
		return nil, err
	}
	subscriptionOrder := sessionsV3ReconnectWorksetSubscriptionOrder(selector, workset.SessionOrder, activeIntents)
	subscriptions := sessionsV3ReconnectSubscriptions(clientID, subscriptionOrder, signedSnapshotEndpointCursor)
	worksets := []V3RealtimeWorksetSubscriptionRequest{{
		WorksetID:             worksetID,
		SubscriptionID:        sessionsV3ReconnectWorksetSubscriptionID(clientID, worksetID),
		Surface:               surface,
		Selector:              selector,
		Resources:             sessionsV3ReconnectRealtimeResourceSet(worksetReq.Resources, worksetReq.History, worksetReq.IncludeActive),
		AutoSubscribeSessions: req.Workset.AutoSubscribeSessions,
	}}
	return sessionsV3ReconnectResponseMap(sessionsV3ReconnectResponseInput{
		Rev:                       workset.Rev,
		ClientID:                  clientID,
		Surface:                   surface,
		WorksetID:                 worksetID,
		SnapshotEndpointCursor:    signedSnapshotEndpointCursor,
		SessionsByID:              response["sessions_by_id"],
		ProjectionsBySession:      response["projections_by_session"],
		MessagesBySession:         messagesBySession,
		EventsBySession:           eventsBySession,
		PlansBySession:            plansBySession,
		PlanRevisionsBySession:    planRevisionsBySession,
		PermissionsBySession:      response["permissions_by_session"],
		UsageBySession:            response["usage_by_session"],
		PreferencesBySession:      response["preferences_by_session"],
		AgentModelPolicyBySession: response["agent_model_policy_by_session"],
		RunIntentsBySession:       runIntentsBySession,
		HistoryManifestsBySession: historyManifestsBySession,
		HistoryChunksByID:         historyChunksByID,
		Omissions:                 response["omissions"],
		Pagination:                response["pagination"],
		Watermarks:                response["watermarks"],
		Subscriptions:             subscriptions,
		Worksets:                  worksets,
		SessionOrder:              workset.SessionOrder,
		DiagnosticsBySession:      map[string][]sessionsV3ReconnectDiagnostic{},
	}), nil
}

func sessionsV3ReconnectResponseMap(input sessionsV3ReconnectResponseInput) map[string]any {
	response := map[string]any{
		"ok":                       true,
		"rev":                      input.Rev,
		"snapshot_endpoint_cursor": input.SnapshotEndpointCursor,
		"sessions_by_id":           input.SessionsByID,
		"projections_by_session":   input.ProjectionsBySession,
		"subscriptions":            input.Subscriptions,
		"session_order":            input.SessionOrder,
		"diagnostics_by_session":   input.DiagnosticsBySession,
	}
	if input.CurrentRunIntentBySession != nil {
		response["current_run_intent_by_session"] = input.CurrentRunIntentBySession
	}
	optional := map[string]any{
		"run_intents_by_session":        input.RunIntentsBySession,
		"client_id":                     input.ClientID,
		"surface":                       input.Surface,
		"workset_id":                    input.WorksetID,
		"messages_by_session":           input.MessagesBySession,
		"events_by_session":             input.EventsBySession,
		"plans_by_session":              input.PlansBySession,
		"plan_revisions_by_session":     input.PlanRevisionsBySession,
		"permissions_by_session":        input.PermissionsBySession,
		"usage_by_session":              input.UsageBySession,
		"preferences_by_session":        input.PreferencesBySession,
		"agent_model_policy_by_session": input.AgentModelPolicyBySession,
		"history_manifests_by_session":  input.HistoryManifestsBySession,
		"history_chunks_by_id":          input.HistoryChunksByID,
		"omissions":                     input.Omissions,
		"pagination":                    input.Pagination,
		"watermarks":                    input.Watermarks,
		"worksets":                      input.Worksets,
	}
	for key, value := range optional {
		if !sessionsV3ReconnectEmptyValue(value) {
			response[key] = value
		}
	}
	if len(input.Worksets) > 0 {
		response["realtime"] = map[string]any{
			"stream_path": V3RealtimeStreamPath,
			"resume": V3RealtimeMessage{
				Protocol:        V3RealtimeProtocol,
				ProtocolVersion: V3RealtimeProtocolVersion,
				Kind:            V3RealtimeKindResume,
				EndpointCursor:  input.SnapshotEndpointCursor,
				Subscriptions:   reconnectSubscriptionsToV3Realtime(input.Subscriptions),
				Worksets:        input.Worksets,
			},
		}
	}
	return response
}

func sessionsV3ReconnectRealtimeResourceSet(resources sessionsV3WorksetResources, history sessionsV3WorksetHistory, includeActive bool) []string {
	filtered := make([]string, 0)
	for _, resource := range sessionsV3SyncResourceSet(resources, history, includeActive) {
		if v3RealtimeWorksetResourceAllowed(resource) {
			filtered = append(filtered, resource)
		}
	}
	resourceSet, err := canonicalV3RealtimeWorksetResources(filtered)
	if err != nil {
		return []string{"membership", "projections", "sessions", "tombstones"}
	}
	return resourceSet
}

func sessionsV3ReconnectWorksetRequestForOptions(req sessionsV3ReconnectWorksetRequest) (sessionsV3WorksetRequest, V3RealtimeWorksetSelector) {
	selector := req.Selector
	if len(selector.SessionIDs) == 0 {
		selector.SessionIDs = req.SessionIDs
	}
	if !selector.Global {
		selector.Global = req.Global
	}
	if selector.WorkspacePath == "" && len(selector.WorkspacePaths) == 0 {
		selector.WorkspacePath = req.Workspace.WorkspacePath
		selector.WorkspacePaths = req.Workspace.WorkspacePaths
	}
	if selector.Recent.Limit == 0 && req.Recent.Limit != 0 {
		selector.Recent = req.Recent
	}
	if strings.TrimSpace(selector.Kind) == "global" {
		selector.Global = true
	}
	if strings.TrimSpace(selector.Kind) == "" {
		switch {
		case selector.Global:
			selector.Kind = "global"
		case len(selector.SessionIDs) > 0:
			selector.Kind = "session_ids"
		case selector.Recent.Limit > 0:
			selector.Kind = "recent"
		case strings.TrimSpace(selector.WorkspacePath) != "" || len(selector.WorkspacePaths) > 0:
			selector.Kind = "workspace"
		}
	}
	return sessionsV3WorksetRequest{
		SessionIDs:    selector.SessionIDs,
		Global:        selector.Global,
		Workspace:     sessionsV3WorksetWorkspace{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths},
		Recent:        selector.Recent,
		History:       req.History,
		Resources:     req.Resources,
		IncludeActive: req.IncludeActive,
	}, selector
}

func sessionsV3ReconnectWorksetSubscriptionOrder(selector V3RealtimeWorksetSelector, worksetOrder []string, activeIntents []sessionruntime.SessionRunIntent) []string {
	if strings.TrimSpace(selector.Kind) == "session_ids" {
		return worksetOrder
	}
	activeSessionIDs := make(map[string]struct{}, len(activeIntents))
	for _, intent := range activeIntents {
		sessionID := strings.TrimSpace(intent.SessionID)
		if sessionID == "" {
			continue
		}
		activeSessionIDs[sessionID] = struct{}{}
	}
	out := make([]string, 0, len(worksetOrder))
	seen := make(map[string]struct{}, len(worksetOrder))
	for _, sessionID := range worksetOrder {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, active := activeSessionIDs[sessionID]; !active {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		out = append(out, sessionID)
	}
	return out
}

func sessionsV3ReconnectSubscriptions(clientID string, sessionOrder []string, endpointCursor string) []sessionsV3ReconnectSubscription {
	subscriptions := make([]sessionsV3ReconnectSubscription, 0, len(sessionOrder))
	for _, sessionID := range sessionOrder {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		subscriptions = append(subscriptions, sessionsV3ReconnectSubscription{
			Protocol:        V3RealtimeProtocol,
			ProtocolVersion: V3RealtimeProtocolVersion,
			Kind:            V3RealtimeKindSubscribe,
			SessionID:       sessionID,
			SubscriptionID:  sessionsV3ReconnectSessionSubscriptionID(clientID, sessionID),
			EndpointCursor:  endpointCursor,
		})
	}
	return subscriptions
}

func reconnectSubscriptionsToV3Realtime(subscriptions []sessionsV3ReconnectSubscription) []V3RealtimeSubscriptionRequest {
	out := make([]V3RealtimeSubscriptionRequest, 0, len(subscriptions))
	for _, sub := range subscriptions {
		out = append(out, V3RealtimeSubscriptionRequest{SessionID: sub.SessionID, SubscriptionID: sub.SubscriptionID, EndpointCursor: sub.EndpointCursor})
	}
	return out
}

func sessionsV3ReconnectSessionSubscriptionID(clientID, sessionID string) string {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return "reconnect:" + sessionID
	}
	return clientID + ":session:" + sessionID
}

func sessionsV3ReconnectWorksetSubscriptionID(clientID, worksetID string) string {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return "reconnect:" + worksetID
	}
	return clientID + ":" + worksetID
}

func sessionsV3ReconnectHasWorkset(req sessionsV3ReconnectRequest) bool {
	workset := req.Workset
	return strings.TrimSpace(workset.WorksetID) != "" || workset.AutoSubscribeSessions || workset.IncludeActive || workset.Global || len(workset.SessionIDs) > 0 || strings.TrimSpace(workset.Workspace.WorkspacePath) != "" || len(workset.Workspace.WorkspacePaths) > 0 || workset.Recent.Limit > 0 || strings.TrimSpace(workset.Selector.Kind) != "" || workset.Selector.Global || len(workset.Selector.SessionIDs) > 0 || strings.TrimSpace(workset.Selector.WorkspacePath) != "" || len(workset.Selector.WorkspacePaths) > 0 || workset.Selector.Recent.Limit > 0
}

func sessionsV3ReconnectEmptyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []V3RealtimeWorksetSubscriptionRequest:
		return len(typed) == 0
	default:
		return false
	}
}

func sessionsV3ReconnectErrorStatus(err error) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	text := err.Error()
	if strings.Contains(text, "workset") || strings.Contains(text, "requires") || strings.Contains(text, "selector") || strings.Contains(text, "canonical") || strings.Contains(text, "cannot be combined") || strings.Contains(text, "at least one") {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func (s *Server) sessionsV3ReconnectActiveRunIntents(principal identity.Principal) ([]sessionruntime.SessionRunIntent, error) {
	states, err := s.sessions.ListActiveSessionRunStates(principal.AccountScopeID, sessionsV3ReconnectRunIntentListLimit)
	if err != nil {
		return nil, err
	}
	out := make([]sessionruntime.SessionRunIntent, 0, len(states))
	for _, state := range states {
		if !state.Active || strings.TrimSpace(state.RunID) == "" {
			continue
		}
		out = append(out, sessionruntime.SessionRunIntent{
			SessionID:      strings.TrimSpace(state.SessionID),
			UserID:         strings.TrimSpace(state.UserID),
			AccountScopeID: strings.TrimSpace(state.AccountScopeID),
			RunID:          strings.TrimSpace(state.RunID),
			Status:         strings.TrimSpace(state.Status),
			BlockedReason:  strings.TrimSpace(state.BlockedReason),
			CreatedAt:      state.CreatedAt,
			UpdatedAt:      state.UpdatedAt,
			EventSeq:       state.EventSeq,
		})
	}
	return out, nil
}

func sessionsV3ReconnectSessionVisible(session pebblestore.SessionSnapshot, accountScopeID string) bool {
	if strings.TrimSpace(session.ID) == "" {
		return false
	}
	if accountScopeID == "" || strings.TrimSpace(session.AccountScopeID) == "" {
		return true
	}
	return strings.TrimSpace(session.AccountScopeID) == accountScopeID
}

func sessionsV3ReconnectActiveIntents(intents []sessionruntime.SessionRunIntent) []sessionruntime.SessionRunIntent {
	out := make([]sessionruntime.SessionRunIntent, 0, len(intents))
	for _, intent := range intents {
		if sessionV3RunIntentStatusActive(intent.Status) {
			out = append(out, intent)
		}
	}
	return out
}

func sessionsV3ReconnectCurrentRunIntent(intents []sessionruntime.SessionRunIntent) sessionruntime.SessionRunIntent {
	if len(intents) == 0 {
		return sessionruntime.SessionRunIntent{}
	}
	current := intents[0]
	for _, intent := range intents[1:] {
		if sessionsV3ReconnectRunIntentLess(current, intent) {
			current = intent
		}
	}
	return current
}

func sessionsV3ReconnectRunIntentLess(a, b sessionruntime.SessionRunIntent) bool {
	aPriority := sessionsV3ReconnectRunIntentPriority(a.Status)
	bPriority := sessionsV3ReconnectRunIntentPriority(b.Status)
	if aPriority != bPriority {
		return aPriority < bPriority
	}
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt < b.UpdatedAt
	}
	if a.EventSeq != b.EventSeq {
		return a.EventSeq < b.EventSeq
	}
	return strings.TrimSpace(a.RunID) < strings.TrimSpace(b.RunID)
}

func sessionsV3ReconnectRunIntentPriority(status string) int {
	switch strings.TrimSpace(status) {
	case sessionruntime.RunIntentRunning:
		return 2
	case sessionruntime.RunIntentPendingExecutor:
		return 1
	default:
		return 0
	}
}

func sessionsV3ReconnectMultipleActiveDiagnostic(active []sessionruntime.SessionRunIntent, current sessionruntime.SessionRunIntent) sessionsV3ReconnectDiagnostic {
	runIDs := make([]string, 0, len(active))
	statuses := make([]string, 0, len(active))
	for _, intent := range active {
		runIDs = append(runIDs, intent.RunID)
		statuses = append(statuses, intent.Status)
	}
	return sessionsV3ReconnectDiagnostic{
		Code:     "multiple_active_run_intents",
		Message:  fmt.Sprintf("session has %d active durable v3 run intents; selected %q deterministically", len(active), current.RunID),
		RunIDs:   runIDs,
		Statuses: statuses,
	}
}

type sessionsV3ReconnectSessionCandidate struct {
	SessionID string
	Session   pebblestore.SessionSnapshot
	Current   sessionruntime.SessionRunIntent
}
