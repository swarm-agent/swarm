package api

import (
	"errors"
	"log"
	"strings"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// applySessionV3PrimaryMutation is the only server-side V3 mutation entrypoint.
// The store commits through ApplySessionMutation first; the committed,
// non-replayed durable realtime outbox row returned by that call is the accepted
// delivery truth. Post-commit in-memory/global wakeups are accelerators only:
// failures there must not make an already committed durable mutation fail.
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
	if err := s.publishCommittedV3RealtimeOutbox(*result.RealtimeOutbox); err != nil {
		log.Printf("warning: v3 realtime outbox wake failed after durable commit session=%q endpoint_seq=%d: %v", result.SessionID, result.RealtimeOutbox.EndpointSeq, err)
	}
	if err := s.publishCommittedSessionV3GlobalEvent(result); err != nil {
		log.Printf("warning: v3 session global mirror publish failed after durable commit session=%q seq=%d: %v", result.SessionID, result.Event.Seq, err)
	}
	return nil
}

var appendCommittedSessionV3GlobalEvent = func(s *Server, event sessionruntime.SessionEvent) error {
	env, err := s.events.AppendWithSourceSeq("session:"+event.SessionID, event.EventType, event.SessionID, event.Payload, "v3", event.Seq, event.CausationID, event.CorrelationID)
	if err != nil {
		return err
	}
	if s.hub != nil {
		s.hub.Publish(env)
	}
	return nil
}

func (s *Server) publishCommittedSessionV3GlobalEvent(result sessionruntime.SessionMutationResult) error {
	if s == nil || s.events == nil || result.Event.Seq == 0 || strings.TrimSpace(result.Event.SessionID) == "" {
		return nil
	}
	return appendCommittedSessionV3GlobalEvent(s, result.Event)
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
	return nil
}
