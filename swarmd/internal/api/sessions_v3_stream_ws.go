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
	AfterSeq         uint64                           `json:"after_seq,omitempty"`
	LastSeq          uint64                           `json:"last_seq,omitempty"`
	HighWatermarkSeq uint64                           `json:"high_watermark_seq,omitempty"`
	NextSeq          uint64                           `json:"next_seq,omitempty"`
	Error            string                           `json:"error,omitempty"`
	Event            *sessionruntime.SessionEvent     `json:"event,omitempty"`
	Projection       sessionruntime.SessionProjection `json:"projection,omitempty"`
}

type sessionV3StreamSubscriber struct {
	id        string
	sessionID string
	send      chan sessionruntime.SessionEvent
}

type sessionV3StreamHub struct {
	subs    map[string]map[string]*sessionV3StreamSubscriber
	nextSub uint64
	mu      sync.Mutex
}

func newSessionV3StreamHub() *sessionV3StreamHub {
	return &sessionV3StreamHub{subs: make(map[string]map[string]*sessionV3StreamSubscriber)}
}

func (h *sessionV3StreamHub) subscribe(sessionID string) *sessionV3StreamSubscriber {
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
		id:        fmt.Sprintf("v3sub_%d", h.nextSub),
		sessionID: sessionID,
		send:      make(chan sessionruntime.SessionEvent, sessionV3StreamSubscriberBufSize),
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
	if existing, ok := subs[sub.id]; ok {
		delete(subs, sub.id)
		close(existing.send)
	}
	if len(subs) == 0 {
		delete(h.subs, sub.sessionID)
	}
}

func (h *sessionV3StreamHub) publish(event sessionruntime.SessionEvent) {
	if h == nil || strings.TrimSpace(event.SessionID) == "" {
		return
	}
	h.mu.Lock()
	subs := make([]*sessionV3StreamSubscriber, 0, len(h.subs[event.SessionID]))
	for _, sub := range h.subs[event.SessionID] {
		if sub != nil {
			subs = append(subs, sub)
		}
	}
	h.mu.Unlock()
	for _, sub := range subs {
		select {
		case sub.send <- event:
		default:
		}
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

	sub := s.v3SessionStreams.subscribe(sessionID)
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
	s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "replay.complete", OK: true, SessionID: sessionID, LastSeq: lastSent, HighWatermarkSeq: replay.Projection.ProjectionHighWatermarkSeq, NextSeq: lastSent})

	ticker := time.NewTicker(sessionV3StreamKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case event, ok := <-sub.send:
			if !ok {
				return
			}
			if event.Seq <= lastSent {
				continue
			}
			if event.Seq != lastSent+1 {
				s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "cursor.error", OK: false, SessionID: sessionID, AfterSeq: lastSent, Error: fmt.Sprintf("live event sequence gap at %d, want %d; refetch required", event.Seq, lastSent+1)})
				return
			}
			if !s.sendSessionV3StreamEvent(conn, sessionID, event) {
				return
			}
			lastSent = event.Seq
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
	return s.sendSessionV3StreamFrame(conn, sessionV3StreamFrame{Type: "event", OK: true, SessionID: sessionID, LastSeq: event.Seq, Event: &event}) == nil
}

func (s *Server) sendSessionV3StreamFrame(conn *transportws.Conn, frame sessionV3StreamFrame) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.WriteText(raw)
}
