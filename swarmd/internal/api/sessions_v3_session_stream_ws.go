package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sessionconnection"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const sessionConnectionReplayLimit = 500

func (s *Server) handleSessionConnectionStream(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	connectionID, ok := parseSessionConnectionStreamPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("invalid session connection stream path"))
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		writeError(w, http.StatusUnauthorized, errors.New("connection token is required"))
		return
	}
	svc, err := s.sessionConnectionService()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	connection, err := svc.ValidateStreamToken(token, connectionID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: connection.UserID, AccountScopeID: connection.AccountID}
	if _, ok, err := s.requireSessionV3Access(principal, connection.SessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !ok {
		writeError(w, http.StatusForbidden, errors.New("session is not visible to principal"))
		return
	}
	conn, err := transportws.Accept(w, r)
	if err != nil {
		if errors.Is(err, transportws.ErrUpgradeRequired) {
			writeError(w, http.StatusUpgradeRequired, errors.New("websocket upgrade required"))
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer conn.Close()
	s.streamSessionConnection(conn, svc, connection)
}

func parseSessionConnectionStreamPath(path string) (string, bool) {
	if !strings.HasPrefix(path, sessionConnectionStreamPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, sessionConnectionStreamPrefix)
	if !strings.HasSuffix(rest, "/stream") {
		return "", false
	}
	connectionID := strings.TrimSuffix(rest, "/stream")
	connectionID = strings.Trim(connectionID, "/")
	return connectionID, connectionID != "" && !strings.Contains(connectionID, "/")
}

func (s *Server) streamSessionConnection(conn *transportws.Conn, svc *sessionconnection.Service, connection sessionconnection.Connection) {
	lastEventSeq := connection.EventSeq
	lastEndpointSeq := connection.EndpointSeq
	if !s.replaySessionConnection(conn, svc, &connection, &lastEventSeq, &lastEndpointSeq) {
		return
	}
	readyToken, err := svc.AdvanceToken(connection, lastEventSeq, lastEndpointSeq)
	if err != nil {
		return
	}
	if err := sendSessionConnectionFrame(conn, SessionReadyFrame{Type: "session.ready", ConnectionId: connection.ConnectionID, SessionId: connection.SessionID, EventSeq: lastEventSeq, ResumeToken: readyToken}); err != nil {
		return
	}
	connection.EventSeq = lastEventSeq
	connection.EndpointSeq = lastEndpointSeq
	if s.v3RealtimeOutbox == nil {
		s.v3RealtimeOutbox = newV3RealtimeOutboxHub()
	}
	sub := s.v3RealtimeOutbox.subscribe()
	if sub == nil {
		return
	}
	defer s.v3RealtimeOutbox.unsubscribe(sub)
	for {
		select {
		case record := <-sub.send:
			if strings.TrimSpace(record.SessionID) != connection.SessionID {
				continue
			}
			if record.EndpointSeq <= lastEndpointSeq || record.Event.Seq <= lastEventSeq {
				continue
			}
			if !s.sendSessionConnectionRecord(conn, svc, &connection, record, &lastEventSeq, &lastEndpointSeq) {
				return
			}
		case <-sub.slow:
			_ = sendSessionConnectionFrame(conn, SessionReconnectRequiredFrame{Type: "session.reconnect_required", SessionId: connection.SessionID, Reason: string(SessionReconnectRequiredReasonSlowConsumer), Action: SessionErrorAction{Method: http.MethodPost, Path: "/v3/sessions/" + url.PathEscape(connection.SessionID) + ":connect"}})
			return
		case <-time.After(30 * time.Second):
			// Keep the connection open without client-originated control frames.
			continue
		}
	}
}

func (s *Server) replaySessionConnection(conn *transportws.Conn, svc *sessionconnection.Service, connection *sessionconnection.Connection, lastEventSeq, lastEndpointSeq *uint64) bool {
	current := *lastEndpointSeq
	for {
		records, err := s.sessions.ListRealtimeOutboxForSessionAfterEndpoint(connection.SessionID, current, sessionConnectionReplayLimit)
		if err != nil {
			return false
		}
		if len(records) == 0 {
			return true
		}
		for _, record := range records {
			if !s.sendSessionConnectionRecord(conn, svc, connection, record, lastEventSeq, lastEndpointSeq) {
				return false
			}
			current = *lastEndpointSeq
		}
		if len(records) < sessionConnectionReplayLimit {
			return true
		}
	}
}

func (s *Server) sendSessionConnectionRecord(conn *transportws.Conn, svc *sessionconnection.Service, connection *sessionconnection.Connection, record pebblestore.V3RealtimeOutboxRecord, lastEventSeq, lastEndpointSeq *uint64) bool {
	token, err := svc.AdvanceToken(*connection, record.Event.Seq, record.EndpointSeq)
	if err != nil {
		return false
	}
	if err := sendSessionConnectionFrame(conn, SessionEventFrame{Type: "session.event", SessionId: connection.SessionID, EventSeq: record.Event.Seq, Event: mustMarshalSessionConnectionEvent(record.Event), ResumeToken: token}); err != nil {
		return false
	}
	if phase, runID, ok := runPhaseFromSessionConnectionEvent(record.Event); ok {
		if err := sendSessionConnectionFrame(conn, RunPhaseFrame{Type: "run.phase", SessionId: connection.SessionID, RunId: runID, Phase: RunPhase(phase), EventSeq: record.Event.Seq}); err != nil {
			return false
		}
	}
	*lastEventSeq = record.Event.Seq
	*lastEndpointSeq = record.EndpointSeq
	connection.EventSeq = *lastEventSeq
	connection.EndpointSeq = *lastEndpointSeq
	return true
}

func runPhaseFromSessionConnectionEvent(event pebblestore.V3SessionEvent) (string, string, bool) {
	var payload struct {
		RunID         string `json:"run_id"`
		Status        string `json:"status"`
		Phase         string `json:"phase"`
		BlockedReason string `json:"blocked_reason"`
		RunIntent     *struct {
			RunID         string `json:"run_id"`
			Status        string `json:"status"`
			BlockedReason string `json:"blocked_reason"`
		} `json:"run_intent"`
	}
	if len(event.Payload) == 0 || json.Unmarshal(event.Payload, &payload) != nil {
		return "", "", false
	}
	runID := strings.TrimSpace(payload.RunID)
	status := strings.TrimSpace(payload.Status)
	blocked := strings.TrimSpace(payload.BlockedReason)
	if payload.RunIntent != nil {
		runID = firstNonEmpty(runID, payload.RunIntent.RunID)
		status = firstNonEmpty(status, payload.RunIntent.Status)
		blocked = firstNonEmpty(blocked, payload.RunIntent.BlockedReason)
	}
	phase := strings.TrimSpace(payload.Phase)
	if runID == "" || (status == "" && phase == "") {
		return "", "", false
	}
	if phase != "" {
		return phase, runID, true
	}
	return sessionConnectionRunPhaseFromStatus(status, blocked), runID, true
}

func sessionConnectionRunPhaseFromStatus(status, blocked string) string {
	switch strings.TrimSpace(status) {
	case "pending_executor":
		return "pending_executor"
	case "running":
		return "executor_started"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	case "interrupted":
		return "interrupted"
	case "dispatch_blocked":
		return "waiting_permission"
	default:
		if strings.TrimSpace(blocked) != "" {
			return "waiting_permission"
		}
		return "accepted"
	}
}

func mustMarshalSessionConnectionEvent(event pebblestore.V3SessionEvent) json.RawMessage {
	raw, err := json.Marshal(event)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func sendSessionConnectionFrame(conn *transportws.Conn, frame any) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.WriteText(raw)
}
