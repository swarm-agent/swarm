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
	result, err := s.sessions.ApplySessionMutation(input)
	if err != nil {
		return result, err
	}
	s.publishCommittedSessionV3MutationResult(result)
	return result, nil
}

func (s *Server) publishCommittedSessionV3MutationResult(result sessionruntime.SessionMutationResult) {
	if result.Replayed || result.Event.Seq == 0 {
		return
	}
	s.publishCommittedSessionV3Event(result.Event)
}

func (s *Server) publishCommittedSessionV3Event(event sessionruntime.SessionEvent) {
	if s == nil || s.v3SessionStreams == nil || event.Seq == 0 {
		return
	}
	s.v3SessionStreams.publish(event)
}
