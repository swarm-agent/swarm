package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

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
	bootstrapReq := reconnectToSyncBootstrapRequest(req)
	options, selector, resources, err := sessionsV3SyncBootstrapOptions(principal, bootstrapReq)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.sessionsV3SyncSnapshotResponse(context.Background(), options, selector, resources, bootstrapReq.KnownSessions)
	if err != nil {
		return nil, err
	}
	return sessionsV3ReconnectMapFromSyncSnapshot(snapshot, req), nil
}

func (s *Server) sessionsV3ReconnectWorksetResponse(principal identity.Principal, req sessionsV3ReconnectRequest) (map[string]any, error) {
	bootstrapReq := reconnectToSyncBootstrapRequest(req)
	options, selector, resources, err := sessionsV3SyncBootstrapOptions(principal, bootstrapReq)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.sessionsV3SyncSnapshotResponse(context.Background(), options, selector, resources, bootstrapReq.KnownSessions)
	if err != nil {
		return nil, err
	}
	return sessionsV3ReconnectMapFromSyncSnapshot(snapshot, req), nil
}

func reconnectToSyncBootstrapRequest(req sessionsV3ReconnectRequest) sessionsV3SyncBootstrapRequest {
	surface := normalizeV3SyncSurface(req.Surface)
	if !sessionsV3ReconnectHasWorkset(req) {
		return sessionsV3SyncBootstrapRequest{
			Surface:       surface,
			Selector:      sessionsV3SyncSelector{Kind: "session_ids"},
			History:       sessionsV3WorksetHistory{Mode: pebblestore.V3SyncSnapshotHistoryModeNone},
			Resources:     sessionsV3WorksetResources{CurrentRunState: true},
			IncludeActive: true,
		}
	}
	workset := req.Workset
	selector := reconnectWorksetSyncSelector(workset)
	return sessionsV3SyncBootstrapRequest{
		Surface:       surface,
		SelectorKind:  selector.Kind,
		Selector:      selector,
		History:       workset.History,
		Resources:     workset.Resources,
		IncludeActive: workset.IncludeActive,
	}
}

func reconnectWorksetSyncSelector(req sessionsV3ReconnectWorksetRequest) sessionsV3SyncSelector {
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
	selector.Attention.PendingPermissions = selector.Attention.PendingPermissions || req.Selector.Attention.PendingPermissions
	if selector.Recent.Limit == 0 && strings.TrimSpace(selector.Kind) == "workspace" {
		selector.Recent.Limit = sessionsV3WorksetMaxResourcePageSize
	}
	return sessionsV3SyncSelector{
		Kind:           selector.Kind,
		Global:         selector.Global,
		WorkspacePath:  selector.WorkspacePath,
		WorkspacePaths: selector.WorkspacePaths,
		SessionIDs:     selector.SessionIDs,
		Recent:         selector.Recent,
		Attention:      selector.Attention,
	}
}

func sessionsV3ReconnectMapFromSyncSnapshot(snapshot sessionsV3SyncSnapshotResponseBody, req sessionsV3ReconnectRequest) map[string]any {
	subscriptions := sessionsV3ReconnectSubscriptionsFromRealtime(snapshot.Realtime)
	worksets := sessionsV3ReconnectWorksetsFromSnapshot(snapshot, req)
	realtime := sessionsV3ReconnectRealtimeFromSnapshot(snapshot, subscriptions, worksets)
	return sessionsV3ReconnectResponseMap(sessionsV3ReconnectResponseInput{
		Rev:                       snapshot.Rev,
		ClientID:                  strings.TrimSpace(req.ClientID),
		Surface:                   normalizeV3SyncSurface(req.Surface),
		WorksetID:                 sessionsV3ReconnectWorksetID(req, snapshot),
		SnapshotEndpointCursor:    snapshot.SnapshotEndpointCursor,
		SessionsByID:              snapshot.SessionsByID,
		ProjectionsBySession:      snapshot.ProjectionsBySession,
		MessagesBySession:         nilIfEmptyMap(snapshot.MessagesBySession),
		EventsBySession:           nilIfEmptyMap(snapshot.EventsBySession),
		RunIntentsBySession:       nilIfEmptyMap(snapshot.RunIntentsBySession),
		CurrentRunStateBySession:  nilIfEmptyMap(snapshot.CurrentRunStateBySession),
		CurrentRunIntentBySession: sessionsV3ReconnectCurrentRunIntentsFromStates(snapshot.CurrentRunStateBySession),
		SessionViewsByID:          nilIfEmptyMap(snapshot.SessionViewsByID),
		ActiveSessionIDs:          snapshot.ActiveSessionIDs,
		HistoryManifestsBySession: nilIfEmptyMap(snapshot.HistoryManifestsBySession),
		HistoryChunksByID:         nilIfEmptyMap(snapshot.HistoryChunksByID),
		Omissions:                 snapshot.Omissions,
		Pagination:                snapshot.Pagination,
		Watermarks:                snapshot.Watermarks,
		Subscriptions:             subscriptions,
		Worksets:                  worksets,
		Realtime:                  realtime,
		SessionOrder:              snapshot.SessionOrder,
		DiagnosticsBySession:      map[string][]sessionsV3ReconnectDiagnostic{},
	})
}

func sessionsV3ReconnectSubscriptionsFromRealtime(realtime *sessionsV3RealtimeBootstrap) []sessionsV3ReconnectSubscription {
	if realtime == nil {
		return nil
	}
	out := make([]sessionsV3ReconnectSubscription, 0, len(realtime.Resume.Subscriptions))
	for _, sub := range realtime.Resume.Subscriptions {
		sessionID := strings.TrimSpace(sub.SessionID)
		if sessionID == "" {
			continue
		}
		out = append(out, sessionsV3ReconnectSubscription{
			Protocol:        V3RealtimeProtocol,
			ProtocolVersion: V3RealtimeProtocolVersion,
			Kind:            V3RealtimeKindSubscribe,
			SessionID:       sessionID,
			SubscriptionID:  strings.TrimSpace(sub.SubscriptionID),
			EndpointCursor:  strings.TrimSpace(sub.EndpointCursor),
		})
	}
	return out
}

func sessionsV3ReconnectWorksetsFromSnapshot(snapshot sessionsV3SyncSnapshotResponseBody, req sessionsV3ReconnectRequest) []V3RealtimeWorksetSubscriptionRequest {
	if snapshot.Realtime == nil || len(snapshot.Realtime.Resume.Worksets) == 0 || !sessionsV3ReconnectHasWorkset(req) {
		return nil
	}
	worksets := append([]V3RealtimeWorksetSubscriptionRequest(nil), snapshot.Realtime.Resume.Worksets...)
	worksetID := strings.TrimSpace(req.Workset.WorksetID)
	clientID := strings.TrimSpace(req.ClientID)
	for i := range worksets {
		if worksetID != "" {
			worksets[i].WorksetID = worksetID
			worksets[i].SubscriptionID = sessionsV3ReconnectWorksetSubscriptionID(clientID, worksetID)
		}
		worksets[i].AutoSubscribeSessions = false
	}
	return worksets
}

func sessionsV3ReconnectRealtimeFromSnapshot(snapshot sessionsV3SyncSnapshotResponseBody, subscriptions []sessionsV3ReconnectSubscription, worksets []V3RealtimeWorksetSubscriptionRequest) *sessionsV3RealtimeBootstrap {
	if strings.TrimSpace(snapshot.SnapshotEndpointCursor) == "" {
		return nil
	}
	return &sessionsV3RealtimeBootstrap{
		StreamPath: V3RealtimeStreamPath,
		Resume: V3RealtimeMessage{
			Protocol:        V3RealtimeProtocol,
			ProtocolVersion: V3RealtimeProtocolVersion,
			Kind:            V3RealtimeKindResume,
			EndpointCursor:  snapshot.SnapshotEndpointCursor,
			Subscriptions:   reconnectSubscriptionsToV3Realtime(subscriptions),
			Worksets:        worksets,
		},
	}
}

func sessionsV3ReconnectWorksetID(req sessionsV3ReconnectRequest, snapshot sessionsV3SyncSnapshotResponseBody) string {
	if !sessionsV3ReconnectHasWorkset(req) {
		return ""
	}
	if worksetID := strings.TrimSpace(req.Workset.WorksetID); worksetID != "" {
		return worksetID
	}
	if snapshot.Realtime != nil && len(snapshot.Realtime.Resume.Worksets) > 0 {
		return snapshot.Realtime.Resume.Worksets[0].WorksetID
	}
	return ""
}

func sessionsV3ReconnectCurrentRunIntentsFromStates(states map[string]pebblestore.V3SessionRunState) map[string]pebblestore.V3SessionRunIntent {
	if len(states) == 0 {
		return nil
	}
	out := make(map[string]pebblestore.V3SessionRunIntent, len(states))
	for sessionID, state := range states {
		if !state.Active || strings.TrimSpace(state.RunID) == "" {
			continue
		}
		out[sessionID] = pebblestore.V3SessionRunIntent{
			SessionID:      strings.TrimSpace(state.SessionID),
			UserID:         strings.TrimSpace(state.UserID),
			AccountScopeID: strings.TrimSpace(state.AccountScopeID),
			RunID:          strings.TrimSpace(state.RunID),
			Status:         strings.TrimSpace(state.Status),
			BlockedReason:  strings.TrimSpace(state.BlockedReason),
			CreatedAt:      state.CreatedAt,
			UpdatedAt:      state.UpdatedAt,
			EventSeq:       state.EventSeq,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func nilIfEmptyMap[M ~map[K]V, K comparable, V any](value M) any {
	if len(value) == 0 {
		return nil
	}
	return value
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
	RunIntentsBySession       any
	CurrentRunIntentBySession any
	CurrentRunStateBySession  any
	SessionViewsByID          any
	ActiveSessionIDs          []string
	HistoryManifestsBySession any
	HistoryChunksByID         any
	Omissions                 any
	Pagination                any
	Watermarks                any
	Subscriptions             []sessionsV3ReconnectSubscription
	Worksets                  []V3RealtimeWorksetSubscriptionRequest
	SessionOrder              []string
	DiagnosticsBySession      map[string][]sessionsV3ReconnectDiagnostic
	Realtime                  *sessionsV3RealtimeBootstrap
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
	if input.Realtime != nil {
		response["realtime"] = input.Realtime
	}
	optional := map[string]any{
		"run_intents_by_session":       input.RunIntentsBySession,
		"current_run_state_by_session": input.CurrentRunStateBySession,
		"session_views_by_id":          input.SessionViewsByID,
		"active_session_ids":           input.ActiveSessionIDs,
		"client_id":                    input.ClientID,
		"surface":                      input.Surface,
		"workset_id":                   input.WorksetID,
		"messages_by_session":          input.MessagesBySession,
		"events_by_session":            input.EventsBySession,
		"history_manifests_by_session": input.HistoryManifestsBySession,
		"history_chunks_by_id":         input.HistoryChunksByID,
		"omissions":                    input.Omissions,
		"pagination":                   input.Pagination,
		"watermarks":                   input.Watermarks,
		"worksets":                     input.Worksets,
	}
	for key, value := range optional {
		if !sessionsV3ReconnectEmptyValue(value) {
			response[key] = value
		}
	}
	return response
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
