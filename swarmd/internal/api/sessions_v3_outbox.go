package api

import (
	"errors"

	sessionruntime "swarm/packages/swarmd/internal/session"
)

// applySessionV3PrimaryMutation is the only server-side V3 mutation entrypoint.
// The store commits through ApplySessionMutation first; only the committed,
// non-replayed event returned by that call is then offered to the V3 stream hub.
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
	s.publishCommittedSessionV3MutationResult(result)
	s.recordV3StoreResultDiagnostic(input, result, nil)
	return result, nil
}

func (s *Server) publishCommittedSessionV3MutationResult(result sessionruntime.SessionMutationResult) {
	if result.Replayed || result.Event.Seq == 0 {
		return
	}
	s.publishCommittedSessionV3Event(result.Event)
	if result.RealtimeOutbox != nil {
		s.publishCommittedV3RealtimeOutbox(*result.RealtimeOutbox)
	}
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
