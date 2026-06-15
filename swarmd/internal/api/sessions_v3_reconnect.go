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

type sessionsV3ReconnectRequest struct{}

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
	_ = req

	response, err := s.sessionsV3ReconnectResponse(principal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
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

func (s *Server) sessionsV3ReconnectResponse(principal identity.Principal) (map[string]any, error) {
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

	return map[string]any{
		"ok":                            true,
		"rev":                           rev,
		"snapshot_endpoint_cursor":      signedSnapshotEndpointCursor,
		"sessions_by_id":                sessionsByID,
		"projections_by_session":        projectionsBySession,
		"run_intents_by_session":        runIntentsBySession,
		"current_run_intent_by_session": currentRunIntentBySession,
		"subscriptions":                 subscriptions,
		"session_order":                 sessionOrder,
		"diagnostics_by_session":        diagnosticsBySession,
	}, nil
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
