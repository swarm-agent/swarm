package api

import (
	"errors"
	"fmt"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// applySessionV3PrimaryMutation is the only server-side V3 mutation entrypoint.
// The store commits through ApplySessionMutation first; only the committed,
// non-replayed event returned by that call is then offered to the canonical
// global websocket stream and the V3-specific compatibility stream hubs.
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
	if err := s.publishCommittedSessionV3GlobalEvent(result.Event); err != nil {
		return err
	}
	s.publishCommittedSessionV3Event(result.Event)
	if result.RealtimeOutbox != nil {
		s.publishCommittedV3RealtimeOutbox(*result.RealtimeOutbox)
	}
	return nil
}

func (s *Server) publishCommittedSessionV3GlobalEvent(event sessionruntime.SessionEvent) error {
	if s == nil {
		return errors.New("server is not configured")
	}
	if event.Seq == 0 {
		return nil
	}
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return errors.New("committed v3 session event is missing session_id")
	}
	eventType := strings.TrimSpace(event.EventType)
	if eventType == "" {
		return errors.New("committed v3 session event is missing event_type")
	}
	if s.events == nil {
		return errors.New("global event log is not configured for v3 session events")
	}
	appended, err := s.events.AppendWithSource("session:"+sessionID, eventType, sessionID, event.Payload, "v3", event.CausationID, event.CorrelationID)
	if err != nil {
		return fmt.Errorf("append committed v3 session event to global stream: %w", err)
	}
	if s.hub != nil {
		s.hub.Publish(appended)
	}
	return nil
}

func (s *Server) publishCommittedSessionV3Event(event sessionruntime.SessionEvent) {
	if s == nil || s.v3SessionStreams == nil || event.Seq == 0 {
		return
	}
	s.v3SessionStreams.publish(event)
}

func (s *Server) publishCommittedV3RealtimeOutbox(record sessionruntime.RealtimeOutboxRecord) {
	if s == nil || record.EndpointSeq == 0 {
		return
	}
	if s.v3RealtimeOutbox == nil {
		s.v3RealtimeOutbox = newV3RealtimeOutboxHub()
	}
	s.v3RealtimeOutbox.publish(record)
}
