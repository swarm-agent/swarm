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
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const (
	v3RealtimeReplayLimit       = 500
	v3RealtimeKeepaliveInterval = 15 * time.Second
)

type v3RealtimeSubscription struct {
	SessionID      string
	SubscriptionID string
	LastSeq        uint64
}

func (s *Server) handleV3RealtimeStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	if s.v3RealtimeOutbox == nil {
		s.v3RealtimeOutbox = newV3RealtimeOutboxHub()
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

	sub := s.v3RealtimeOutbox.subscribe()
	if sub == nil {
		s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSlowConsumer, ErrorCode: "subscribe_failed", Error: "unable to subscribe to v3 realtime outbox"})
		return
	}
	defer s.v3RealtimeOutbox.unsubscribe(sub)

	endpointCursor := parseV3RealtimeEndpointCursor(r.URL.Query().Get("endpoint_cursor"))
	subs := map[string]v3RealtimeSubscription{}
	lastEndpointSeq := endpointCursor
	lastKeepaliveSeq := endpointCursor

	if rawSessions := strings.TrimSpace(r.URL.Query().Get("sessions")); rawSessions != "" {
		for _, sessionID := range strings.Split(rawSessions, ",") {
			sessionID = strings.TrimSpace(sessionID)
			if sessionID == "" {
				continue
			}
			subID := "sub-" + sessionID
			last, ok := s.v3RealtimeSubscribeSession(conn, principal, subID, sessionID, 0)
			if ok {
				subs[sessionID] = v3RealtimeSubscription{SessionID: sessionID, SubscriptionID: subID, LastSeq: last}
			}
		}
	}

	readMessages := make(chan V3RealtimeMessage, 8)
	readErr := make(chan error, 1)
	go func() {
		for {
			raw, err := conn.ReadText()
			if err != nil {
				readErr <- err
				return
			}
			var message V3RealtimeMessage
			if err := json.Unmarshal(raw, &message); err != nil {
				readErr <- fmt.Errorf("decode v3 realtime message: %w", err)
				return
			}
			if err := ValidateV3RealtimeMessage(message); err != nil {
				readErr <- err
				return
			}
			readMessages <- message
		}
	}()

	ticker := time.NewTicker(v3RealtimeKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-readErr:
			if err != nil {
				return
			}
		case message := <-readMessages:
			switch message.Kind {
			case V3RealtimeKindSubscribe:
				last, ok := s.v3RealtimeSubscribeSession(conn, principal, message.SubscriptionID, message.SessionID, message.AfterSeq)
				if ok {
					subs[message.SessionID] = v3RealtimeSubscription{SessionID: message.SessionID, SubscriptionID: message.SubscriptionID, LastSeq: last}
				}
			case V3RealtimeKindUnsubscribe:
				for sessionID, sub := range subs {
					if sub.SubscriptionID == message.SubscriptionID || sessionID == message.SessionID {
						delete(subs, sessionID)
					}
				}
			case V3RealtimeKindResume:
				lastEndpointSeq = parseV3RealtimeEndpointCursor(message.EndpointCursor)
				lastKeepaliveSeq = lastEndpointSeq
			}
		case record := <-sub.send:
			if record.EndpointSeq <= lastEndpointSeq {
				continue
			}
			if record.EndpointSeq != lastEndpointSeq+1 && lastEndpointSeq != 0 {
				_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_cursor_gap", fmt.Sprintf("endpoint sequence gap at %d, want %d; reconnect required", record.EndpointSeq, lastEndpointSeq+1), lastEndpointSeq, record.EndpointSeq))
				return
			}
			lastEndpointSeq = record.EndpointSeq
			if !s.v3RealtimePrincipalCanSee(principal, record) {
				continue
			}
			subscription, ok := subs[record.SessionID]
			if !ok {
				continue
			}
			if record.Event.Seq <= subscription.LastSeq {
				continue
			}
			if record.Event.Seq != subscription.LastSeq+1 {
				_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(record.SessionID, "session_cursor_gap", fmt.Sprintf("session event sequence gap at %d, want %d; refetch required", record.Event.Seq, subscription.LastSeq+1), subscription.LastSeq, record.Event.Seq))
				delete(subs, record.SessionID)
				continue
			}
			if !s.sendV3RealtimeOutboxEvent(conn, record) {
				return
			}
			subscription.LastSeq = record.Event.Seq
			subs[record.SessionID] = subscription
		case slow := <-sub.slow:
			_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSlowConsumer, NextSeq: slow.EndpointSeq, ErrorCode: "slow_consumer", Reason: slow.Reason})
			return
		case <-ticker.C:
			if err := s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindKeepalive, LastSeq: lastKeepaliveSeq, EndpointCursor: pebbleV3RealtimeOutboxCursor(lastEndpointSeq)}); err != nil {
				return
			}
			lastKeepaliveSeq = lastEndpointSeq
		}
	}
}

func (s *Server) v3RealtimeSubscribeSession(conn *transportws.Conn, principal identity.Principal, subscriptionID, sessionID string, afterSeq uint64) (uint64, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "missing_session_id", "subscribe.session requires session_id", 0, 0))
		return 0, false
	}
	if _, found, err := s.hydrateSessionsV3Primary(principal, sessionID); err != nil {
		_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, SessionID: sessionID, ErrorCode: "auth_denied", Error: err.Error()})
		return 0, false
	} else if !found {
		_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, SessionID: sessionID, ErrorCode: "not_found", Error: "session not found"})
		return 0, false
	}
	projection, ok, err := s.sessions.GetSessionProjection(sessionID)
	if err != nil || !ok {
		msg := "session projection not found"
		if err != nil {
			msg = err.Error()
		}
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(sessionID, "projection_missing", msg, afterSeq, 0))
		return 0, false
	}
	if afterSeq > projection.ProjectionHighWatermarkSeq {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(sessionID, "cursor_ahead", "after_seq is ahead of projection high watermark; refetch required", afterSeq, projection.ProjectionHighWatermarkSeq))
		return 0, false
	}

	_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayStart, SessionID: sessionID, SubscriptionID: subscriptionID, AfterSeq: afterSeq, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq})
	records, err := s.sessions.ListRealtimeOutboxForSessionAfterSeq(sessionID, afterSeq, v3RealtimeReplayLimit)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(sessionID, "replay_failed", err.Error(), afterSeq, projection.ProjectionHighWatermarkSeq))
		return afterSeq, false
	}
	lastSeq := afterSeq
	for _, record := range records {
		if record.Event.Seq != lastSeq+1 {
			_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(sessionID, "session_cursor_gap", fmt.Sprintf("session event sequence gap at %d, want %d; refetch required", record.Event.Seq, lastSeq+1), lastSeq, record.Event.Seq))
			return lastSeq, false
		}
		if !s.sendV3RealtimeOutboxEvent(conn, record) {
			return lastSeq, false
		}
		lastSeq = record.Event.Seq
	}
	_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayDone, SessionID: sessionID, SubscriptionID: subscriptionID, LastSeq: lastSeq, NextSeq: lastSeq + 1, HighWatermarkSeq: projection.ProjectionHighWatermarkSeq})
	return lastSeq, true
}

func (s *Server) v3RealtimePrincipalCanSee(principal identity.Principal, record sessionruntime.RealtimeOutboxRecord) bool {
	if !principal.Valid() || record.Event.Seq == 0 {
		return false
	}
	if record.UserID != "" && principal.UserID != record.UserID {
		return false
	}
	if record.AccountScopeID != "" && principal.AccountScopeID != record.AccountScopeID {
		return false
	}
	return true
}

func (s *Server) sendV3RealtimeOutboxEvent(conn *transportws.Conn, record sessionruntime.RealtimeOutboxRecord) bool {
	message := V3RealtimeMessage{
		Protocol:         V3RealtimeProtocol,
		ProtocolVersion:  V3RealtimeProtocolVersion,
		Kind:             V3RealtimeKindEvent,
		SessionID:        record.SessionID,
		LastSeq:          record.Event.Seq,
		HighWatermarkSeq: record.Projection.ProjectionHighWatermarkSeq,
		EndpointCursor:   record.EndpointCursor,
		EventType:        record.Event.EventType,
		Event:            &record.Event,
		Projection:       &record.Projection,
	}
	return s.sendV3RealtimeMessage(conn, message) == nil
}

func (s *Server) sendV3RealtimeMessage(conn *transportws.Conn, message V3RealtimeMessage) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return conn.WriteText(raw)
}

func NewV3RealtimeCursorError(sessionID, code, message string, lastSeq, nextSeq uint64) V3RealtimeMessage {
	out := V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindCursorError, SessionID: sessionID, LastSeq: lastSeq, NextSeq: nextSeq, HighWatermarkSeq: nextSeq, ErrorCode: code, Error: message}
	return out
}

func parseV3RealtimeEndpointCursor(raw string) uint64 {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "cursor-")
	if raw == "" {
		return 0
	}
	seq, _ := strconv.ParseUint(raw, 10, 64)
	return seq
}

func pebbleV3RealtimeOutboxCursor(seq uint64) string {
	if seq == 0 {
		return ""
	}
	return fmt.Sprintf("cursor-%d", seq)
}
