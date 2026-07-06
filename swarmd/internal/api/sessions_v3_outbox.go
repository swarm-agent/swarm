package api

import (
	"errors"
	"log"
	"strings"

	"swarm/packages/swarmd/internal/identity"
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
	if err := s.publishCommittedSessionV3MutationInputResult(input, result); err != nil {
		s.recordV3StoreResultDiagnostic(input, result, err)
		return result, err
	}
	s.recordV3StoreResultDiagnostic(input, result, nil)
	return result, nil
}

func (s *Server) publishCommittedSessionV3MutationResult(result sessionruntime.SessionMutationResult) error {
	return s.publishCommittedSessionV3MutationInputResult(sessionruntime.SessionMutationInput{}, result)
}

func (s *Server) publishCommittedSessionV3MutationInputResult(input sessionruntime.SessionMutationInput, result sessionruntime.SessionMutationResult) error {
	if result.Replayed || result.Event.Seq == 0 {
		return nil
	}
	if result.RealtimeOutbox == nil || result.RealtimeOutbox.EndpointSeq == 0 {
		return errors.New("committed v3 session mutation is missing durable realtime outbox record")
	}
	if sessionV3IsDiagnosticEventType(result.Event.EventType) {
		return nil
	}
	if err := s.publishCommittedV3RealtimeOutbox(*result.RealtimeOutbox); err != nil {
		log.Printf("warning: v3 realtime outbox wake failed after durable commit session=%q endpoint_seq=%d: %v", result.SessionID, result.RealtimeOutbox.EndpointSeq, err)
	}
	if err := s.publishCommittedSessionV3GlobalEvent(result); err != nil {
		log.Printf("warning: v3 session global mirror publish failed after durable commit session=%q seq=%d: %v", result.SessionID, result.Event.Seq, err)
	}
	s.maybeStartCommittedSessionV3TitleFlow(input, result)
	return nil
}

func (s *Server) maybeStartCommittedSessionV3TitleFlow(input sessionruntime.SessionMutationInput, result sessionruntime.SessionMutationResult) {
	if s == nil || s.v3SessionExecutor == nil || result.Replayed || result.Message == nil || result.RunIntent == nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(result.Message.Role), "user") {
		return
	}
	status := strings.TrimSpace(result.RunIntent.Status)
	if status != sessionruntime.RunIntentPendingExecutor && status != sessionruntime.RunIntentRunning {
		return
	}
	sessionID := firstNonEmpty(strings.TrimSpace(result.SessionID), strings.TrimSpace(input.SessionID), strings.TrimSpace(result.Message.SessionID), strings.TrimSpace(result.RunIntent.SessionID))
	runID := strings.TrimSpace(result.RunIntent.RunID)
	if sessionID == "" || runID == "" {
		return
	}
	principal := identity.Principal{
		Type:           identity.PrincipalTypeUser,
		UserID:         firstNonEmpty(strings.TrimSpace(input.UserID), strings.TrimSpace(result.Message.UserID), strings.TrimSpace(result.RunIntent.UserID)),
		AccountScopeID: firstNonEmpty(strings.TrimSpace(input.AccountScopeID), strings.TrimSpace(result.Message.AccountScopeID), strings.TrimSpace(result.RunIntent.AccountScopeID)),
	}
	if result.Session != nil {
		principal.UserID = firstNonEmpty(principal.UserID, strings.TrimSpace(result.Session.UserID))
		principal.AccountScopeID = firstNonEmpty(principal.AccountScopeID, strings.TrimSpace(result.Session.AccountScopeID))
	}
	s.v3SessionExecutor.maybeStartSessionV3TitleFlow(sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID}, result)
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

var publishCommittedV3RealtimeOutboxWake = func(hub *v3RealtimeOutboxHub, record sessionruntime.RealtimeOutboxRecord) error {
	hub.publish(record)
	return nil
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
	return publishCommittedV3RealtimeOutboxWake(s.v3RealtimeOutbox, record)
}
