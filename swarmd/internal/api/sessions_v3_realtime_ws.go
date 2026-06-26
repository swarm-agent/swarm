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
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	transportws "swarm/packages/swarmd/internal/transport/ws"
)

const v3RealtimeReplayLimit = 500

var v3RealtimeKeepaliveInterval = 15 * time.Second
var v3RealtimeWriteTimeout = 5 * time.Second

var v3RealtimeWriteObserver func(activeDelta int)

var v3RealtimeListRealtimeOutboxAfter = func(s *Server, afterEndpointSeq uint64, limit int) ([]sessionruntime.RealtimeOutboxRecord, error) {
	return s.sessions.ListRealtimeOutboxAfter(afterEndpointSeq, limit)
}

type v3RealtimeSubscription struct {
	SessionID      string
	SubscriptionID string
	LastSeq        uint64
	AutoSubscribed bool
	WorksetIDs     map[string]struct{}
}

type v3RealtimeWorksetSubscription struct {
	WorksetID             string
	SubscriptionID        string
	Surface               string
	Selector              V3RealtimeWorksetSelector
	Resources             []string
	AutoSubscribeSessions bool
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
	if s.v3LiveHub == nil {
		s.v3LiveHub = newV3LiveHub()
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

	liveSub := s.v3LiveHub.subscribe()
	if liveSub == nil {
		_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSlowConsumer, ErrorCode: "subscribe_failed", Error: "unable to subscribe to v3 realtime live hub"})
		return
	}
	defer s.v3LiveHub.unsubscribe(liveSub)

	surface := r.URL.Query().Get("surface")
	scope := v3SyncCursorScopeForRealtime(principal, surface)
	rawEndpointCursor := strings.TrimSpace(r.URL.Query().Get("endpoint_cursor"))
	endpointCursor, _, cursorErr := s.parseV3RealtimeEndpointCursor(rawEndpointCursor, principal, surface)
	if cursorErr != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", v3RealtimeCursorErrorCode(cursorErr), cursorErr.Error(), 0, 0))
		return
	}
	if rawEndpointCursor == "" {
		currentHead, err := s.sessions.CurrentRealtimeOutboxRevision()
		if err != nil {
			_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_resume_failed", err.Error(), 0, 0))
			return
		}
		endpointCursor = currentHead
	} else if ok := s.v3RealtimeValidateEndpointCursor(conn, endpointCursor); !ok {
		return
	}
	subs := map[string]v3RealtimeSubscription{}
	worksets := map[string]v3RealtimeWorksetSubscription{}
	livePatchAccepted := false
	lastEndpointSeq := endpointCursor
	lastKeepaliveSeq := endpointCursor

	if strings.TrimSpace(r.URL.Query().Get("sessions")) != "" || strings.TrimSpace(r.URL.Query().Get("session")) != "" || strings.TrimSpace(r.URL.Query().Get("session_id")) != "" {
		_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, ErrorCode: "query_session_subscriptions_unsupported", Error: "desktop V3 realtime does not accept query-string session subscriptions; send subscribe.session or resume frames"})
		return
	}
	helloCursor, err := s.signV3SyncEndpointCursor(scope, endpointCursor)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "cursor_sign_failed", err.Error(), 0, endpointCursor))
		return
	}
	hello := V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindHello, EndpointCursor: helloCursor}
	if s.v3LivePatchEnabled {
		hello.Capabilities = []string{V3RealtimeCapabilityLivePatchV1}
	}
	if err := s.sendV3RealtimeMessage(conn, hello); err != nil {
		return
	}

	if len(subs) > 0 {
		if !s.v3RealtimeSendReplayStartForSubscriptions(conn, subs, endpointCursor, scope) {
			return
		}
		advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, lastEndpointSeq, endpointCursor, subs, worksets, false)
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
			if err := ValidateV3RealtimeInboundClientMessage(message); err != nil {
				readMessages <- V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, ErrorCode: "invalid_message", Error: err.Error()}
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
			if message.Kind == V3RealtimeKindAuthDenied {
				_ = s.sendV3RealtimeMessage(conn, message)
				return
			}
			switch message.Kind {
			case V3RealtimeKindSubscribe:
				endpointSeq := lastEndpointSeq
				if strings.TrimSpace(message.EndpointCursor) != "" {
					parsed, _, err := s.parseV3RealtimeEndpointCursor(message.EndpointCursor, principal, surface)
					if err != nil {
						_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(message.SessionID, v3RealtimeCursorErrorCode(err), err.Error(), lastEndpointSeq, 0))
						return
					}
					if ok := s.v3RealtimeValidateEndpointCursor(conn, parsed); !ok {
						return
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
					advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, catchUpFrom, endpointSeq, combined, worksets, false)
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
				s.syncV3LiveSubscriptionSessions(liveSub, principal, subs, livePatchAccepted)
			case V3RealtimeKindResume:
				livePatchAccepted = s.v3LivePatchEnabled && containsV3RealtimeCapability(message.Capabilities, V3RealtimeCapabilityLivePatchV1)
				s.syncV3LiveSubscriptionSessions(liveSub, principal, nil, false)
				resumeSeq, _, err := s.parseV3RealtimeEndpointCursor(message.EndpointCursor, principal, surface)
				if err != nil {
					_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", v3RealtimeCursorErrorCode(err), err.Error(), lastEndpointSeq, 0))
					return
				}
				if ok := s.v3RealtimeValidateEndpointCursor(conn, resumeSeq); !ok {
					return
				}
				requestedWorksets, ok := s.v3RealtimeValidateResumeWorksets(conn, principal, message.Worksets)
				if !ok {
					return
				}
				requestedSubs := map[string]v3RealtimeSubscription{}
				requestedSubCursors := map[string]uint64{}
				catchUpFrom := resumeSeq
				for _, requestedSub := range message.Subscriptions {
					subCursor := resumeSeq
					if strings.TrimSpace(requestedSub.EndpointCursor) != "" {
						parsed, _, err := s.parseV3RealtimeEndpointCursor(requestedSub.EndpointCursor, principal, surface)
						if err != nil {
							_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(requestedSub.SessionID, v3RealtimeCursorErrorCode(err), err.Error(), lastEndpointSeq, 0))
							return
						}
						if ok := s.v3RealtimeValidateEndpointCursor(conn, parsed); !ok {
							return
						}
						subCursor = parsed
					}
					last, ok := s.v3RealtimePrimeSubscriptionAtEndpointCursor(conn, principal, requestedSub.SubscriptionID, requestedSub.SessionID, subCursor)
					if !ok {
						return
					}
					newSub := v3RealtimeSubscription{SessionID: requestedSub.SessionID, SubscriptionID: requestedSub.SubscriptionID, LastSeq: last}
					newSub.WorksetIDs, newSub.AutoSubscribed = s.v3RealtimeMatchedWorksetIDsForSession(principal, requestedSub.SessionID, requestedWorksets)
					requestedSubs[requestedSub.SessionID] = newSub
					requestedSubCursors[requestedSub.SessionID] = subCursor
					if subCursor < catchUpFrom {
						catchUpFrom = subCursor
					}
				}
				for sessionID, requestedSub := range requestedSubs {
					if !s.v3RealtimeSendReplayStartForSubscriptions(conn, map[string]v3RealtimeSubscription{sessionID: requestedSub}, requestedSubCursors[sessionID], scope) {
						return
					}
				}
				advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, catchUpFrom, resumeSeq, requestedSubs, requestedWorksets, len(requestedSubs) == 0)
				if !ok {
					return
				}
				if len(requestedSubs) > 0 {
					doneSubs := make(map[string]v3RealtimeSubscription, len(requestedSubs))
					for sessionID := range requestedSubs {
						if sub, ok := advanced.Subscriptions[sessionID]; ok {
							doneSubs[sessionID] = sub
						}
					}
					if !s.v3RealtimeSendReplayDoneForSubscriptions(conn, doneSubs, advanced.EndpointSeq, scope) {
						return
					}
				}
				subs = advanced.Subscriptions
				worksets = requestedWorksets
				s.syncV3LiveSubscriptionSessions(liveSub, principal, subs, livePatchAccepted)
				lastEndpointSeq = advanced.EndpointSeq
				lastKeepaliveSeq = advanced.EndpointSeq
			}
		case <-sub.send:
			advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, lastEndpointSeq, lastEndpointSeq, subs, worksets, true)
			if !ok {
				return
			}
			subs = advanced.Subscriptions
			s.syncV3LiveSubscriptionSessions(liveSub, principal, subs, livePatchAccepted)
			lastEndpointSeq = advanced.EndpointSeq
		case <-liveSub.notify:
			patches := liveSub.drain(v3LiveWriterMaxFramesPerTurn, v3LiveWriterMaxBytesPerTurn)
			for _, patch := range patches {
				if err := s.sendV3RealtimeLivePatch(conn, patch); err != nil {
					return
				}
			}
		case slow := <-liveSub.slow:
			_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSlowConsumer, ErrorCode: "slow_consumer", Reason: slow.Reason})
			return
		case slow := <-sub.slow:
			_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindSlowConsumer, NextSeq: slow.EndpointSeq, ErrorCode: "slow_consumer", Reason: slow.Reason})
			return
		case <-ticker.C:
			advanced, ok := s.v3RealtimeCatchUpEndpointCursor(conn, principal, scope, lastEndpointSeq, lastEndpointSeq, subs, worksets, true)
			if !ok {
				return
			}
			subs = advanced.Subscriptions
			s.syncV3LiveSubscriptionSessions(liveSub, principal, subs, livePatchAccepted)
			lastEndpointSeq = advanced.EndpointSeq
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
	EndpointSeq         uint64
	LastSentEndpointSeq uint64
	Subscriptions       map[string]v3RealtimeSubscription
	DeliveredEvents     int
}

func (s *Server) syncV3LiveSubscriptionSessions(liveSub *v3LiveSubscriber, principal identity.Principal, subs map[string]v3RealtimeSubscription, livePatchAccepted bool) {
	if s == nil || s.v3LiveHub == nil || liveSub == nil {
		return
	}
	if !livePatchAccepted || !principal.Valid() {
		s.v3LiveHub.replaceSessions(liveSub, "", nil)
		return
	}
	sessionIDs := make([]string, 0, len(subs))
	for sessionID := range subs {
		sessionIDs = append(sessionIDs, sessionID)
	}
	sort.Strings(sessionIDs)
	s.v3LiveHub.replaceSessions(liveSub, principal.AccountScopeID, sessionIDs)
}

func (s *Server) v3RealtimeValidateResumeWorksets(conn *transportws.Conn, principal identity.Principal, requested []V3RealtimeWorksetSubscriptionRequest) (map[string]v3RealtimeWorksetSubscription, bool) {
	out := make(map[string]v3RealtimeWorksetSubscription, len(requested))
	for _, workset := range requested {
		worksetID := strings.TrimSpace(workset.WorksetID)
		subscriptionID := strings.TrimSpace(workset.SubscriptionID)
		if worksetID == "" || subscriptionID == "" {
			_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, ErrorCode: "invalid_workset_subscription", Error: "resume workset requires workset_id and subscription_id"})
			return nil, false
		}
		selector, err := canonicalV3RealtimeWorksetSelector(workset.Selector)
		if err != nil {
			_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, ErrorCode: "invalid_workset_selector", Error: err.Error()})
			return nil, false
		}
		resources, err := canonicalV3RealtimeWorksetResources(workset.Resources)
		if err != nil {
			_ = s.sendV3RealtimeMessage(conn, V3RealtimeMessage{Protocol: V3RealtimeProtocol, ProtocolVersion: V3RealtimeProtocolVersion, Kind: V3RealtimeKindAuthDenied, ErrorCode: "invalid_workset_resources", Error: err.Error()})
			return nil, false
		}
		out[worksetID] = v3RealtimeWorksetSubscription{
			WorksetID:             worksetID,
			SubscriptionID:        subscriptionID,
			Surface:               normalizeV3SyncSurface(workset.Surface),
			Selector:              selector,
			Resources:             resources,
			AutoSubscribeSessions: workset.AutoSubscribeSessions,
		}
	}
	return out, true
}

func (s *Server) v3RealtimeValidateEndpointCursor(conn *transportws.Conn, requestedEndpointSeq uint64) bool {
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
	if oldestAvailableEndpointSeq > 0 && requestedEndpointSeq+1 < oldestAvailableEndpointSeq {
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

func (s *Server) v3RealtimeCatchUpEndpointCursor(conn *transportws.Conn, principal identity.Principal, scope v3SyncCursorScope, currentEndpointSeq, requestedEndpointSeq uint64, subs map[string]v3RealtimeSubscription, worksets map[string]v3RealtimeWorksetSubscription, emitZeroEventWatermark bool) (v3RealtimeAdvanceResult, bool) {
	initialEndpointSeq := currentEndpointSeq
	current := currentEndpointSeq
	advanced := v3RealtimeAdvanceResult{EndpointSeq: current, LastSentEndpointSeq: current, Subscriptions: cloneV3RealtimeSubscriptions(subs)}
	currentHead, err := s.sessions.CurrentRealtimeOutboxRevision()
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_resume_failed", err.Error(), current, requestedEndpointSeq))
		return advanced, false
	}
	oldestAvailableEndpointSeq, err := s.v3RealtimeOldestAvailableEndpointSeq()
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_resume_failed", err.Error(), current, requestedEndpointSeq))
		return advanced, false
	}
	if requestedEndpointSeq > currentHead {
		msg := NewV3RealtimeCursorError("", "endpoint_cursor_ahead", "endpoint cursor is ahead of committed realtime outbox; reconnect required", currentHead, requestedEndpointSeq)
		msg.LatestEndpointSeq = currentHead
		_ = s.sendV3RealtimeMessage(conn, msg)
		return advanced, false
	}
	for {
		records, err := v3RealtimeListRealtimeOutboxAfter(s, current, v3RealtimeReplayLimit)
		if err != nil {
			_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "endpoint_resume_failed", err.Error(), current, requestedEndpointSeq))
			return advanced, false
		}
		if len(records) == 0 {
			if current < currentHead {
				_ = s.sendV3RealtimeEndpointCursorGap(conn, current+1, oldestAvailableEndpointSeq, currentHead)
				return advanced, false
			}
			if emitZeroEventWatermark && !s.v3RealtimeSendEndpointWatermarkIfAdvanced(conn, scope, initialEndpointSeq, advanced) {
				return advanced, false
			}
			return advanced, true
		}
		for _, record := range records {
			next, ok, delivered := s.v3RealtimeProcessOutboxRecord(conn, principal, scope, record, current, advanced.Subscriptions, worksets, oldestAvailableEndpointSeq, currentHead)
			if !ok {
				return advanced, false
			}
			next.DeliveredEvents = advanced.DeliveredEvents
			if delivered {
				next.DeliveredEvents++
			} else {
				next.LastSentEndpointSeq = advanced.LastSentEndpointSeq
			}
			advanced = next
			current = next.EndpointSeq
		}
		if len(records) < v3RealtimeReplayLimit {
			if advanced.EndpointSeq < currentHead {
				_ = s.sendV3RealtimeEndpointCursorGap(conn, advanced.EndpointSeq+1, oldestAvailableEndpointSeq, currentHead)
				return advanced, false
			}
			if emitZeroEventWatermark && !s.v3RealtimeSendEndpointWatermarkIfAdvanced(conn, scope, initialEndpointSeq, advanced) {
				return advanced, false
			}
			return advanced, true
		}
	}
}

func (s *Server) sendV3RealtimeEndpointCursorGap(conn *transportws.Conn, missingEndpointSeq, oldestAvailableEndpointSeq, latestEndpointSeq uint64) error {
	msg := NewV3RealtimeCursorError("", "endpoint_cursor_gap", fmt.Sprintf("endpoint cursor cannot be replayed continuously from durable realtime outbox at %d; bootstrap required", missingEndpointSeq), 0, missingEndpointSeq)
	msg.BootstrapRequired = true
	msg.OldestAvailableEndpointSeq = oldestAvailableEndpointSeq
	msg.LatestEndpointSeq = latestEndpointSeq
	msg.MissingEndpointSeq = missingEndpointSeq
	return s.sendV3RealtimeMessage(conn, msg)
}

func (s *Server) v3RealtimeSendEndpointWatermarkIfAdvanced(conn *transportws.Conn, scope v3SyncCursorScope, initialEndpointSeq uint64, advanced v3RealtimeAdvanceResult) bool {
	if advanced.EndpointSeq <= advanced.LastSentEndpointSeq {
		return true
	}
	cursor, err := s.signV3SyncEndpointCursor(scope, advanced.EndpointSeq)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError("", "cursor_sign_failed", err.Error(), initialEndpointSeq, advanced.EndpointSeq))
		return false
	}
	return s.sendV3RealtimeMessage(conn, V3RealtimeMessage{
		Protocol:         V3RealtimeProtocol,
		ProtocolVersion:  V3RealtimeProtocolVersion,
		Kind:             V3RealtimeKindEndpointWatermark,
		EndpointCursor:   cursor,
		HighWatermarkSeq: advanced.EndpointSeq,
		Rev:              advanced.EndpointSeq,
		PrevRev:          advanced.LastSentEndpointSeq,
	}) == nil
}

func (s *Server) v3RealtimeProcessOutboxRecord(conn *transportws.Conn, principal identity.Principal, scope v3SyncCursorScope, record sessionruntime.RealtimeOutboxRecord, lastEndpointSeq uint64, subs map[string]v3RealtimeSubscription, worksets map[string]v3RealtimeWorksetSubscription, oldestAvailableEndpointSeq, latestEndpointSeq uint64) (v3RealtimeAdvanceResult, bool, bool) {
	advanced := v3RealtimeAdvanceResult{EndpointSeq: lastEndpointSeq, LastSentEndpointSeq: lastEndpointSeq, Subscriptions: cloneV3RealtimeSubscriptions(subs)}
	if record.EndpointSeq <= lastEndpointSeq {
		return advanced, true, false
	}
	if record.EndpointSeq != lastEndpointSeq+1 && lastEndpointSeq != 0 {
		_ = s.sendV3RealtimeEndpointCursorGap(conn, lastEndpointSeq+1, oldestAvailableEndpointSeq, latestEndpointSeq)
		return advanced, false, false
	}
	advanced.EndpointSeq = record.EndpointSeq
	if !s.v3RealtimePrincipalCanSee(principal, record) {
		return advanced, true, false
	}

	subscription, subscribed := advanced.Subscriptions[record.SessionID]
	removeAutoSubscriptionAfterDelivery := false
	if !subscribed {
		match, ok := s.v3RealtimeMatchRecordWorkset(principal, record, worksets)
		if !ok {
			return advanced, true, false
		}
		if !match.AutoSubscribeSessions {
			if !s.sendV3RealtimeWorksetSessionFrame(conn, V3RealtimeKindWorksetSessionUpdated, match, v3RealtimeSubscription{}, record, scope) {
				return advanced, false, false
			}
			advanced.LastSentEndpointSeq = record.EndpointSeq
			return advanced, true, true
		}
		primeAt := uint64(0)
		if record.EndpointSeq > 0 {
			primeAt = record.EndpointSeq - 1
		}
		last, ok := s.v3RealtimePrimeSubscriptionAtEndpointCursor(conn, principal, v3RealtimeAutoSubscriptionID(match, record.SessionID), record.SessionID, primeAt)
		if !ok {
			return advanced, false, false
		}
		subscription = v3RealtimeSubscription{SessionID: record.SessionID, SubscriptionID: v3RealtimeAutoSubscriptionID(match, record.SessionID), LastSeq: last, AutoSubscribed: true, WorksetIDs: map[string]struct{}{match.WorksetID: struct{}{}}}
		advanced.Subscriptions[record.SessionID] = subscription
		if !s.sendV3RealtimeWorksetSessionFrame(conn, V3RealtimeKindWorksetSessionDiscovered, match, subscription, record, scope) {
			return advanced, false, false
		}
		advanced.LastSentEndpointSeq = record.EndpointSeq
	} else if subscription.AutoSubscribed {
		removeFromSubscribedWorksets := v3RealtimeRecordRemovesFromWorkset(record)
		if v3RealtimeRecordChangesVisibility(record) && !removeFromSubscribedWorksets {
			matchedWorksetIDs, matched := s.v3RealtimeMatchedWorksetIDsForRecord(principal, record, worksets)
			if matched {
				subscription.WorksetIDs = matchedWorksetIDs
				advanced.Subscriptions[record.SessionID] = subscription
			} else {
				removeFromSubscribedWorksets = true
			}
		}
		if removeFromSubscribedWorksets {
			removeAutoSubscriptionAfterDelivery = true
			for worksetID := range subscription.WorksetIDs {
				workset, ok := worksets[worksetID]
				if !ok {
					continue
				}
				if !s.sendV3RealtimeWorksetSessionFrame(conn, V3RealtimeKindWorksetSessionRemoved, workset, subscription, record, scope) {
					return advanced, false, false
				}
				advanced.LastSentEndpointSeq = record.EndpointSeq
			}
		}
	}

	if record.Event.Seq <= subscription.LastSeq {
		return advanced, true, false
	}
	if record.Event.Seq != subscription.LastSeq+1 {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(record.SessionID, "session_cursor_gap", fmt.Sprintf("session event sequence gap at %d, want %d; refetch required", record.Event.Seq, subscription.LastSeq+1), subscription.LastSeq, record.Event.Seq))
		delete(advanced.Subscriptions, record.SessionID)
		return advanced, true, false
	}
	if !s.sendV3RealtimeOutboxEvent(conn, record, scope) {
		return advanced, false, false
	}
	advanced.LastSentEndpointSeq = record.EndpointSeq
	subscription.LastSeq = record.Event.Seq
	if removeAutoSubscriptionAfterDelivery {
		delete(advanced.Subscriptions, record.SessionID)
	} else {
		advanced.Subscriptions[record.SessionID] = subscription
	}
	return advanced, true, true
}

func (s *Server) v3RealtimeMatchedWorksetIDsForSession(principal identity.Principal, sessionID string, worksets map[string]v3RealtimeWorksetSubscription) (map[string]struct{}, bool) {
	if len(worksets) == 0 {
		return nil, false
	}
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return nil, false
	}
	return v3RealtimeMatchedWorksetIDsForSnapshot(principal, session, worksets)
}

func (s *Server) v3RealtimeMatchedWorksetIDsForRecord(principal identity.Principal, record sessionruntime.RealtimeOutboxRecord, worksets map[string]v3RealtimeWorksetSubscription) (map[string]struct{}, bool) {
	if len(worksets) == 0 {
		return nil, false
	}
	session, ok := s.v3RealtimeSessionSnapshotForRecord(record)
	if !ok {
		return nil, false
	}
	return v3RealtimeMatchedWorksetIDsForSnapshot(principal, session, worksets)
}

func v3RealtimeMatchedWorksetIDsForSnapshot(principal identity.Principal, session pebblestore.SessionSnapshot, worksets map[string]v3RealtimeWorksetSubscription) (map[string]struct{}, bool) {
	matched := map[string]struct{}{}
	auto := false
	for _, workset := range orderedV3RealtimeWorksets(worksets) {
		if !v3RealtimeSessionMatchesWorksetSelector(principal, session, workset.Selector) {
			continue
		}
		matched[workset.WorksetID] = struct{}{}
		if workset.AutoSubscribeSessions {
			auto = true
		}
	}
	if len(matched) == 0 {
		return nil, false
	}
	return matched, auto
}

func (s *Server) v3RealtimeMatchRecordWorkset(principal identity.Principal, record sessionruntime.RealtimeOutboxRecord, worksets map[string]v3RealtimeWorksetSubscription) (v3RealtimeWorksetSubscription, bool) {
	if len(worksets) == 0 || v3RealtimeRecordRemovesFromWorkset(record) {
		return v3RealtimeWorksetSubscription{}, false
	}
	session, ok := s.v3RealtimeSessionSnapshotForRecord(record)
	if !ok {
		return v3RealtimeWorksetSubscription{}, false
	}
	var fallback v3RealtimeWorksetSubscription
	fallbackOK := false
	for _, workset := range orderedV3RealtimeWorksets(worksets) {
		if !v3RealtimeSessionMatchesWorksetSelector(principal, session, workset.Selector) {
			continue
		}
		if !v3RealtimeWorksetIncludesRecordResource(workset, record) {
			continue
		}
		if workset.AutoSubscribeSessions {
			return workset, true
		}
		if !fallbackOK {
			fallback = workset
			fallbackOK = true
		}
	}
	return fallback, fallbackOK
}

func (s *Server) v3RealtimeSessionSnapshotForRecord(record sessionruntime.RealtimeOutboxRecord) (pebblestore.SessionSnapshot, bool) {
	return v3RealtimeSessionSnapshotFromRecord(record)
}

func v3RealtimeSessionSnapshotFromRecord(record sessionruntime.RealtimeOutboxRecord) (pebblestore.SessionSnapshot, bool) {
	if record.Membership != nil && strings.TrimSpace(record.Membership.SessionID) != "" {
		return v3RealtimeSessionSnapshotFromMembership(*record.Membership), true
	}
	var payload struct {
		Session *pebblestore.SessionSnapshot `json:"session,omitempty"`
	}
	if len(record.Event.Payload) > 0 {
		if err := json.Unmarshal(record.Event.Payload, &payload); err == nil && payload.Session != nil && strings.TrimSpace(payload.Session.ID) != "" {
			session := *payload.Session
			return session, true
		}
	}
	return pebblestore.SessionSnapshot{}, false
}

func v3RealtimeSessionSnapshotFromMembership(membership pebblestore.V3RealtimeOutboxMembership) pebblestore.SessionSnapshot {
	return pebblestore.SessionSnapshot{
		ID:                      strings.TrimSpace(membership.SessionID),
		UserID:                  strings.TrimSpace(membership.UserID),
		AccountScopeID:          strings.TrimSpace(membership.AccountScopeID),
		WorkspacePath:           strings.TrimSpace(membership.WorkspacePath),
		WorkspaceName:           strings.TrimSpace(membership.WorkspaceName),
		WorktreeRootPath:        strings.TrimSpace(membership.WorktreeRootPath),
		TemporaryWorkspaceRoots: append([]string(nil), membership.TemporaryWorkspaceRoots...),
		Metadata:                cloneSessionsV3Metadata(membership.Metadata),
	}
}

func canonicalV3RealtimeWorksetSelector(selector V3RealtimeWorksetSelector) (V3RealtimeWorksetSelector, error) {
	selector.Kind = strings.TrimSpace(selector.Kind)
	if !sessionsV3SyncSelectorKindAllowed(selector.Kind) {
		return V3RealtimeWorksetSelector{}, fmt.Errorf("unsupported v3 realtime selector.kind %q", selector.Kind)
	}
	selector.SessionIDs = canonicalV3SyncSessionIDs(selector.SessionIDs)
	selector.Recent.BeforeSessionID = strings.TrimSpace(selector.Recent.BeforeSessionID)
	workspacePaths, err := canonicalSessionsV3WorksetWorkspacePaths(sessionsV3WorksetWorkspace{WorkspacePath: selector.WorkspacePath, WorkspacePaths: selector.WorkspacePaths})
	if err != nil {
		return V3RealtimeWorksetSelector{}, err
	}
	selector.WorkspacePath = ""
	selector.WorkspacePaths = nil
	if len(workspacePaths) == 1 {
		selector.WorkspacePath = workspacePaths[0]
	} else if len(workspacePaths) > 1 {
		selector.WorkspacePaths = workspacePaths
	}
	if selector.Kind == "" {
		return V3RealtimeWorksetSelector{}, errors.New("v3 realtime resume workset requires selector.kind")
	}
	switch selector.Kind {
	case "global":
		selector.Global = true
		selector.Recent = sessionsV3WorksetRecent{}
		if len(selector.SessionIDs) > 0 || len(workspacePaths) > 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime global selector cannot be combined with session_ids, workspace_path, or workspace_paths")
		}
	case "session_ids":
		if selector.Global || len(workspacePaths) > 0 || selector.Recent.Limit > 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime session_ids selector cannot be combined with global, workspace, or recent filters")
		}
		if len(selector.SessionIDs) == 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime session_ids selector requires session_ids")
		}
	case "workspace":
		if selector.Global || len(selector.SessionIDs) > 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime workspace selector cannot be combined with global or session_ids")
		}
		if len(workspacePaths) == 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime workspace selector requires workspace_path or workspace_paths")
		}
	case "recent":
		if len(selector.SessionIDs) > 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime recent selector cannot be combined with session_ids")
		}
		if selector.Recent.Limit <= 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime recent selector requires recent.limit")
		}
		if selector.Global && len(workspacePaths) > 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime recent global selector cannot be combined with workspace_path or workspace_paths")
		}
		if !selector.Global && len(workspacePaths) == 0 {
			return V3RealtimeWorksetSelector{}, errors.New("v3 realtime recent selector requires workspace_path, workspace_paths, or global=true")
		}
	default:
		return V3RealtimeWorksetSelector{}, fmt.Errorf("unsupported v3 realtime selector.kind %q", selector.Kind)
	}
	return selector, nil
}

func v3RealtimeWorksetResourceAllowed(resource string) bool {
	switch strings.TrimSpace(resource) {
	case "sessions", "projections", "events", "messages", "run_intents", "current_run_state", "permission_summaries", "active_plan", "plan_revisions", "membership", "tombstones":
		return true
	default:
		return false
	}
}

func canonicalV3RealtimeWorksetResources(resources []string) ([]string, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(resources))
	for _, resource := range resources {
		resource = strings.TrimSpace(resource)
		if resource == "" {
			continue
		}
		if !v3RealtimeWorksetResourceAllowed(resource) {
			return nil, fmt.Errorf("unsupported v3 realtime workset resource %q", resource)
		}
		if _, ok := seen[resource]; ok {
			continue
		}
		seen[resource] = struct{}{}
		out = append(out, resource)
	}
	sort.Strings(out)
	return out, nil
}

func v3RealtimeSessionMatchesWorksetSelector(principal identity.Principal, session pebblestore.SessionSnapshot, selector V3RealtimeWorksetSelector) bool {
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return false
	}
	if strings.TrimSpace(session.UserID) == "" || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
		return false
	}
	for _, sessionID := range selector.SessionIDs {
		if strings.TrimSpace(sessionID) == session.ID {
			return true
		}
	}
	if strings.TrimSpace(selector.Kind) == "global" || selector.Global {
		return true
	}
	paths := selector.WorkspacePaths
	if strings.TrimSpace(selector.WorkspacePath) != "" {
		paths = append([]string{selector.WorkspacePath}, paths...)
	}
	if len(paths) == 0 && selector.Recent.Limit > 0 {
		return true
	}
	candidates := []string{
		strings.TrimSpace(session.WorkspacePath),
		strings.TrimSpace(session.WorktreeRootPath),
		sessionsV3MetadataString(session.Metadata, "swarm_v3_tui_cwd_path"),
		sessionsV3MetadataString(session.Metadata, "swarm_v3_tui_worktree_path"),
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		for _, path := range paths {
			if strings.TrimSpace(path) != "" && strings.TrimSpace(path) == strings.TrimSpace(candidate) {
				return true
			}
		}
	}
	return false
}

func orderedV3RealtimeWorksets(worksets map[string]v3RealtimeWorksetSubscription) []v3RealtimeWorksetSubscription {
	out := make([]v3RealtimeWorksetSubscription, 0, len(worksets))
	for _, workset := range worksets {
		out = append(out, workset)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WorksetID == out[j].WorksetID {
			return out[i].SubscriptionID < out[j].SubscriptionID
		}
		return out[i].WorksetID < out[j].WorksetID
	})
	return out
}

func v3RealtimeWorksetIncludesRecordResource(workset v3RealtimeWorksetSubscription, record sessionruntime.RealtimeOutboxRecord) bool {
	switch strings.TrimSpace(record.Event.EventType) {
	case "permission.summary.updated":
		return v3RealtimeWorksetIncludesResource(workset, "permission_summaries")
	default:
		return true
	}
}

func v3RealtimeWorksetIncludesResource(workset v3RealtimeWorksetSubscription, resource string) bool {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return false
	}
	for _, candidate := range workset.Resources {
		if strings.TrimSpace(candidate) == resource {
			return true
		}
	}
	return false
}

func v3RealtimeAutoSubscriptionID(workset v3RealtimeWorksetSubscription, sessionID string) string {
	base := strings.TrimSpace(workset.SubscriptionID)
	if base == "" {
		base = strings.TrimSpace(workset.WorksetID)
	}
	if base == "" {
		base = "workset"
	}
	return base + ":session:" + strings.TrimSpace(sessionID)
}

func v3RealtimeRecordRemovesFromWorkset(record sessionruntime.RealtimeOutboxRecord) bool {
	switch strings.TrimSpace(record.Event.EventType) {
	case "session.archived", "session.deleted":
		return true
	default:
		return false
	}
}

func v3RealtimeRecordChangesVisibility(record sessionruntime.RealtimeOutboxRecord) bool {
	return strings.TrimSpace(record.Event.EventType) == "session.visibility.changed"
}

func (s *Server) sendV3RealtimeWorksetSessionFrame(conn *transportws.Conn, kind string, workset v3RealtimeWorksetSubscription, subscription v3RealtimeSubscription, record sessionruntime.RealtimeOutboxRecord, scope v3SyncCursorScope) bool {
	cursor, err := s.signV3SyncEndpointCursor(scope, record.EndpointSeq)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(record.SessionID, "cursor_sign_failed", err.Error(), record.EndpointSeq-1, record.EndpointSeq))
		return false
	}
	message := V3RealtimeMessage{
		Protocol:              V3RealtimeProtocol,
		ProtocolVersion:       V3RealtimeProtocolVersion,
		Kind:                  kind,
		WorksetID:             workset.WorksetID,
		WorksetSubscriptionID: workset.SubscriptionID,
		SessionID:             record.SessionID,
		SubscriptionID:        subscription.SubscriptionID,
		AutoSubscribed:        subscription.AutoSubscribed,
		EndpointCursor:        cursor,
		Rev:                   record.EndpointSeq,
		PrevRev:               record.EndpointSeq - 1,
		EventType:             record.Event.EventType,
		Projection:            &record.Projection,
	}
	if v3RealtimeWorksetIncludesResource(workset, "permission_summaries") {
		if summary, ok := v3RealtimePermissionSummaryFromRecord(record); ok {
			message.PermissionSummary = &summary
		}
	}
	if kind == V3RealtimeKindWorksetSessionUpdated || kind == V3RealtimeKindWorksetSessionDiscovered {
		if session, ok := s.v3RealtimeSessionSnapshotForRecord(record); ok {
			shell := sessionsV3SyncSessionShell(session)
			message.Session = &shell
		} else if session, ok, err := s.sessions.GetSession(record.SessionID); err != nil {
			_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(record.SessionID, "session_shell_lookup_failed", err.Error(), record.EndpointSeq-1, record.EndpointSeq))
			return false
		} else if ok {
			shell := sessionsV3SyncSessionShell(session)
			message.Session = &shell
		}
		if state, ok, err := s.sessions.GetSessionRunState(record.SessionID); err == nil && ok {
			message.CurrentRunState = &state
		} else if err != nil {
			_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(record.SessionID, "run_state_lookup_failed", err.Error(), record.EndpointSeq-1, record.EndpointSeq))
			return false
		}
	}
	return s.sendV3RealtimeMessage(conn, message) == nil
}

func cloneV3RealtimeSubscriptions(in map[string]v3RealtimeSubscription) map[string]v3RealtimeSubscription {
	out := make(map[string]v3RealtimeSubscription, len(in))
	for sessionID, sub := range in {
		if sub.WorksetIDs != nil {
			worksetIDs := make(map[string]struct{}, len(sub.WorksetIDs))
			for worksetID := range sub.WorksetIDs {
				worksetIDs[worksetID] = struct{}{}
			}
			sub.WorksetIDs = worksetIDs
		}
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

func v3RealtimePermissionSummaryFromRecord(record sessionruntime.RealtimeOutboxRecord) (sessionsV3PermissionSummary, bool) {
	if strings.TrimSpace(record.Event.EventType) != "permission.summary.updated" || len(record.Event.Payload) == 0 {
		return sessionsV3PermissionSummary{}, false
	}
	var payload struct {
		PermissionSummary *sessionsV3PermissionSummary `json:"permission_summary,omitempty"`
		Summary           *sessionsV3PermissionSummary `json:"summary,omitempty"`
		SessionID         string                       `json:"session_id,omitempty"`
	}
	if err := json.Unmarshal(record.Event.Payload, &payload); err != nil {
		return sessionsV3PermissionSummary{}, false
	}
	summary := payload.PermissionSummary
	if summary == nil {
		summary = payload.Summary
	}
	if summary == nil {
		return sessionsV3PermissionSummary{}, false
	}
	out := *summary
	if strings.TrimSpace(out.SessionID) == "" {
		out.SessionID = firstNonEmpty(strings.TrimSpace(payload.SessionID), strings.TrimSpace(record.SessionID))
	}
	if out.PendingApprovalCount <= 0 {
		out.PendingApprovalCount = 0
		out.OldestPendingAt = 0
		out.NewestPendingAt = 0
	}
	return out, strings.TrimSpace(out.SessionID) != ""
}

func v3RealtimeRecordVisibleToPrincipal(principal identity.Principal, record sessionruntime.RealtimeOutboxRecord) bool {
	if !principal.Valid() || record.Event.Seq == 0 {
		return false
	}
	if strings.TrimSpace(record.AccountScopeID) == "" || strings.TrimSpace(record.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return false
	}
	if strings.TrimSpace(record.UserID) == "" || strings.TrimSpace(record.UserID) != strings.TrimSpace(principal.UserID) {
		return false
	}
	return true
}

func (s *Server) v3RealtimePrincipalCanSee(principal identity.Principal, record sessionruntime.RealtimeOutboxRecord) bool {
	return v3RealtimeRecordVisibleToPrincipal(principal, record)
}

func (s *Server) sendV3RealtimeOutboxEvent(conn *transportws.Conn, record sessionruntime.RealtimeOutboxRecord, scope v3SyncCursorScope) bool {
	cursor, err := s.signV3SyncEndpointCursor(scope, record.EndpointSeq)
	if err != nil {
		_ = s.sendV3RealtimeMessage(conn, NewV3RealtimeCursorError(record.SessionID, "cursor_sign_failed", err.Error(), record.EndpointSeq-1, record.EndpointSeq))
		return false
	}
	event := record.Event
	event.Payload = sanitizeV3SyncStreamEventPayload(event.EventType, event.Payload)
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
		Event:            &event,
		Projection:       &record.Projection,
	}
	return s.sendV3RealtimeMessage(conn, message) == nil
}

func (s *Server) sendV3RealtimeMessage(conn *transportws.Conn, message V3RealtimeMessage) error {
	if err := ValidateV3RealtimeOutboundServerMessage(message); err != nil {
		return err
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return s.writeV3RealtimePayload(conn, raw)
}

func (s *Server) sendV3RealtimeLivePatch(conn *transportws.Conn, patch V3RealtimeLivePatch) error {
	message := NewV3RealtimeLiveMessage(patch)
	if err := ValidateV3RealtimeLiveMessage(message); err != nil {
		return err
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return s.writeV3RealtimePayload(conn, raw)
}

func (s *Server) writeV3RealtimePayload(conn *transportws.Conn, raw []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(v3RealtimeWriteTimeout)); err != nil {
		return err
	}
	if observer := v3RealtimeWriteObserver; observer != nil {
		observer(+1)
		defer observer(-1)
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

func containsV3RealtimeCapability(capabilities []string, capability string) bool {
	capability = strings.TrimSpace(capability)
	if capability == "" {
		return false
	}
	for _, candidate := range capabilities {
		if strings.TrimSpace(candidate) == capability {
			return true
		}
	}
	return false
}
