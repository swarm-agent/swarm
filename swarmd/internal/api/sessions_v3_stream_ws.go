package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const (
	sessionV3StreamReplayLimit       = 500
	sessionV3StreamSubscriberBufSize = 256
	sessionV3StreamKeepaliveInterval = 15 * time.Second
)

type sessionV3StreamHello struct {
	Type     string `json:"type"`
	AfterSeq uint64 `json:"after_seq,omitempty"`
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
	Error            string                           `json:"error,omitempty"`
	Event            *sessionruntime.SessionEvent     `json:"event,omitempty"`
	Projection       sessionruntime.SessionProjection `json:"projection,omitempty"`
}

type sessionV3StreamSlowConsumer struct {
	NextSeq uint64
	Reason  string
}

type sessionV3StreamRoutedEvent struct {
	Event           sessionruntime.SessionEvent
	ParentSessionID string
	Relation        string
	LineageKind     string
}

type sessionV3StreamSubscriber struct {
	id             string
	sessionID      string
	userID         string
	accountScopeID string
	send           chan sessionV3StreamRoutedEvent
	slow           chan sessionV3StreamSlowConsumer
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

func (l sessionV3StreamLineage) matchesSubscriber(sub *sessionV3StreamSubscriber) bool {
	if sub == nil || !l.valid() {
		return false
	}
	if strings.TrimSpace(sub.accountScopeID) != "" && strings.TrimSpace(l.AccountScopeID) != "" && strings.TrimSpace(sub.accountScopeID) != strings.TrimSpace(l.AccountScopeID) {
		return false
	}
	if strings.TrimSpace(sub.userID) != "" && strings.TrimSpace(l.UserID) != "" && strings.TrimSpace(sub.userID) != strings.TrimSpace(l.UserID) {
		return false
	}
	return true
}

type sessionV3StreamHub struct {
	subs           map[string]map[string]*sessionV3StreamSubscriber
	childLineage   map[string]sessionV3StreamLineage
	parentChildren map[string]map[string]struct{}
	nextSub        uint64
	mu             sync.Mutex
}

func newSessionV3StreamHub() *sessionV3StreamHub {
	return &sessionV3StreamHub{
		subs:           make(map[string]map[string]*sessionV3StreamSubscriber),
		childLineage:   make(map[string]sessionV3StreamLineage),
		parentChildren: make(map[string]map[string]struct{}),
	}
}

func (h *sessionV3StreamHub) subscribe(sessionID string) *sessionV3StreamSubscriber {
	return h.subscribeScoped(sessionID, "", "")
}

func (h *sessionV3StreamHub) subscribeScoped(sessionID, userID, accountScopeID string) *sessionV3StreamSubscriber {
	if h == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextSub++
	sub := &sessionV3StreamSubscriber{
		id:             fmt.Sprintf("v3sub_%d", h.nextSub),
		sessionID:      sessionID,
		userID:         strings.TrimSpace(userID),
		accountScopeID: strings.TrimSpace(accountScopeID),
		send:           make(chan sessionV3StreamRoutedEvent, sessionV3StreamSubscriberBufSize),
		slow:           make(chan sessionV3StreamSlowConsumer, 1),
	}
	if h.subs[sessionID] == nil {
		h.subs[sessionID] = make(map[string]*sessionV3StreamSubscriber)
	}
	h.subs[sessionID][sub.id] = sub
	return sub
}

func (h *sessionV3StreamHub) unsubscribe(sub *sessionV3StreamSubscriber) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.subs[sub.sessionID]
	if subs == nil {
		return
	}
	if _, ok := subs[sub.id]; ok {
		delete(subs, sub.id)
	}
	if len(subs) == 0 {
		delete(h.subs, sub.sessionID)
	}
}

func (h *sessionV3StreamHub) publish(event sessionruntime.SessionEvent) {
	if h == nil || strings.TrimSpace(event.SessionID) == "" {
		return
	}
	routed := h.subscribersForEvent(event)
	for _, item := range routed {
		select {
		case item.sub.send <- item.event:
		default:
			h.markSlowConsumer(item.sub, item.event.Event.Seq)
		}
	}
}

func (h *sessionV3StreamHub) subscribersForEvent(event sessionruntime.SessionEvent) []struct {
	sub   *sessionV3StreamSubscriber
	event sessionV3StreamRoutedEvent
} {
	if h == nil {
		return nil
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return nil
	}
	h.mu.Lock()
	out := make([]struct {
		sub   *sessionV3StreamSubscriber
		event sessionV3StreamRoutedEvent
	}, 0, len(h.subs[sessionID]))
	for _, sub := range h.subs[sessionID] {
		if sub != nil {
			out = append(out, struct {
				sub   *sessionV3StreamSubscriber
				event sessionV3StreamRoutedEvent
			}{sub: sub, event: sessionV3StreamRoutedEvent{Event: event, Relation: "self"}})
		}
	}
	if lineage, ok := h.childLineage[sessionID]; ok && strings.EqualFold(lineage.LineageKind, "delegated_subagent") {
		parentSessionID := strings.TrimSpace(lineage.ParentSessionID)
		if parentSessionID != "" && parentSessionID != sessionID {
			for _, sub := range h.subs[parentSessionID] {
				if sub != nil && lineage.matchesSubscriber(sub) {
					out = append(out, struct {
						sub   *sessionV3StreamSubscriber
						event sessionV3StreamRoutedEvent
					}{sub: sub, event: sessionV3StreamRoutedEvent{Event: event, ParentSessionID: parentSessionID, Relation: "child", LineageKind: lineage.LineageKind}})
				}
			}
		}
	}
	h.mu.Unlock()
	return out
}

func (h *sessionV3StreamHub) registerLineage(lineage sessionV3StreamLineage) {
	if h == nil {
		return
	}
	lineage.ChildSessionID = strings.TrimSpace(lineage.ChildSessionID)
	lineage.ParentSessionID = strings.TrimSpace(lineage.ParentSessionID)
	lineage.UserID = strings.TrimSpace(lineage.UserID)
	lineage.AccountScopeID = strings.TrimSpace(lineage.AccountScopeID)
	lineage.LineageKind = strings.TrimSpace(lineage.LineageKind)
	if lineage.ChildSessionID == "" || lineage.ParentSessionID == "" || lineage.ChildSessionID == lineage.ParentSessionID || !strings.EqualFold(lineage.LineageKind, "delegated_subagent") {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.childLineage[lineage.ChildSessionID] = lineage
	if h.parentChildren[lineage.ParentSessionID] == nil {
		h.parentChildren[lineage.ParentSessionID] = make(map[string]struct{})
	}
	h.parentChildren[lineage.ParentSessionID][lineage.ChildSessionID] = struct{}{}
}

func (h *sessionV3StreamHub) seedLineage(sessions []pebblestore.SessionSnapshot, parentSessionID string) {
	if h == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	for _, session := range sessions {
		lineage := sessionV3LineageFromSession(session)
		if lineage.ParentSessionID == parentSessionID {
			h.registerLineage(lineage)
		}
	}
}

func (h *sessionV3StreamHub) childLineagesForParent(parentSessionID string) []sessionV3StreamLineage {
	if h == nil {
		return nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	childIDs := h.parentChildren[parentSessionID]
	lineages := make([]sessionV3StreamLineage, 0, len(childIDs))
	for childID := range childIDs {
		lineage := h.childLineage[childID]
		if lineage.valid() && lineage.ParentSessionID == parentSessionID {
			lineages = append(lineages, lineage)
		}
	}
	return lineages
}

func (h *sessionV3StreamHub) markSlowConsumer(sub *sessionV3StreamSubscriber, nextSeq uint64) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	subs := h.subs[sub.sessionID]
	if subs == nil || subs[sub.id] == nil {
		h.mu.Unlock()
		return
	}
	delete(subs, sub.id)
	if len(subs) == 0 {
		delete(h.subs, sub.sessionID)
	}
	h.mu.Unlock()

	notice := sessionV3StreamSlowConsumer{
		NextSeq: nextSeq,
		Reason:  fmt.Sprintf("slow_consumer: subscriber queue full before event seq %d; reconnect required", nextSeq),
	}
	select {
	case sub.slow <- notice:
	default:
	}
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
	if s.v3SessionStreams == nil {
		s.v3SessionStreams = newSessionV3StreamHub()
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

	afterSeq, hasAfterSeq, err := parseSessionV3StreamAfterSeq(r)
	if err != nil {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, Error: err.Error()})
		return
	}
	if !hasAfterSeq {
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
	}
	s.streamSessionV3PrimaryEvents(conn, sessionID, afterSeq)
}

func parseSessionV3StreamAfterSeq(r *http.Request) (uint64, bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("after_seq"))
	if raw == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, true, errors.New("after_seq must be an unsigned integer")
	}
	return parsed, true, nil
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

func (s *Server) streamSessionV3PrimaryEvents(conn *transportws.Conn, sessionID string, afterSeq uint64) {
	s.seedSessionV3StreamLineage(sessionID)
	projection, ok, err := s.sessions.GetSessionProjection(sessionID)
	if err != nil {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, AfterSeq: afterSeq, Error: err.Error()})
		return
	}
	if !ok {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, AfterSeq: afterSeq, Error: "session projection not found"})
		return
	}
	if afterSeq > projection.ProjectionHighWatermarkSeq {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: afterSeq, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq, Error: "after_seq is ahead of projection high watermark; refetch required"})
		return
	}

	subUserID := ""
	subAccountScopeID := ""
	if session, found, err := s.sessions.GetSession(sessionID); err == nil && found {
		subUserID = session.UserID
		subAccountScopeID = session.AccountScopeID
	}
	sub := s.v3SessionStreams.subscribeScoped(sessionID, subUserID, subAccountScopeID)
	if sub == nil {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, Error: "unable to subscribe to v3 session stream"})
		return
	}
	defer s.v3SessionStreams.unsubscribe(sub)

	replay, err := s.sessions.ReplaySessionEvents(sessionID, afterSeq, sessionV3StreamReplayLimit)
	if err != nil {
		s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: afterSeq, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq, Error: err.Error()})
		return
	}
	lastSent := afterSeq
	lastChildSeqBySessionID := map[string]uint64{}
	s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "replay.started", OK: true, SessionID: sessionID, AfterSeq: afterSeq, HighWatermarkSeq: replay.Projection.ProjectionHighWatermarkSeq})
	for _, event := range replay.Events {
		if event.Seq <= lastSent {
			continue
		}
		if event.Seq != lastSent+1 {
			s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: lastSent, HighWatermarkSeq: replay.Projection.ProjectionHighWatermarkSeq, Error: fmt.Sprintf("event sequence gap at %d, want %d; refetch required", event.Seq, lastSent+1)})
			return
		}
		if !s.sendSessionV3StreamEvent(conn, sessionID, event) {
			return
		}
		lastSent = event.Seq
	}
	if !s.replaySessionV3StreamChildren(conn, sessionID, sub, lastSent, lastChildSeqBySessionID) {
		return
	}
	s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "replay.complete", OK: true, SessionID: sessionID, LastSeq: lastSent, HighWatermarkSeq: replay.Projection.ProjectionHighWatermarkSeq, NextSeq: lastSent})

	ticker := time.NewTicker(sessionV3StreamKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case routed := <-sub.send:
			if routed.Relation == "child" {
				if !s.sendSessionV3StreamChildLiveEvent(conn, sessionID, routed, lastSent, lastChildSeqBySessionID) {
					return
				}
				continue
			}
			event := routed.Event
			if event.Seq <= lastSent {
				continue
			}
			if event.Seq != lastSent+1 {
				s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: lastSent, HighWatermarkSeq: event.Seq, NextSeq: event.Seq, Error: fmt.Sprintf("live event sequence gap at %d, want %d; refetch required", event.Seq, lastSent+1)})
				return
			}
			if !s.sendSessionV3StreamEvent(conn, sessionID, event) {
				return
			}
			lastSent = event.Seq
		case slow := <-sub.slow:
			s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: lastSent, NextSeq: slow.NextSeq, Error: slow.Reason})
			return
		case <-ticker.C:
			if err := s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "keepalive", OK: true, SessionID: sessionID, LastSeq: lastSent}); err != nil {
				return
			}
		}
	}
}

func (s *Server) sendSessionV3StreamEvent(conn *transportws.Conn, sessionID string, event sessionruntime.SessionEvent) bool {
	if strings.TrimSpace(event.SessionID) != strings.TrimSpace(sessionID) {
		_ = s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: sessionID, Error: "event belongs to a different session"})
		return false
	}
	return s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "event", OK: true, SessionID: sessionID, Relation: "self", LastSeq: event.Seq, Event: &event}) == nil
}

func (s *Server) replaySessionV3StreamChildren(conn *transportws.Conn, parentSessionID string, sub *sessionV3StreamSubscriber, parentLastSeq uint64, lastChildSeqBySessionID map[string]uint64) bool {
	if s == nil || s.sessions == nil || s.v3SessionStreams == nil {
		return true
	}
	for _, lineage := range s.v3SessionStreams.childLineagesForParent(parentSessionID) {
		if !lineage.valid() || !lineage.matchesSubscriber(sub) {
			continue
		}
		replay, err := s.sessions.ReplaySessionEvents(lineage.ChildSessionID, 0, sessionV3StreamReplayLimit)
		if err != nil {
			if !s.sendSessionV3StreamChildCursorError(conn, parentSessionID, lineage.ChildSessionID, parentLastSeq, 0, 0, err) {
				return false
			}
			continue
		}
		var childLastSeq uint64
		for _, event := range replay.Events {
			if event.Seq <= childLastSeq {
				continue
			}
			if event.Seq != childLastSeq+1 {
				if !s.sendSessionV3StreamChildCursorError(conn, parentSessionID, lineage.ChildSessionID, parentLastSeq, childLastSeq+1, event.Seq, nil) {
					return false
				}
				childLastSeq = event.Seq
				continue
			}
			routed := sessionV3StreamRoutedEvent{Event: event, ParentSessionID: parentSessionID, Relation: "child", LineageKind: lineage.LineageKind}
			if !s.sendSessionV3StreamRoutedEvent(conn, parentSessionID, routed, parentLastSeq) {
				return false
			}
			childLastSeq = event.Seq
		}
		lastChildSeqBySessionID[lineage.ChildSessionID] = childLastSeq
		if replay.Projection.ProjectionHighWatermarkSeq > childLastSeq {
			if !s.sendSessionV3StreamChildCursorError(conn, parentSessionID, lineage.ChildSessionID, parentLastSeq, childLastSeq+1, replay.Projection.ProjectionHighWatermarkSeq, nil) {
				return false
			}
			lastChildSeqBySessionID[lineage.ChildSessionID] = replay.Projection.ProjectionHighWatermarkSeq
		}
	}
	return true
}

func (s *Server) sendSessionV3StreamChildLiveEvent(conn *transportws.Conn, parentSessionID string, routed sessionV3StreamRoutedEvent, parentLastSeq uint64, lastChildSeqBySessionID map[string]uint64) bool {
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
		if !s.sendSessionV3StreamChildCursorError(conn, parentSessionID, childSessionID, parentLastSeq, lastChildSeq+1, event.Seq, nil) {
			return false
		}
		lastChildSeqBySessionID[childSessionID] = event.Seq
		return true
	}
	if !s.sendSessionV3StreamRoutedEvent(conn, parentSessionID, routed, parentLastSeq) {
		return false
	}
	lastChildSeqBySessionID[childSessionID] = event.Seq
	return true
}

func (s *Server) sendSessionV3StreamRoutedEvent(conn *transportws.Conn, parentSessionID string, routed sessionV3StreamRoutedEvent, parentLastSeq uint64) bool {
	event := routed.Event
	if strings.TrimSpace(event.SessionID) == "" || strings.TrimSpace(event.SessionID) == strings.TrimSpace(parentSessionID) {
		_ = s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "error", OK: false, SessionID: parentSessionID, Error: "child event is missing child session identity"})
		return false
	}
	frame := sessionV3StreamFrame{Type: "event", OK: true, SessionID: event.SessionID, ParentSessionID: parentSessionID, Relation: "child", LineageKind: routed.LineageKind, AfterSeq: parentLastSeq, LastSeq: parentLastSeq, Event: &event}
	return s.sendSessionV3StreamFrame(conn, frame) == nil
}

func (s *Server) sendSessionV3StreamChildCursorError(conn *transportws.Conn, parentSessionID, childSessionID string, parentLastSeq, wantSeq, gotSeq uint64, err error) bool {
	message := fmt.Sprintf("child event sequence gap at %d, want %d; child refetch required", gotSeq, wantSeq)
	if err != nil {
		message = "child event replay failed; child refetch required: " + err.Error()
	}
	frame := sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: childSessionID, ParentSessionID: parentSessionID, Relation: "child", AfterSeq: parentLastSeq, NextSeq: gotSeq, Error: message}
	return s.sendSessionV3StreamFrame(conn, frame) == nil
}

func (s *Server) registerSessionV3StreamLineageFromResult(result sessionruntime.SessionMutationResult) {
	if s == nil || s.v3SessionStreams == nil || result.Session == nil {
		return
	}
	s.v3SessionStreams.registerLineage(sessionV3LineageFromSession(*result.Session))
}

func (s *Server) seedSessionV3StreamLineage(parentSessionID string) {
	if s == nil || s.sessions == nil || s.v3SessionStreams == nil {
		return
	}
	parent, found, err := s.sessions.GetSession(parentSessionID)
	if err != nil || !found || strings.TrimSpace(parent.AccountScopeID) == "" {
		return
	}
	sessions, err := s.sessions.ListSessionsForAccount(parent.AccountScopeID, 1000)
	if err != nil {
		return
	}
	s.v3SessionStreams.seedLineage(sessions, parentSessionID)
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
