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
type SessionReplay = pebblestore.V3SessionReplay
type RealtimeOutboxRecord = pebblestore.V3RealtimeOutboxRecord

type SessionIdempotencyRecord = pebblestore.V3SessionIdempotencyRecord

const (
	SessionMutationCreateSession    = pebblestore.V3SessionMutationCreateSession
	SessionMutationAppendMessage    = pebblestore.V3SessionMutationAppendMessage
	SessionMutationUpsertLifecycle  = pebblestore.V3SessionMutationUpsertLifecycle
	SessionMutationRecordRunIntent  = pebblestore.V3SessionMutationRecordRunIntent
	SessionMutationRecordDiagnostic = pebblestore.V3SessionMutationRecordDiagnostic
	SessionMutationRecordUsage      = pebblestore.V3SessionMutationRecordUsage
	SessionMutationUpdateMode       = pebblestore.V3SessionMutationUpdateMode
	SessionMutationUpdatePreference = pebblestore.V3SessionMutationUpdatePreference
	SessionMutationUpdateMetadata   = pebblestore.V3SessionMutationUpdateMetadata
	SessionMutationUpdateTitle      = pebblestore.V3SessionMutationUpdateTitle

	RunIntentPendingExecutor = pebblestore.V3RunIntentPendingExecutor
	RunIntentRunning         = pebblestore.V3RunIntentRunning
	RunIntentCompleted       = pebblestore.V3RunIntentCompleted
	RunIntentFailed          = pebblestore.V3RunIntentFailed
	RunIntentCancelled       = pebblestore.V3RunIntentCancelled
	RunIntentExpired         = pebblestore.V3RunIntentExpired
	RunIntentInterrupted     = pebblestore.V3RunIntentInterrupted
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

func (s *Service) ListSessionMessages(sessionID string, afterSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionMessages(sessionID, afterSeq, limit)
}

func (s *Service) ListSessionMessageTail(sessionID string, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionMessageTail(sessionID, limit)
}

func (s *Service) ListSessionMessagesBefore(sessionID string, beforeSeq uint64, limit int) ([]pebblestore.MessageSnapshot, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionMessagesBefore(sessionID, beforeSeq, limit)
}

func (s *Service) GetSessionProjection(sessionID string) (SessionProjection, bool, error) {
	if s == nil || s.store == nil {
		return SessionProjection{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionProjection(sessionID)
}

func (s *Service) HydrateSessionSnapshot(sessionID string, messageLimit, eventLimit int) (pebblestore.V3SessionHydration, bool, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionHydration{}, false, errors.New("session store is not configured")
	}
	return s.store.HydrateV3SessionSnapshot(sessionID, messageLimit, eventLimit)
}

func (s *Service) BuildSessionWorkset(options pebblestore.V3SessionWorksetOptions) (pebblestore.V3SessionWorksetResult, error) {
	if s == nil || s.store == nil {
		return pebblestore.V3SessionWorksetResult{}, errors.New("session store is not configured")
	}
	return s.store.BuildV3SessionWorkset(options)
}

func (s *Service) ReplaySessionEvents(sessionID string, afterSeq uint64, limit int) (SessionReplay, error) {
	if s == nil || s.store == nil {
		return SessionReplay{}, errors.New("session store is not configured")
	}
	return s.store.ReplayV3SessionEvents(sessionID, afterSeq, limit)
}
func (s *Service) ListRealtimeOutboxAfter(afterEndpointSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxAfter(afterEndpointSeq, limit)
}

func (s *Service) ListRealtimeOutboxForAuthScopeAfter(accountScopeID, userID string, afterEndpointSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxForAuthScopeAfter(accountScopeID, userID, afterEndpointSeq, limit)
}

func (s *Service) ListRealtimeOutboxForSessionAfterEndpoint(sessionID string, afterEndpointSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxForSessionAfterEndpoint(sessionID, afterEndpointSeq, limit)
}

func (s *Service) ListRealtimeOutboxForSessionsAfterEndpoint(sessionIDs []string, afterEndpointSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxForSessionsAfterEndpoint(sessionIDs, afterEndpointSeq, limit)
}

func (s *Service) LastRealtimeOutboxForSessionAtOrBeforeEndpoint(sessionID string, endpointSeq uint64) (RealtimeOutboxRecord, bool, error) {
	if s == nil || s.store == nil {
		return RealtimeOutboxRecord{}, false, errors.New("session store is not configured")
	}
	return s.store.LastV3RealtimeOutboxForSessionAtOrBeforeEndpoint(sessionID, endpointSeq)
}

func (s *Service) CurrentRealtimeOutboxRevision() (uint64, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("session store is not configured")
	}
	return s.store.CurrentV3RealtimeOutboxRevision()
}

func (s *Service) CurrentRealtimeOutboxCursor() (string, error) {
	if s == nil || s.store == nil {
		return "", errors.New("session store is not configured")
	}
	return s.store.CurrentV3RealtimeOutboxCursor()
}

func (s *Service) ListRealtimeOutboxForSessionAfterSeq(sessionID string, afterSeq uint64, limit int) ([]RealtimeOutboxRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3RealtimeOutboxForSessionAfterSeq(sessionID, afterSeq, limit)
}

func (s *Service) GetSessionRunIntent(sessionID, runID string) (SessionRunIntent, bool, error) {
	if s == nil || s.store == nil {
		return SessionRunIntent{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionRunIntent(sessionID, runID)
}

func (s *Service) GetSessionActiveRunIntent(sessionID string) (SessionRunIntent, bool, error) {
	if s == nil || s.store == nil {
		return SessionRunIntent{}, false, errors.New("session store is not configured")
	}
	return s.store.GetV3SessionActiveRunIntent(sessionID)
}

func (s *Service) ListSessionRunIntents(sessionID string, afterSeq uint64, limit int) ([]SessionRunIntent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionRunIntents(sessionID, afterSeq, limit)
}

func (s *Service) ListSessionRunIntentsByStatus(status string, limit int) ([]SessionRunIntent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionRunIntentsByStatus(status, limit)
}

func (s *Service) ListRecoverableSessionRunIntents(staleRunningBeforeUnixMs int64, limit int) ([]SessionRunIntent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	return s.store.ListV3SessionRecoverableRunIntents(staleRunningBeforeUnixMs, limit)
}
