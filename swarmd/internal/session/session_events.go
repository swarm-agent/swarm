package session

import (
	"errors"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type SessionMutationInput = pebblestore.V3SessionMutationInput
type SessionMutationResult = pebblestore.V3SessionMutationResult
type SessionEvent = pebblestore.V3SessionEvent
type SessionProjection = pebblestore.V3SessionProjection
type SessionRunIntent = pebblestore.V3SessionRunIntent

type SessionIdempotencyRecord = pebblestore.V3SessionIdempotencyRecord

const (
	SessionMutationCreateSession   = pebblestore.V3SessionMutationCreateSession
	SessionMutationAppendMessage   = pebblestore.V3SessionMutationAppendMessage
	SessionMutationUpsertLifecycle = pebblestore.V3SessionMutationUpsertLifecycle
	SessionMutationRecordRunIntent = pebblestore.V3SessionMutationRecordRunIntent

	RunIntentPendingExecutor = pebblestore.V3RunIntentPendingExecutor
	RunIntentDispatchBlocked = pebblestore.V3RunIntentDispatchBlocked
)

var ErrSessionIdempotencyConflict = pebblestore.ErrV3IdempotencyConflict

func (s *Service) ApplySessionMutation(input SessionMutationInput) (SessionMutationResult, error) {
	if s == nil || s.store == nil {
		return SessionMutationResult{}, errors.New("session store is not configured")
	}
	return s.store.ApplyV3SessionMutation(input)
}

func (s *Service) ListSessionEvents(sessionID string, afterSeq uint64, limit int) ([]SessionEvent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionEvents(sessionID, afterSeq, limit)
}

func (s *Service) GetSessionProjection(sessionID string) (SessionProjection, bool, error) {
	if s == nil || s.store == nil {
		return SessionProjection{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionProjection(sessionID)
}
