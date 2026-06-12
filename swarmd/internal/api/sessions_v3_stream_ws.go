package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const (
	sessionV3StreamReplayLimit       = 500
	sessionV3StreamKeepaliveInterval = 15 * time.Second
)

type sessionV3StreamHello struct {
	Type           string `json:"type"`
	AfterSeq       uint64 `json:"after_seq,omitempty"`
	EndpointCursor string `json:"endpoint_cursor,omitempty"`
}

type sessionV3StreamFrame struct {
	Type             string                           `json:"type"`
	OK               bool                             `json:"ok,omitempty"`
	SessionID        string                           `json:"session_id,omitempty"`
	ParentSessionID  string                           `json:"parent_session_id,omitempty"`
	Relation         string                           `json:"relation,omitempty"`
	LineageKind      string                           `json:"lineage_kind,omitempty"`
	AfterSeq         uint64                           `json:"after_seq,omitempty"`
	LastSeq          uint64                           `json:"last_seq,omitempty"`
	HighWatermarkSeq uint64                           `json:"high_watermark_seq,omitempty"`
	NextSeq          uint64                           `json:"next_seq,omitempty"`
	EndpointCursor   string                           `json:"endpoint_cursor,omitempty"`
	Error            string                           `json:"error,omitempty"`
	Event            *sessionruntime.SessionEvent     `json:"event,omitempty"`
	Projection       sessionruntime.SessionProjection `json:"projection,omitempty"`
}

type sessionV3StreamRoutedEvent struct {
	Event           sessionruntime.SessionEvent
	ParentSessionID string
	Relation        string
	LineageKind     string
}

type sessionV3StreamLineage struct {
	ChildSessionID  string
	ParentSessionID string
	UserID          string
	AccountScopeID  string
	LineageKind     string
}

func (l sessionV3StreamLineage) valid() bool {
	return strings.TrimSpace(l.ChildSessionID) != "" && strings.TrimSpace(l.ParentSessionID) != "" && strings.TrimSpace(l.ChildSessionID) != strings.TrimSpace(l.ParentSessionID) && strings.EqualFold(strings.TrimSpace(l.LineageKind), "delegated_subagent")
}

func (l sessionV3StreamLineage) matchesScope(userID, accountScopeID string) bool {
	if !l.valid() {
		return false
	}
	if strings.TrimSpace(accountScopeID) != "" && strings.TrimSpace(l.AccountScopeID) != "" && strings.TrimSpace(accountScopeID) != strings.TrimSpace(l.AccountScopeID) {
		return false
	}
	if strings.TrimSpace(userID) != "" && strings.TrimSpace(l.UserID) != "" && strings.TrimSpace(userID) != strings.TrimSpace(l.UserID) {
		return false
	}
	return true
}

func (s *Server) handleSessionV3PrimaryStream(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	if s.v3RealtimeOutbox == nil {
		s.v3RealtimeOutbox = newV3RealtimeOutboxHub()
	}
	if !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if _, found, err := s.hydrateSessionsV3Primary(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
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

	afterSeq, endpointSeq, hasResume, err := parseSessionV3StreamResume(r)
	if err != nil {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
		return
	}
	if !hasResume {
		raw, err := conn.ReadText()
		if err != nil {
			return
		}
		hello, err := decodeSessionV3StreamHello(raw)
		if err != nil {
			s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
			return
		}
		afterSeq = hello.AfterSeq
		if strings.TrimSpace(hello.EndpointCursor) != "" {
			parsed, err := parseV3RealtimeEndpointCursorStrict(hello.EndpointCursor)
			if err != nil {
				s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, Error: err.Error()})
				return
			}
			endpointSeq = parsed
		}
	}
	s.streamSessionV3PrimaryEvents(conn, sessionID, afterSeq, endpointSeq)
}

func parseSessionV3StreamResume(r *http.Request) (uint64, uint64, bool, error) {
	rawEndpointCursor := strings.TrimSpace(r.URL.Query().Get("endpoint_cursor"))
	if rawEndpointCursor != "" {
		parsed, err := parseV3RealtimeEndpointCursorStrict(rawEndpointCursor)
		return 0, parsed, true, err
	}
	raw := strings.TrimSpace(r.URL.Query().Get("after_seq"))
	if raw == "" {
		return 0, 0, false, nil
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, 0, true, errors.New("after_seq must be an unsigned integer")
	}
	return parsed, 0, true, nil
}

func decodeSessionV3StreamHello(raw []byte) (sessionV3StreamHello, error) {
	var hello sessionV3StreamHello
	if err := json.Unmarshal(raw, &hello); err != nil {
		return sessionV3StreamHello{}, fmt.Errorf("decode v3 stream hello: %w", err)
	}
	hello.Type = strings.ToLower(strings.TrimSpace(hello.Type))
	if hello.Type != "" && hello.Type != "session.stream.hello" && hello.Type != "hello" {
		return sessionV3StreamHello{}, fmt.Errorf("unsupported v3 stream hello type %q", hello.Type)
	}
	return hello, nil
}

func (s *Server) streamSessionV3PrimaryEvents(conn *transportws.Conn, sessionID string, afterSeq, endpointSeq uint64) {
	projection, ok, err := s.sessions.GetSessionProjection(sessionID)
	if err != nil {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, AfterSeq: afterSeq, Error: err.Error()})
		return
	}
	if !ok {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, AfterSeq: afterSeq, Error: "session projection not found"})
		return
	}

	if endpointSeq > 0 {
		if record, found, err := s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(sessionID, endpointSeq); err != nil {
			s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, EndpointCursor: pebbleV3RealtimeOutboxCursor(endpointSeq), Error: err.Error()})
			return
		} else if found {
			afterSeq = record.Event.Seq
		}
	}
	if afterSeq > projection.ProjectionHighWatermarkSeq {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: afterSeq, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(endpointSeq), Error: "after_seq is ahead of projection high watermark; refetch required"})
		return
	}

	subUserID := ""
	subAccountScopeID := ""
	if session, found, err := s.sessions.GetSession(sessionID); err == nil && found {
		subUserID = session.UserID
		subAccountScopeID = session.AccountScopeID
	}
	sub := s.v3RealtimeOutbox.subscribe()
	if sub == nil {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, Error: "unable to subscribe to v3 realtime outbox"})
		return
	}
	defer s.v3RealtimeOutbox.unsubscribe(sub)

	lastEndpointSeq := endpointSeq
	lastSent := afterSeq
	lastChildSeqBySessionID := map[string]uint64{}
	lineages := s.sessionV3StreamChildLineages(sessionID)
	s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "replay.started", OK: true, SessionID: sessionID, AfterSeq: afterSeq, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(endpointSeq)})

	replay, err := s.sessions.ListRealtimeOutboxForSessionAfterSeq(sessionID, afterSeq, sessionV3StreamReplayLimit)
	if err != nil {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: afterSeq, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(endpointSeq), Error: err.Error()})
		return
	}
	for _, record := range replay {
		lastEndpointSeq = maxUint64(lastEndpointSeq, record.EndpointSeq)
		event := record.Event
		if event.Seq <= lastSent {
			continue
		}
		if event.Seq != lastSent+1 {
			s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: lastSent, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(lastEndpointSeq), Error: fmt.Sprintf("event sequence gap at %d, want %d; refetch required", event.Seq, lastSent+1)})
			return
		}
		if !s.sendSessionV3StreamEvent(conn, sessionID, event, lastEndpointSeq) {
			return
		}
		lastSent = event.Seq
	}
	if !s.replaySessionV3StreamChildren(conn, sessionID, lineages, subUserID, subAccountScopeID, lastSent, lastChildSeqBySessionID, &lastEndpointSeq) {
		return
	}
	s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "replay.complete", OK: true, SessionID: sessionID, LastSeq: lastSent, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq, NextSeq: lastSent, EndpointCursor: pebbleV3RealtimeOutboxCursor(lastEndpointSeq)})

	ticker := time.NewTicker(sessionV3StreamKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sub.send:
			if !s.catchUpSessionV3StreamFromRealtimeOutbox(conn, sessionID, subUserID, subAccountScopeID, &lastEndpointSeq, &lastSent, lastChildSeqBySessionID, lineages) {
				return
			}
		case slow := <-sub.slow:
			s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: lastSent, NextSeq: slow.EndpointSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(lastEndpointSeq), Error: slow.Reason})
			return
		case <-ticker.C:
			if err := s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "keepalive", OK: true, SessionID: sessionID, LastSeq: lastSent, EndpointCursor: pebbleV3RealtimeOutboxCursor(lastEndpointSeq)}); err != nil {
				return
			}
		}
	}
}

func (s *Server) catchUpSessionV3StreamFromRealtimeOutbox(conn *transportws.Conn, parentSessionID, userID, accountScopeID string, lastEndpointSeq, parentLastSeq *uint64, lastChildSeqBySessionID map[string]uint64, lineages map[string]sessionV3StreamLineage) bool {
	if s == nil || s.sessions == nil || lastEndpointSeq == nil || parentLastSeq == nil {
		return false
	}
	current := *lastEndpointSeq
	for {
		records, err := s.sessions.ListRealtimeOutboxAfter(current, sessionV3StreamReplayLimit)
		if err != nil {
			s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: parentSessionID, AfterSeq: *parentLastSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(current), Error: err.Error()})
			return false
		}
		if len(records) == 0 {
			*lastEndpointSeq = current
			return true
		}
		for _, record := range records {
			if record.EndpointSeq <= current {
				continue
			}
			current = record.EndpointSeq
			routed, ok := s.routeSessionV3StreamOutboxRecord(parentSessionID, userID, accountScopeID, record, lineages)
			if !ok {
				continue
			}
			if routed.Relation == "child" {
				if !s.sendSessionV3StreamChildLiveEvent(conn, parentSessionID, routed, *parentLastSeq, lastChildSeqBySessionID, current) {
					return false
				}
				continue
			}
			event := routed.Event
			if event.Seq <= *parentLastSeq {
				continue
			}
			if event.Seq != *parentLastSeq+1 {
				s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: parentSessionID, AfterSeq: *parentLastSeq, HighWatermarkSeq: event.Seq, NextSeq: event.Seq, EndpointCursor: pebbleV3RealtimeOutboxCursor(current), Error: fmt.Sprintf("live event sequence gap at %d, want %d; refetch required", event.Seq, *parentLastSeq+1)})
				return false
			}
			if !s.sendSessionV3StreamEvent(conn, parentSessionID, event, current) {
				return false
			}
			*parentLastSeq = event.Seq
		}
		if len(records) < sessionV3StreamReplayLimit {
			*lastEndpointSeq = current
			return true
		}
	}
}

func (s *Server) routeSessionV3StreamOutboxRecord(parentSessionID, userID, accountScopeID string, record sessionruntime.RealtimeOutboxRecord, lineages map[string]sessionV3StreamLineage) (sessionV3StreamRoutedEvent, bool) {
	if record.EndpointSeq == 0 || record.Event.Seq == 0 {
		return sessionV3StreamRoutedEvent{}, false
	}
	if record.UserID != "" && userID != "" && record.UserID != userID {
		return sessionV3StreamRoutedEvent{}, false
	}
	if record.AccountScopeID != "" && accountScopeID != "" && record.AccountScopeID != accountScopeID {
		return sessionV3StreamRoutedEvent{}, false
	}
	if strings.TrimSpace(record.SessionID) == strings.TrimSpace(parentSessionID) {
		return sessionV3StreamRoutedEvent{Event: record.Event, Relation: "self"}, true
	}
	lineage, ok := lineages[record.SessionID]
	if !ok && s != nil && s.sessions != nil {
		if session, found, err := s.sessions.GetSession(record.SessionID); err == nil && found {
			lineage = sessionV3LineageFromSession(session)
			if lineage.valid() {
				lineages[record.SessionID] = lineage
			}
		}
	}
	if lineage.valid() && lineage.ParentSessionID == parentSessionID && lineage.matchesScope(userID, accountScopeID) {
		return sessionV3StreamRoutedEvent{Event: record.Event, ParentSessionID: parentSessionID, Relation: "child", LineageKind: lineage.LineageKind}, true
	}
	return sessionV3StreamRoutedEvent{}, false
}

func (s *Server) sendSessionV3StreamEvent(conn *transportws.Conn, sessionID string, event sessionruntime.SessionEvent, endpointSeq uint64) bool {
	if strings.TrimSpace(event.SessionID) != strings.TrimSpace(sessionID) {
		_ = s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, Error: "event belongs to a different session"})
		return false
	}
	return s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "event", OK: true, SessionID: sessionID, Relation: "self", LastSeq: event.Seq, EndpointCursor: pebbleV3RealtimeOutboxCursor(endpointSeq), Event: &event}) == nil
}

func (s *Server) replaySessionV3StreamChildren(conn *transportws.Conn, parentSessionID string, lineages map[string]sessionV3StreamLineage, userID, accountScopeID string, parentLastSeq uint64, lastChildSeqBySessionID map[string]uint64, lastEndpointSeq *uint64) bool {
	if s == nil || s.sessions == nil {
		return true
	}
	for _, lineage := range lineages {
		if !lineage.valid() || !lineage.matchesScope(userID, accountScopeID) {
			continue
		}
		replay, err := s.sessions.ListRealtimeOutboxForSessionAfterSeq(lineage.ChildSessionID, 0, sessionV3StreamReplayLimit)
		if err != nil {
			if !s.sendSessionV3StreamChildCursorError(conn, parentSessionID, lineage.ChildSessionID, parentLastSeq, 0, 0, *lastEndpointSeq, err) {
				return false
			}
			continue
		}
		var childLastSeq uint64
		for _, record := range replay {
			if lastEndpointSeq != nil {
				*lastEndpointSeq = maxUint64(*lastEndpointSeq, record.EndpointSeq)
			}
			event := record.Event
			if event.Seq <= childLastSeq {
				continue
			}
			if event.Seq != childLastSeq+1 {
				if !s.sendSessionV3StreamChildCursorError(conn, parentSessionID, lineage.ChildSessionID, parentLastSeq, childLastSeq+1, event.Seq, record.EndpointSeq, nil) {
					return false
				}
				childLastSeq = event.Seq
				continue
			}
			routed := sessionV3StreamRoutedEvent{Event: event, ParentSessionID: parentSessionID, Relation: "child", LineageKind: lineage.LineageKind}
			if !s.sendSessionV3StreamRoutedEvent(conn, parentSessionID, routed, parentLastSeq, record.EndpointSeq) {
				return false
			}
			childLastSeq = event.Seq
		}
		lastChildSeqBySessionID[lineage.ChildSessionID] = childLastSeq
	}
	return true
}

func (s *Server) sendSessionV3StreamChildLiveEvent(conn *transportws.Conn, parentSessionID string, routed sessionV3StreamRoutedEvent, parentLastSeq uint64, lastChildSeqBySessionID map[string]uint64, endpointSeq uint64) bool {
	event := routed.Event
	childSessionID := strings.TrimSpace(event.SessionID)
	if childSessionID == "" || childSessionID == strings.TrimSpace(parentSessionID) {
		_ = s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: parentSessionID, Error: "child event is missing child session identity"})
		return false
	}
	lastChildSeq := lastChildSeqBySessionID[childSessionID]
	if event.Seq <= lastChildSeq {
		return true
	}
	if event.Seq != lastChildSeq+1 {
		if !s.sendSessionV3StreamChildCursorError(conn, parentSessionID, childSessionID, parentLastSeq, lastChildSeq+1, event.Seq, endpointSeq, nil) {
			return false
		}
		lastChildSeqBySessionID[childSessionID] = event.Seq
		return true
	}
	if !s.sendSessionV3StreamRoutedEvent(conn, parentSessionID, routed, parentLastSeq, endpointSeq) {
		return false
	}
	lastChildSeqBySessionID[childSessionID] = event.Seq
	return true
}

func (s *Server) sendSessionV3StreamRoutedEvent(conn *transportws.Conn, parentSessionID string, routed sessionV3StreamRoutedEvent, parentLastSeq, endpointSeq uint64) bool {
	event := routed.Event
	if strings.TrimSpace(event.SessionID) == "" || strings.TrimSpace(event.SessionID) == strings.TrimSpace(parentSessionID) {
		_ = s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: parentSessionID, Error: "child event is missing child session identity"})
		return false
	}
	frame := sessionV3StreamFrame{Type: "event", OK: true, SessionID: event.SessionID, ParentSessionID: parentSessionID, Relation: "child", LineageKind: routed.LineageKind, AfterSeq: parentLastSeq, LastSeq: parentLastSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(endpointSeq), Event: &event}
	return s.sendSessionV3StreamFrame(conn, frame) == nil
}

func (s *Server) sendSessionV3StreamChildCursorError(conn *transportws.Conn, parentSessionID, childSessionID string, parentLastSeq, wantSeq, gotSeq, endpointSeq uint64, err error) bool {
	message := fmt.Sprintf("child event sequence gap at %d, want %d; child refetch required", gotSeq, wantSeq)
	if err != nil {
		message = "child event replay failed; child refetch required: " + err.Error()
	}
	frame := sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: childSessionID, ParentSessionID: parentSessionID, Relation: "child", AfterSeq: parentLastSeq, NextSeq: gotSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(endpointSeq), Error: message}
	return s.sendSessionV3StreamFrame(conn, frame) == nil
}

func (s *Server) sessionV3StreamChildLineages(parentSessionID string) map[string]sessionV3StreamLineage {
	out := map[string]sessionV3StreamLineage{}
	if s == nil || s.sessions == nil {
		return out
	}
	parent, found, err := s.sessions.GetSession(parentSessionID)
	if err != nil || !found || strings.TrimSpace(parent.AccountScopeID) == "" {
		return out
	}
	sessions, err := s.sessions.ListSessionsForAccount(parent.AccountScopeID, 1000)
	if err != nil {
		return out
	}
	for _, session := range sessions {
		lineage := sessionV3LineageFromSession(session)
		if lineage.valid() && lineage.ParentSessionID == parentSessionID {
			out[lineage.ChildSessionID] = lineage
		}
	}
	return out
}

func sessionV3LineageFromSession(session pebblestore.SessionSnapshot) sessionV3StreamLineage {
	metadata := session.Metadata
	childID := strings.TrimSpace(session.ID)
	parentID := sessionV3MetadataString(metadata, "parent_session_id")
	lineageKind := sessionV3MetadataString(metadata, "lineage_kind")
	if parentID == "" || parentID == childID || !strings.EqualFold(lineageKind, "delegated_subagent") {
		return sessionV3StreamLineage{}
	}
	return sessionV3StreamLineage{ChildSessionID: childID, ParentSessionID: parentID, UserID: strings.TrimSpace(session.UserID), AccountScopeID: strings.TrimSpace(session.AccountScopeID), LineageKind: lineageKind}
}

func (s *Server) sendSessionV3StreamFrame(conn *transportws.Conn, frame sessionV3StreamFrame) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.WriteText(raw)
}

func maxUint64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
