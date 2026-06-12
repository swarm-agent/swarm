package api

import (
	"errors"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// applySessionV3PrimaryMutation is the only server-side V3 mutation entrypoint.
// The store commits through ApplySessionMutation first; only the committed,
// non-replayed durable realtime outbox row returned by that call is then used
// to wake canonical realtime delivery. V3 session events are not mirrored to
// global /ws session channels; /v3/realtime/stream is the single transport.
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
	return nil
}
