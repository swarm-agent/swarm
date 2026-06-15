package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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

	scope := v3SyncCursorScopeForRealtime(principal, r.URL.Query().Get("surface"))
	endpointCursor, _, cursorErr := s.parseV3SyncEndpointCursor(r.URL.Query().Get("endpoint_cursor"), scope)
	if cursorErr != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", v3RealtimeCursorErrorCode(cursorErr), cursorErr.Error(), 0, 0))
		return
	}
	if endpointCursor > 0 {
		if ok := s.v3RealtimeValidateEndpointCursor(conn, endpointCursor); !ok {
			return
		}
	}
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
			last, ok := s.v3RealtimePrimeSubscriptionAtEndpointCursor(conn, principal, subID, sessionID, endpointCursor)
			if ok {
				subs[sessionID] = v3RealtimeSubscription{SessionID: sessionID, SubscriptionID: subID, LastSeq: last}
			}
		}
	}
	helloCursor, err := s.signV3SyncEndpointCursor(scope, endpointCursor)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "cursor_sign_failed", err.Error(), 0, endpointCursor))
		return
	}
	if err := s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindHello, EndpointCursor: helloCursor}); err != nil {
		return
	}

	if len(subs) > 0 {
		if !s.v3RealtimeSendReplayStartForSubscriptions(conn, subs, endpointCursor, scope) {
			return
		}
		advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, lastEndpointSeq, endpointCursor, subs)
		if !ok {
			return
		}
		subs = advanced.Subscriptions
		lastEndpointSeq = advanced.EndpointSeq
		lastKeepaliveSeq = advanced.EndpointSeq
		if !s.v3RealtimeSendReplayDoneForSubscriptions(conn, subs, lastEndpointSeq, scope) {
			return
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
				endpointSeq := lastEndpointSeq
				if strings.TrimSpace(message.EndpointCursor) != "" {
					parsed, _, err := s.parseV3SyncEndpointCursor(message.EndpointCursor, scope)
					if err != nil {
						_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(message.SessionID, v3RealtimeCursorErrorCode(err), err.Error(), lastEndpointSeq, 0))
						return
					}
					if parsed > 0 {
						if ok := s.v3RealtimeValidateEndpointCursor(conn, parsed); !ok {
							return
						}
					}
					endpointSeq = parsed
				}
				last, ok := s.v3RealtimePrimeSubscriptionAtEndpointCursor(conn, principal, message.SubscriptionID, message.SessionID, endpointSeq)
				if ok {
					newSub := v3RealtimeSubscription{SessionID: message.SessionID, SubscriptionID: message.SubscriptionID, LastSeq: last}
					newSubOnly := map[string]v3RealtimeSubscription{message.SessionID: newSub}
					if !s.v3RealtimeSendReplayStartForSubscriptions(conn, newSubOnly, endpointSeq, scope) {
						return
					}
					combined := cloneV3RealtimeSubscriptions(subs)
					combined[message.SessionID] = newSub
					catchUpFrom := lastEndpointSeq
					if endpointSeq < catchUpFrom {
						catchUpFrom = endpointSeq
					}
					advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, catchUpFrom, endpointSeq, combined)
					if !ok {
						return
					}
					if !s.v3RealtimeSendReplayDoneForSubscriptions(conn, map[string]v3RealtimeSubscription{message.SessionID: advanced.Subscriptions[message.SessionID]}, advanced.EndpointSeq, scope) {
						return
					}
					subs = advanced.Subscriptions
					if advanced.EndpointSeq > lastEndpointSeq {
						lastEndpointSeq = advanced.EndpointSeq
					}
				}
			case V3RealtimeKindUnsubscribe:
				for sessionID, sub := range subs {
					if sub.SubscriptionID == message.SubscriptionID || sessionID == message.SessionID {
						delete(subs, sessionID)
					}
				}
			case V3RealtimeKindResume:
				resumeSeq, _, err := s.parseV3SyncEndpointCursor(message.EndpointCursor, scope)
				if err != nil {
					_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", v3RealtimeCursorErrorCode(err), err.Error(), lastEndpointSeq, 0))
					return
				}
				if resumeSeq > 0 {
					if ok := s.v3RealtimeValidateEndpointCursor(conn, resumeSeq); !ok {
						return
					}
				}
				combined := cloneV3RealtimeSubscriptions(subs)
				catchUpFrom := lastEndpointSeq
				for _, requestedSub := range message.Subscriptions {
					subCursor := resumeSeq
					if strings.TrimSpace(requestedSub.EndpointCursor) != "" {
						parsed, _, err := s.parseV3SyncEndpointCursor(requestedSub.EndpointCursor, scope)
						if err != nil {
							_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(requestedSub.SessionID, v3RealtimeCursorErrorCode(err), err.Error(), lastEndpointSeq, 0))
							return
						}
						if parsed > 0 {
							if ok := s.v3RealtimeValidateEndpointCursor(conn, parsed); !ok {
								return
							}
						}
						subCursor = parsed
					}
					last, ok := s.v3RealtimePrimeSubscriptionAtEndpointCursor(conn, principal, requestedSub.SubscriptionID, requestedSub.SessionID, subCursor)
					if !ok {
						return
					}
					newSub := v3RealtimeSubscription{SessionID: requestedSub.SessionID, SubscriptionID: requestedSub.SubscriptionID, LastSeq: last}
					if !s.v3RealtimeSendReplayStartForSubscriptions(conn, map[string]v3RealtimeSubscription{requestedSub.SessionID: newSub}, subCursor, scope) {
						return
					}
					combined[requestedSub.SessionID] = newSub
					if subCursor < catchUpFrom {
						catchUpFrom = subCursor
					}
				}
				advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, catchUpFrom, resumeSeq, combined)
				if !ok {
					return
				}
				if len(message.Subscriptions) > 0 {
					doneSubs := make(map[string]v3RealtimeSubscription, len(message.Subscriptions))
					for _, requestedSub := range message.Subscriptions {
						if sub, ok := advanced.Subscriptions[requestedSub.SessionID]; ok {
							doneSubs[requestedSub.SessionID] = sub
						}
					}
					if !s.v3RealtimeSendReplayDoneForSubscriptions(conn, doneSubs, advanced.EndpointSeq, scope) {
						return
					}
				}
				subs = advanced.Subscriptions
				lastEndpointSeq = advanced.EndpointSeq
				lastKeepaliveSeq = advanced.EndpointSeq
			}
		case <-sub.send:
			advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, lastEndpointSeq, lastEndpointSeq, subs)
			if !ok {
				return
			}
			subs = advanced.Subscriptions
			lastEndpointSeq = advanced.EndpointSeq
		case slow := <-sub.slow:
			_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSlowConsumer, NextSeq: slow.EndpointSeq, ErrorCode: "slow_consumer", Reason: slow.Reason})
			return
		case <-ticker.C:
			keepaliveCursor, err := s.signV3SyncEndpointCursor(scope, lastEndpointSeq)
			if err != nil {
				_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "cursor_sign_failed", err.Error(), lastEndpointSeq, lastEndpointSeq))
				return
			}
			if err := s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindKeepalive, LastSeq: lastKeepaliveSeq, EndpointCursor: keepaliveCursor}); err != nil {
				return
			}
			lastKeepaliveSeq = lastEndpointSeq
		}
	}
}

type v3RealtimeAdvanceResult struct {
	EndpointSeq   uint64
	Subscriptions map[string]v3RealtimeSubscription
}

func (s *Server) v3RealtimeValidateEndpointCursor(conn *transportws.Conn, requestedEndpointSeq uint64) bool {
	if requestedEndpointSeq == 0 {
		return true
	}
	currentHead, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_resume_failed", err.Error(), 0, requestedEndpointSeq))
		return false
	}
	if requestedEndpointSeq > currentHead {
		msg := NewV3RealtimeCursorError("", "endpoint_cursor_ahead", "endpoint cursor is ahead of committed realtime outbox; reconnect required", currentHead, requestedEndpointSeq)
		msg.LatestEndpointSeq = currentHead
		_ = s.sendV3RealtimeMessage(conn, msg)
		return false
	}
	oldestAvailableEndpointSeq, err := s.v3RealtimeOldestAvailableEndpointSeq()
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_resume_failed", err.Error(), 0, requestedEndpointSeq))
		return false
	}
	if oldestAvailableEndpointSeq > 0 && requestedEndpointSeq < oldestAvailableEndpointSeq {
		msg := NewV3RealtimeCursorError("", "endpoint_cursor_too_old", fmt.Sprintf("endpoint cursor %d is no longer available; bootstrap required", requestedEndpointSeq), 0, requestedEndpointSeq)
		msg.BootstrapRequired = true
		msg.OldestAvailableEndpointSeq = oldestAvailableEndpointSeq
		msg.LatestEndpointSeq = currentHead
		_ = s.sendV3RealtimeMessage(conn, msg)
		return false
	}
	return true
}

func (s *Server) v3RealtimeOldestAvailableEndpointSeq() (uint64, error) {
	if s != nil && s.v3RealtimeRetentionBoundary != nil {
		return s.v3RealtimeRetentionBoundary()
	}
	// Durable realtime outbox compaction is not active yet. A zero boundary means
	// every committed endpoint cursor remains replayable; retention/compaction will
	// wire this to the store-maintained oldest retained endpoint sequence.
	return 0, nil
}

func (s *Server) v3RealtimeCatchUpEndpointCursor(conn *transportws.Conn, principal identity.Principal, scope v3SyncCursorScope, currentEndpointSeq, requestedEndpointSeq uint64, subs map[string]v3RealtimeSubscription) (v3RealtimeAdvanceResult, bool) {
	current := currentEndpointSeq
	advanced := v3RealtimeAdvanceResult{EndpointSeq: current, Subscriptions: cloneV3RealtimeSubscriptions(subs)}
	for {
		records, err := s.sessions.ListRealtimeOutboxAfter(current, v3RealtimeReplayLimit)
		if err != nil {
			_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_resume_failed", err.Error(), current, requestedEndpointSeq))
			return advanced, false
		}
		if len(records) == 0 {
			if requestedEndpointSeq > current {
				msg := NewV3RealtimeCursorError("", "endpoint_cursor_ahead", "endpoint cursor is ahead of committed realtime outbox; reconnect required", current, requestedEndpointSeq)
				msg.LatestEndpointSeq = current
				_ = s.sendV3RealtimeMessage(conn, msg)
				return advanced, false
			}
			return advanced, true
		}
		for _, record := range records {
			next, ok := s.v3RealtimeProcessOutboxRecord(conn, principal, scope, record, current, advanced.Subscriptions)
			if !ok {
				return advanced, false
			}
			advanced = next
			current = next.EndpointSeq
		}
		if len(records) < v3RealtimeReplayLimit {
			return advanced, true
		}
	}
}

func (s *Server) v3RealtimeProcessOutboxRecord(conn *transportws.Conn, principal identity.Principal, scope v3SyncCursorScope, record sessionruntime.RealtimeOutboxRecord, lastEndpointSeq uint64, subs map[string]v3RealtimeSubscription) (v3RealtimeAdvanceResult, bool) {
	advanced := v3RealtimeAdvanceResult{EndpointSeq: lastEndpointSeq, Subscriptions: cloneV3RealtimeSubscriptions(subs)}
	if record.EndpointSeq <= lastEndpointSeq {
		return advanced, true
	}
	if record.EndpointSeq != lastEndpointSeq+1 && lastEndpointSeq != 0 {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_cursor_gap", fmt.Sprintf("endpoint sequence gap at %d, want %d; reconnect required", record.EndpointSeq, lastEndpointSeq+1), lastEndpointSeq, record.EndpointSeq))
		return advanced, false
	}
	advanced.EndpointSeq = record.EndpointSeq
	if !s.v3RealtimePrincipalCanSee(principal, record) {
		return advanced, true
	}
	subscription, ok := advanced.Subscriptions[record.SessionID]
	if !ok {
		return advanced, true
	}
	if record.Event.Seq <= subscription.LastSeq {
		return advanced, true
	}
	if record.Event.Seq != subscription.LastSeq+1 {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(record.SessionID, "session_cursor_gap", fmt.Sprintf("session event sequence gap at %d, want %d; refetch required", record.Event.Seq, subscription.LastSeq+1), subscription.LastSeq, record.Event.Seq))
		delete(advanced.Subscriptions, record.SessionID)
		return advanced, true
	}
	if !s.sendV3RealtimeOutboxEvent(conn, record, scope) {
		return advanced, false
	}
	subscription.LastSeq = record.Event.Seq
	advanced.Subscriptions[record.SessionID] = subscription
	return advanced, true
}

func cloneV3RealtimeSubscriptions(in map[string]v3RealtimeSubscription) map[string]v3RealtimeSubscription {
	out := make(map[string]v3RealtimeSubscription, len(in))
	for sessionID, sub := range in {
		out[sessionID] = sub
	}
	return out
}

func orderedV3RealtimeSubscriptions(subs map[string]v3RealtimeSubscription) []v3RealtimeSubscription {
	out := make([]v3RealtimeSubscription, 0, len(subs))
	for _, sub := range subs {
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SessionID == out[j].SessionID {
			return out[i].SubscriptionID < out[j].SubscriptionID
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

func (s *Server) v3RealtimeSendReplayStartForSubscriptions(conn *transportws.Conn, subs map[string]v3RealtimeSubscription, endpointSeq uint64, scope v3SyncCursorScope) bool {
	cursor, err := s.signV3SyncEndpointCursor(scope, endpointSeq)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "cursor_sign_failed", err.Error(), 0, endpointSeq))
		return false
	}
	for _, sub := range orderedV3RealtimeSubscriptions(subs) {
		if err := s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayStart, SessionID: sub.SessionID, SubscriptionID: sub.SubscriptionID, EndpointCursor: cursor}); err != nil {
			return false
		}
	}
	return true
}

func (s *Server) v3RealtimeSendReplayDoneForSubscriptions(conn *transportws.Conn, subs map[string]v3RealtimeSubscription, endpointSeq uint64, scope v3SyncCursorScope) bool {
	cursor, err := s.signV3SyncEndpointCursor(scope, endpointSeq)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "cursor_sign_failed", err.Error(), 0, endpointSeq))
		return false
	}
	for _, sub := range orderedV3RealtimeSubscriptions(subs) {
		if err := s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindReplayDone, SessionID: sub.SessionID, SubscriptionID: sub.SubscriptionID, LastSeq: sub.LastSeq, NextSeq: sub.LastSeq + 1, EndpointCursor: cursor}); err != nil {
			return false
		}
	}
	return true
}

func (s *Server) v3RealtimePrimeSubscriptionAtEndpointCursor(conn *transportws.Conn, principal identity.Principal, subscriptionID, sessionID string, endpointSeq uint64) (uint64, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "missing_session_id", "subscribe.session requires session_id", 0, 0))
		return 0, false
	}
	if _, found, err := s.hydrateSessionsV3Primary(principal, sessionID); err != nil {
		_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, SessionID: sessionID, ErrorCode: "auth_scope_mismatch", Error: err.Error()})
		return 0, false
	} else if !found {
		_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, SessionID: sessionID, ErrorCode: "auth_scope_mismatch", Error: "session not found"})
		return 0, false
	}
	lastSeq := uint64(0)
	if record, ok, err := s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(sessionID, endpointSeq); err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(sessionID, "replay_failed", err.Error(), 0, endpointSeq))
		return 0, false
	} else if ok {
		lastSeq = record.Event.Seq
	}
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

func (s *Server) sendV3RealtimeOutboxEvent(conn *transportws.Conn, record sessionruntime.RealtimeOutboxRecord, scope v3SyncCursorScope) bool {
	cursor, err := s.signV3SyncEndpointCursor(scope, record.EndpointSeq)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(record.SessionID, "cursor_sign_failed", err.Error(), record.EndpointSeq-1, record.EndpointSeq))
		return false
	}
	message := V3RealtimeMessage{
		Protocol:         V3RealtimeProtocol,
		ProtocolVersion:  V3RealtimeProtocolVersion,
		Kind:             V3RealtimeKindEvent,
		SessionID:        record.SessionID,
		LastSeq:          record.Event.Seq,
		HighWatermarkSeq: record.Projection.ProjectionHighWatermarkSeq,
		EndpointCursor:   cursor,
		Rev:              record.EndpointSeq,
		PrevRev:          record.EndpointSeq - 1,
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

func v3RealtimeCursorErrorCode(err error) string {
	if err == nil {
		return "endpoint_cursor_malformed"
	}
	var cursorErr *v3SyncCursorError
	if errors.As(err, &cursorErr) && strings.TrimSpace(cursorErr.Code) != "" {
		return cursorErr.Code
	}
	return "endpoint_cursor_malformed"
}
