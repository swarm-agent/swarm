package api

import (
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// applySessionV3PrimaryMutation is the only server-side V3 mutation entrypoint.
// The store commits through ApplySessionMutation first; only the committed,
// non-replayed durable realtime outbox row returned by that call is then used
// to wake canonical realtime delivery. Legacy session mirrors, while present,
// are generated downstream from that durable outbox row.
func (s *Server) applySessionV3PrimaryMutation(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
	if s == nil || s.sessions == nil {
		return sessionruntime.SessionMutationResult{}, errors.New("sessions v3 service is not configured")
	}
	s.recordV3StoreInputDiagnostic(input)
	result, err := s.sessions.ApplySessionMutation(input)
	if err != nil {
		s.recordV3StoreResultDiagnostic(input, result, err)
		return result, err
	}
	if err := s.publishCommittedSessionV3MutationResult(result); err != nil {
		s.recordV3StoreResultDiagnostic(input, result, err)
		return result, err
	}
	s.recordV3StoreResultDiagnostic(input, result, nil)
	return result, nil
}

func (s *Server) publishCommittedSessionV3MutationResult(result sessionruntime.SessionMutationResult) error {
	if result.Replayed || result.Event.Seq == 0 {
		return nil
	}
	if result.RealtimeOutbox == nil || result.RealtimeOutbox.EndpointSeq == 0 {
		return errors.New("committed v3 session mutation is missing durable realtime outbox record")
	}
	return s.publishCommittedV3RealtimeOutbox(*result.RealtimeOutbox)
}

func (s *Server) publishCommittedV3RealtimeOutbox(record sessionruntime.RealtimeOutboxRecord) error {
	if s == nil {
		return errors.New("server is not configured")
	}
	if record.EndpointSeq == 0 {
		return nil
	}
	if s.v3RealtimeOutbox == nil {
		s.v3RealtimeOutbox = newV3RealtimeOutboxHub()
	}
	s.v3RealtimeOutbox.publish(record)
	return s.publishV3RealtimeOutboxCompatibilityMirrors(record)
}

func (s *Server) publishV3RealtimeOutboxCompatibilityMirrors(record sessionruntime.RealtimeOutboxRecord) error {
	if err := s.mirrorV3RealtimeOutboxToGlobalSessionCompatibility(record); err != nil {
		return err
	}
	return nil
}

func (s *Server) mirrorV3RealtimeOutboxToGlobalSessionCompatibility(record sessionruntime.RealtimeOutboxRecord) error {
	if s == nil || record.EndpointSeq == 0 || record.Event.Seq == 0 {
		return nil
	}
	event := record.Event
	sessionID := strings.TrimSpace(record.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(event.SessionID)
	}
	if sessionID == "" {
		return errors.New("committed v3 realtime outbox record is missing session_id")
	}
	eventType := strings.TrimSpace(event.EventType)
	if eventType == "" {
		return errors.New("committed v3 realtime outbox record is missing event_type")
	}
	if s.events == nil {
		return nil
	}
	appended, err := s.events.AppendWithSourceSeq("session:"+sessionID, eventType, sessionID, event.Payload, "v3", event.Seq, event.CausationID, event.CorrelationID)
	if err != nil {
		return fmt.Errorf("append committed v3 session compatibility event to global stream: %w", err)
	}
	if s.hub != nil {
		s.hub.Publish(appended)
	}
	return nil
}
