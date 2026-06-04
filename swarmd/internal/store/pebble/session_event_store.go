package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	V3SessionMutationCreateSession   = "session.create"
	V3SessionMutationAppendMessage   = "message.append"
	V3SessionMutationUpsertLifecycle = "lifecycle.upsert"
	V3SessionMutationRecordRunIntent = "run_intent.record"

	V3SessionMutationResponseVersion = "v3.session_mutation.result.v1"
	V3SessionMutationStatusCompleted = "completed"
	V3SessionMutationStatusConflict  = "idempotency_conflict"

	V3RunIntentPendingExecutor = "pending_executor"
	V3RunIntentDispatchBlocked = "dispatch_blocked"
)

var (
	ErrV3IdempotencyConflict = errors.New("v3 session idempotency conflict")
	v3SessionMutationMu      sync.Mutex
)

type V3SessionMutationInput struct {
	SessionID       string                    `json:"session_id"`
	UserID          string                    `json:"user_id,omitempty"`
	AccountScopeID  string                    `json:"account_scope_id,omitempty"`
	ClientRequestID string                    `json:"client_request_id,omitempty"`
	IdempotencyKey  string                    `json:"idempotency_key,omitempty"`
	PayloadHash     string                    `json:"payload_hash,omitempty"`
	RequestHash     string                    `json:"request_hash,omitempty"`
	Kind            string                    `json:"kind"`
	EventID         string                    `json:"event_id,omitempty"`
	EventType       string                    `json:"event_type,omitempty"`
	EventPayload    json.RawMessage           `json:"event_payload,omitempty"`
	CausationID     string                    `json:"causation_id,omitempty"`
	CorrelationID   string                    `json:"correlation_id,omitempty"`
	Session         *SessionSnapshot          `json:"session,omitempty"`
	Message         *MessageSnapshot          `json:"message,omitempty"`
	Lifecycle       *SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
	RunIntent       *V3SessionRunIntent       `json:"run_intent,omitempty"`
	NowUnixMs       int64                     `json:"now_unix_ms,omitempty"`
}

type V3SessionMutationResult struct {
	SessionID       string                     `json:"session_id"`
	PrimarySeq      uint64                     `json:"primary_seq"`
	FirstSeq        uint64                     `json:"first_seq"`
	LastSeq         uint64                     `json:"last_seq"`
	EventIDs        []string                   `json:"event_ids"`
	PayloadHash     string                     `json:"payload_hash"`
	ResponseVersion string                     `json:"response_version,omitempty"`
	ResponseStatus  string                     `json:"response_status,omitempty"`
	ResponseBody    json.RawMessage            `json:"response_body,omitempty"`
	Conflict        *V3SessionMutationConflict `json:"conflict,omitempty"`
	Error           *V3SessionMutationError    `json:"error,omitempty"`
	Event           V3SessionEvent             `json:"event"`
	Session         *SessionSnapshot           `json:"session,omitempty"`
	Message         *MessageSnapshot           `json:"message,omitempty"`
	Lifecycle       *SessionLifecycleSnapshot  `json:"lifecycle,omitempty"`
	RunIntent       *V3SessionRunIntent        `json:"run_intent,omitempty"`
	Projection      V3SessionProjection        `json:"projection"`
	Idempotency     V3SessionIdempotencyRecord `json:"idempotency"`
	Replayed        bool                       `json:"replayed,omitempty"`
}

type V3SessionMutationStoredResult struct {
	SessionID                  string                     `json:"session_id"`
	MessageID                  string                     `json:"message_id,omitempty"`
	RunID                      string                     `json:"run_id,omitempty"`
	FirstSeq                   uint64                     `json:"first_seq"`
	LastSeq                    uint64                     `json:"last_seq"`
	EventIDs                   []string                   `json:"event_ids"`
	PayloadHash                string                     `json:"payload_hash"`
	ResponseVersion            string                     `json:"response_version"`
	ResponseStatus             string                     `json:"response_status"`
	ResponseBody               json.RawMessage            `json:"response_body,omitempty"`
	Conflict                   *V3SessionMutationConflict `json:"conflict,omitempty"`
	Error                      *V3SessionMutationError    `json:"error,omitempty"`
	EventType                  string                     `json:"event_type"`
	LastEventSeq               uint64                     `json:"last_event_seq"`
	ProjectionHighWatermarkSeq uint64                     `json:"projection_high_watermark_seq"`
	AppliedAt                  int64                      `json:"applied_at"`
}

type V3SessionMutationConflict struct {
	Code                string `json:"code"`
	Message             string `json:"message"`
	ExistingPayloadHash string `json:"existing_payload_hash,omitempty"`
	IncomingPayloadHash string `json:"incoming_payload_hash,omitempty"`
}

type V3SessionMutationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type V3SessionIdempotencyRecord struct {
	SessionID       string                        `json:"session_id"`
	UserID          string                        `json:"user_id,omitempty"`
	AccountScopeID  string                        `json:"account_scope_id,omitempty"`
	Operation       string                        `json:"operation"`
	ClientRequestID string                        `json:"client_request_id"`
	Key             string                        `json:"key"`
	PayloadHash     string                        `json:"payload_hash"`
	RequestHash     string                        `json:"request_hash,omitempty"`
	Kind            string                        `json:"kind"`
	Status          string                        `json:"status"`
	Result          V3SessionMutationStoredResult `json:"result"`
	CreatedAt       int64                         `json:"created_at"`
	CompletedAt     int64                         `json:"completed_at"`
}

type V3SessionEvent struct {
	ID            string          `json:"id"`
	SessionID     string          `json:"session_id"`
	Seq           uint64          `json:"seq"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	TsUnixMs      int64           `json:"ts_unix_ms"`
	CausationID   string          `json:"causation_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
}

type V3SessionProjection struct {
	SessionID                  string `json:"session_id"`
	LastEventSeq               uint64 `json:"last_event_seq"`
	ProjectionHighWatermarkSeq uint64 `json:"projection_high_watermark_seq"`
	UpdatedAt                  int64  `json:"updated_at"`
}

type V3SessionRunIntent struct {
	SessionID      string `json:"session_id"`
	UserID         string `json:"user_id,omitempty"`
	AccountScopeID string `json:"account_scope_id,omitempty"`
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	BlockedReason  string `json:"blocked_reason,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	EventSeq       uint64 `json:"event_seq"`
}

func KeyV3SessionSequence(sessionID string) string {
	return fmt.Sprintf("v3/session_seq/%s", keyPart(sessionID))
}

func KeyV3SessionEvent(sessionID string, seq uint64) string {
	return fmt.Sprintf("v3/session_event/%s/%020d", keyPart(sessionID), seq)
}

func V3SessionEventPrefix(sessionID string) string {
	part := keyPart(sessionID)
	if part == "" {
		return "v3/session_event/"
	}
	return fmt.Sprintf("v3/session_event/%s/", part)
}

func KeyV3SessionProjection(sessionID string) string {
	return fmt.Sprintf("v3/session_projection/%s", keyPart(sessionID))
}

func KeyV3SessionIdempotency(accountScopeID, sessionID, idempotencyKey string) string {
	return fmt.Sprintf("v3/session_idempotency/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(idempotencyKey))
}

func KeyV3SessionOperationIdempotency(accountScopeID, sessionID, operation, clientRequestID string) string {
	return KeyV3SessionIdempotency(accountScopeID, sessionID, strings.Join([]string{operation, clientRequestID}, "/"))
}

func KeyV3SessionMessage(sessionID string, primarySeq uint64) string {
	return fmt.Sprintf("v3/session_message/%s/%020d", keyPart(sessionID), primarySeq)
}

func V3SessionMessagePrefix(sessionID string) string {
	return fmt.Sprintf("v3/session_message/%s/", keyPart(sessionID))
}

func KeyV3SessionRunIntent(sessionID, runID string) string {
	return fmt.Sprintf("v3/session_run_intent/%s/%s", keyPart(sessionID), keyPart(runID))
}

func KeyV3SessionRunIntentActive(sessionID string) string {
	return fmt.Sprintf("v3/session_run_intent_active/%s", keyPart(sessionID))
}

func (s *SessionStore) ApplyV3SessionMutation(input V3SessionMutationInput) (V3SessionMutationResult, error) {
	if s == nil || s.store == nil {
		return V3SessionMutationResult{}, errors.New("session store is not configured")
	}
	input = normalizeV3SessionMutationInput(input)
	if err := validateV3SessionMutationInput(input); err != nil {
		return V3SessionMutationResult{}, err
	}

	v3SessionMutationMu.Lock()
	defer v3SessionMutationMu.Unlock()

	idempotencyKey := KeyV3SessionOperationIdempotency(input.AccountScopeID, input.SessionID, input.Kind, input.ClientRequestID)
	if existing, ok, err := s.getV3SessionIdempotencyRecordByKey(idempotencyKey); err != nil {
		return V3SessionMutationResult{}, err
	} else if ok {
		result, resultErr := s.resultFromV3IdempotencyRecord(existing)
		if existing.PayloadHash != input.PayloadHash {
			if resultErr != nil {
				return V3SessionMutationResult{}, fmt.Errorf("%w: %v", ErrV3IdempotencyConflict, resultErr)
			}
			result.Conflict = &V3SessionMutationConflict{
				Code:                V3SessionMutationStatusConflict,
				Message:             "idempotency key was reused with a different payload hash",
				ExistingPayloadHash: existing.PayloadHash,
				IncomingPayloadHash: input.PayloadHash,
			}
			result.ResponseStatus = V3SessionMutationStatusConflict
			return result, ErrV3IdempotencyConflict
		}
		if resultErr != nil {
			return V3SessionMutationResult{}, resultErr
		}
		result.Replayed = true
		return result, nil
	}

	return s.applyFreshV3SessionMutation(input, idempotencyKey)
}

func (s *SessionStore) applyFreshV3SessionMutation(input V3SessionMutationInput, idempotencyStoreKey string) (V3SessionMutationResult, error) {
	currentSeq, err := s.readV3SessionSequence(input.SessionID)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	seq := currentSeq + 1
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	session, sessionProvided, err := s.prepareV3SessionForMutation(input, seq, now)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	message, messageProvided, err := prepareV3MessageForMutation(input, session, seq, now)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	lifecycle, lifecycleProvided := prepareV3LifecycleForMutation(input, seq, now)
	runIntent, runIntentProvided := prepareV3RunIntentForMutation(input, seq, now)
	payload, err := input.v3EventPayload(seq, message, runIntent)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	event := V3SessionEvent{
		ID:            strings.TrimSpace(input.EventID),
		SessionID:     input.SessionID,
		Seq:           seq,
		EventType:     normalizeV3SessionEventType(input),
		Payload:       payload,
		TsUnixMs:      now,
		CausationID:   input.CausationID,
		CorrelationID: input.CorrelationID,
	}
	if event.ID == "" {
		event.ID = fmt.Sprintf("v3evt_%s_%020d", input.SessionID, seq)
	}
	projection := V3SessionProjection{SessionID: input.SessionID, LastEventSeq: seq, ProjectionHighWatermarkSeq: seq, UpdatedAt: now}
	storedResult := V3SessionMutationStoredResult{
		SessionID:                  input.SessionID,
		FirstSeq:                   seq,
		LastSeq:                    seq,
		EventIDs:                   []string{event.ID},
		PayloadHash:                input.PayloadHash,
		ResponseVersion:            V3SessionMutationResponseVersion,
		ResponseStatus:             V3SessionMutationStatusCompleted,
		EventType:                  event.EventType,
		LastEventSeq:               projection.LastEventSeq,
		ProjectionHighWatermarkSeq: projection.ProjectionHighWatermarkSeq,
		AppliedAt:                  now,
	}
	if messageProvided {
		storedResult.MessageID = message.ID
	}
	if runIntentProvided {
		storedResult.RunID = runIntent.RunID
	}
	idempotency := V3SessionIdempotencyRecord{
		SessionID:       input.SessionID,
		UserID:          input.UserID,
		AccountScopeID:  input.AccountScopeID,
		Operation:       input.Kind,
		ClientRequestID: input.ClientRequestID,
		Key:             input.IdempotencyKey,
		PayloadHash:     input.PayloadHash,
		RequestHash:     input.RequestHash,
		Kind:            input.Kind,
		Status:          V3SessionMutationStatusCompleted,
		Result:          storedResult,
		CreatedAt:       now,
		CompletedAt:     now,
	}

	eventPayload, err := json.Marshal(event)
	if err != nil {
		return V3SessionMutationResult{}, fmt.Errorf("marshal v3 session event: %w", err)
	}
	projectionPayload, err := json.Marshal(projection)
	if err != nil {
		return V3SessionMutationResult{}, fmt.Errorf("marshal v3 session projection: %w", err)
	}
	responseBody, err := buildV3MutationResponseBody(storedResult)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	storedResult.ResponseBody = responseBody
	idempotency.Result = storedResult
	idempotencyPayload, err := json.Marshal(idempotency)
	if err != nil {
		return V3SessionMutationResult{}, fmt.Errorf("marshal v3 session idempotency: %w", err)
	}

	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyV3SessionSequence(input.SessionID)), uint64ToBytes(seq), nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	if err := batch.Set([]byte(KeyV3SessionEvent(input.SessionID, seq)), eventPayload, nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	if err := batch.Set([]byte(KeyV3SessionProjection(input.SessionID)), projectionPayload, nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	if sessionProvided || messageProvided {
		if err := s.setSessionInBatch(batch, session); err != nil {
			return V3SessionMutationResult{}, err
		}
	}
	if messageProvided {
		messagePayload, err := json.Marshal(message)
		if err != nil {
			return V3SessionMutationResult{}, fmt.Errorf("marshal v3 message %q/%d: %w", message.SessionID, message.GlobalSeq, err)
		}
		if err := batch.Set([]byte(KeyV3SessionMessage(message.SessionID, seq)), messagePayload, nil); err != nil {
			return V3SessionMutationResult{}, err
		}
	}
	if lifecycleProvided {
		lifecyclePayload, err := json.Marshal(lifecycle)
		if err != nil {
			return V3SessionMutationResult{}, fmt.Errorf("marshal v3 lifecycle %q: %w", lifecycle.SessionID, err)
		}
		if err := batch.Set([]byte(KeySessionLifecycle(lifecycle.SessionID)), lifecyclePayload, nil); err != nil {
			return V3SessionMutationResult{}, err
		}
		if lifecycle.AccountScopeID != "" {
			if err := batch.Set([]byte(KeySessionLifecycleByAccount(lifecycle.AccountScopeID, lifecycle.SessionID)), []byte(lifecycle.SessionID), nil); err != nil {
				return V3SessionMutationResult{}, err
			}
		}
	}
	if runIntentProvided {
		runPayload, err := json.Marshal(runIntent)
		if err != nil {
			return V3SessionMutationResult{}, fmt.Errorf("marshal v3 run intent %q: %w", runIntent.RunID, err)
		}
		if err := batch.Set([]byte(KeyV3SessionRunIntent(runIntent.SessionID, runIntent.RunID)), runPayload, nil); err != nil {
			return V3SessionMutationResult{}, err
		}
		if err := batch.Set([]byte(KeyV3SessionRunIntentActive(runIntent.SessionID)), []byte(runIntent.RunID), nil); err != nil {
			return V3SessionMutationResult{}, err
		}
	}
	if err := batch.Set([]byte(idempotencyStoreKey), idempotencyPayload, nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return V3SessionMutationResult{}, err
	}

	result := V3SessionMutationResult{
		SessionID:       input.SessionID,
		PrimarySeq:      seq,
		FirstSeq:        storedResult.FirstSeq,
		LastSeq:         storedResult.LastSeq,
		EventIDs:        append([]string(nil), storedResult.EventIDs...),
		PayloadHash:     storedResult.PayloadHash,
		ResponseVersion: storedResult.ResponseVersion,
		ResponseStatus:  storedResult.ResponseStatus,
		ResponseBody:    append(json.RawMessage(nil), storedResult.ResponseBody...),
		Event:           event,
		Projection:      projection,
		Idempotency:     idempotency,
	}
	if sessionProvided || messageProvided {
		result.Session = &session
	}
	if messageProvided {
		result.Message = &message
	}
	if lifecycleProvided {
		result.Lifecycle = &lifecycle
	}
	if runIntentProvided {
		result.RunIntent = &runIntent
	}
	return result, nil
}

func (s *SessionStore) GetV3SessionEvent(sessionID string, seq uint64) (V3SessionEvent, bool, error) {
	var event V3SessionEvent
	if seq == 0 {
		return V3SessionEvent{}, false, errors.New("v3 session event seq is required")
	}
	ok, err := s.store.GetJSON(KeyV3SessionEvent(strings.TrimSpace(sessionID), seq), &event)
	if err != nil || !ok {
		return V3SessionEvent{}, ok, err
	}
	return event, true, nil
}

func (s *SessionStore) ListV3SessionEvents(sessionID string, afterSeq uint64, limit int) ([]V3SessionEvent, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]V3SessionEvent, 0, limit)
	err := s.store.IteratePrefix(V3SessionEventPrefix(sessionID), 100000, func(_ string, value []byte) error {
		var event V3SessionEvent
		if err := json.Unmarshal(value, &event); err != nil {
			return err
		}
		if event.Seq <= afterSeq {
			return nil
		}
		if len(out) < limit {
			out = append(out, event)
		}
		return nil
	})
	return out, err
}

func (s *SessionStore) GetV3SessionProjection(sessionID string) (V3SessionProjection, bool, error) {
	var projection V3SessionProjection
	ok, err := s.store.GetJSON(KeyV3SessionProjection(strings.TrimSpace(sessionID)), &projection)
	if err != nil || !ok {
		return V3SessionProjection{}, ok, err
	}
	return projection, true, nil
}

func (s *SessionStore) GetV3SessionIdempotencyRecord(accountScopeID, sessionID, idempotencyKey string) (V3SessionIdempotencyRecord, bool, error) {
	return s.getV3SessionIdempotencyRecordByKey(KeyV3SessionIdempotency(accountScopeID, sessionID, idempotencyKey))
}

func (s *SessionStore) GetV3SessionOperationIdempotencyRecord(accountScopeID, sessionID, operation, clientRequestID string) (V3SessionIdempotencyRecord, bool, error) {
	return s.getV3SessionIdempotencyRecordByKey(KeyV3SessionOperationIdempotency(accountScopeID, sessionID, operation, clientRequestID))
}

func (s *SessionStore) ListV3SessionMessages(sessionID string, afterSeq uint64, limit int) ([]MessageSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]MessageSnapshot, 0, limit)
	err := s.store.IteratePrefix(V3SessionMessagePrefix(sessionID), 100000, func(_ string, value []byte) error {
		var message MessageSnapshot
		if err := json.Unmarshal(value, &message); err != nil {
			return err
		}
		if message.GlobalSeq <= afterSeq {
			return nil
		}
		if len(out) < limit {
			message.Metadata = sanitizeMessageMetadata(message.Metadata)
			out = append(out, message)
		}
		return nil
	})
	return out, err
}

func (s *SessionStore) GetV3SessionRunIntent(sessionID, runID string) (V3SessionRunIntent, bool, error) {
	var intent V3SessionRunIntent
	ok, err := s.store.GetJSON(KeyV3SessionRunIntent(strings.TrimSpace(sessionID), strings.TrimSpace(runID)), &intent)
	if err != nil || !ok {
		return V3SessionRunIntent{}, ok, err
	}
	return intent, true, nil
}

func (s *SessionStore) readV3SessionSequence(sessionID string) (uint64, error) {
	raw, ok, err := s.store.GetBytes(KeyV3SessionSequence(sessionID))
	if err != nil || !ok {
		return 0, err
	}
	seq, err := bytesToUint64(raw)
	if err != nil {
		return 0, fmt.Errorf("decode v3 session sequence %q: %w", sessionID, err)
	}
	return seq, nil
}

func (s *SessionStore) getV3SessionIdempotencyRecordByKey(key string) (V3SessionIdempotencyRecord, bool, error) {
	var record V3SessionIdempotencyRecord
	ok, err := s.store.GetJSON(key, &record)
	if err != nil || !ok {
		return V3SessionIdempotencyRecord{}, ok, err
	}
	return record, true, nil
}

func (s *SessionStore) resultFromV3IdempotencyRecord(record V3SessionIdempotencyRecord) (V3SessionMutationResult, error) {
	primarySeq := record.Result.LastSeq
	if primarySeq == 0 {
		primarySeq = record.Result.FirstSeq
	}
	if primarySeq == 0 {
		return V3SessionMutationResult{}, errors.New("v3 idempotency record is missing sequence result")
	}
	result := V3SessionMutationResult{
		SessionID:       record.Result.SessionID,
		PrimarySeq:      primarySeq,
		FirstSeq:        record.Result.FirstSeq,
		LastSeq:         record.Result.LastSeq,
		EventIDs:        append([]string(nil), record.Result.EventIDs...),
		PayloadHash:     record.Result.PayloadHash,
		ResponseVersion: record.Result.ResponseVersion,
		ResponseStatus:  record.Result.ResponseStatus,
		ResponseBody:    append(json.RawMessage(nil), record.Result.ResponseBody...),
		Conflict:        cloneV3MutationConflict(record.Result.Conflict),
		Error:           cloneV3MutationError(record.Result.Error),
		Idempotency:     record,
	}
	if event, ok, err := s.GetV3SessionEvent(record.Result.SessionID, primarySeq); err != nil {
		return V3SessionMutationResult{}, err
	} else if ok {
		result.Event = event
	}
	result.Projection = V3SessionProjection{
		SessionID:                  record.Result.SessionID,
		LastEventSeq:               record.Result.LastEventSeq,
		ProjectionHighWatermarkSeq: record.Result.ProjectionHighWatermarkSeq,
		UpdatedAt:                  record.Result.AppliedAt,
	}
	if session, ok, err := s.GetSession(record.Result.SessionID); err != nil {
		return V3SessionMutationResult{}, err
	} else if ok {
		result.Session = &session
	}
	if record.Result.MessageID != "" {
		if messages, err := s.ListV3SessionMessages(record.Result.SessionID, primarySeq-1, 1); err != nil {
			return V3SessionMutationResult{}, err
		} else if len(messages) == 1 && messages[0].ID == record.Result.MessageID {
			result.Message = &messages[0]
		}
	}
	if record.Result.RunID != "" {
		if runIntent, ok, err := s.GetV3SessionRunIntent(record.Result.SessionID, record.Result.RunID); err != nil {
			return V3SessionMutationResult{}, err
		} else if ok {
			result.RunIntent = &runIntent
		}
	}
	return result, nil
}

func (s *SessionStore) prepareV3SessionForMutation(input V3SessionMutationInput, seq uint64, now int64) (SessionSnapshot, bool, error) {
	if input.Session != nil {
		session := normalizeSessionOwnership(*input.Session)
		session.ID = input.SessionID
		if session.UserID == "" {
			session.UserID = input.UserID
		}
		if session.AccountScopeID == "" {
			session.AccountScopeID = input.AccountScopeID
		}
		if session.CreatedAt == 0 {
			session.CreatedAt = now
		}
		if session.UpdatedAt == 0 {
			session.UpdatedAt = now
		}
		session.Metadata = cloneSessionMetadataMap(session.Metadata)
		return session, true, nil
	}
	if input.Message != nil {
		session, ok, err := s.GetSession(input.SessionID)
		if err != nil {
			return SessionSnapshot{}, false, err
		}
		if !ok {
			return SessionSnapshot{}, false, fmt.Errorf("session %q not found", input.SessionID)
		}
		session.MessageCount++
		session.UpdatedAt = now
		session.LastMessageAt = now
		return session, false, nil
	}
	return SessionSnapshot{}, false, nil
}

func (s *SessionStore) setSessionInBatch(batch *pebble.Batch, session SessionSnapshot) error {
	session = normalizeSessionOwnership(session)
	payload, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal session %q: %w", session.ID, err)
	}
	if existing, ok, err := s.GetSession(session.ID); err != nil {
		return err
	} else if ok && existing.AccountScopeID != "" && existing.AccountScopeID != session.AccountScopeID {
		if err := batch.Delete([]byte(KeySessionByAccount(existing.AccountScopeID, session.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
	}
	if err := batch.Set([]byte(KeySession(session.ID)), payload, nil); err != nil {
		return err
	}
	if session.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionByAccount(session.AccountScopeID, session.ID)), []byte(session.ID), nil); err != nil {
			return err
		}
	}
	return nil
}

func prepareV3MessageForMutation(input V3SessionMutationInput, session SessionSnapshot, seq uint64, now int64) (MessageSnapshot, bool, error) {
	if input.Message == nil {
		return MessageSnapshot{}, false, nil
	}
	message := sanitizeMessageSnapshot(*input.Message)
	message.SessionID = input.SessionID
	if message.UserID == "" {
		message.UserID = firstNonEmpty(input.UserID, session.UserID)
	}
	if message.AccountScopeID == "" {
		message.AccountScopeID = firstNonEmpty(input.AccountScopeID, session.AccountScopeID)
	}
	if strings.TrimSpace(message.Role) == "" {
		return MessageSnapshot{}, false, errors.New("message role is required")
	}
	if strings.TrimSpace(message.Content) == "" {
		return MessageSnapshot{}, false, errors.New("message content is required")
	}
	message.GlobalSeq = seq
	if strings.TrimSpace(message.ID) == "" {
		message.ID = fmt.Sprintf("v3msg_%s_%020d", input.SessionID, seq)
	}
	if message.CreatedAt == 0 {
		message.CreatedAt = now
	}
	return message, true, nil
}

func prepareV3LifecycleForMutation(input V3SessionMutationInput, seq uint64, now int64) (SessionLifecycleSnapshot, bool) {
	if input.Lifecycle == nil {
		return SessionLifecycleSnapshot{}, false
	}
	lifecycle := *input.Lifecycle
	lifecycle.SessionID = input.SessionID
	if lifecycle.UserID == "" {
		lifecycle.UserID = input.UserID
	}
	if lifecycle.AccountScopeID == "" {
		lifecycle.AccountScopeID = input.AccountScopeID
	}
	lifecycle.Phase = strings.TrimSpace(lifecycle.Phase)
	lifecycle.StopReason = strings.TrimSpace(lifecycle.StopReason)
	lifecycle.Error = strings.TrimSpace(lifecycle.Error)
	lifecycle.OwnerTransport = strings.TrimSpace(lifecycle.OwnerTransport)
	if lifecycle.UpdatedAt == 0 {
		lifecycle.UpdatedAt = now
	}
	if lifecycle.Generation == 0 {
		lifecycle.Generation = seq
	}
	return lifecycle, true
}

func prepareV3RunIntentForMutation(input V3SessionMutationInput, seq uint64, now int64) (V3SessionRunIntent, bool) {
	if input.RunIntent == nil {
		return V3SessionRunIntent{}, false
	}
	intent := *input.RunIntent
	intent.SessionID = input.SessionID
	if intent.UserID == "" {
		intent.UserID = input.UserID
	}
	if intent.AccountScopeID == "" {
		intent.AccountScopeID = input.AccountScopeID
	}
	intent.RunID = strings.TrimSpace(intent.RunID)
	if intent.RunID == "" {
		intent.RunID = fmt.Sprintf("v3run_%s_%020d", input.SessionID, seq)
	}
	intent.Status = strings.TrimSpace(intent.Status)
	if intent.Status == "" {
		intent.Status = V3RunIntentPendingExecutor
	}
	intent.BlockedReason = strings.TrimSpace(intent.BlockedReason)
	if intent.CreatedAt == 0 {
		intent.CreatedAt = now
	}
	if intent.UpdatedAt == 0 {
		intent.UpdatedAt = now
	}
	intent.EventSeq = seq
	return intent, true
}

func normalizeV3SessionMutationInput(input V3SessionMutationInput) V3SessionMutationInput {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.ClientRequestID = strings.TrimSpace(input.ClientRequestID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.PayloadHash = strings.TrimSpace(input.PayloadHash)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.Kind = strings.TrimSpace(input.Kind)
	input.EventID = strings.TrimSpace(input.EventID)
	input.EventType = strings.TrimSpace(input.EventType)
	input.CausationID = strings.TrimSpace(input.CausationID)
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	if input.ClientRequestID == "" {
		input.ClientRequestID = input.IdempotencyKey
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = input.ClientRequestID
	}
	if input.PayloadHash == "" {
		input.PayloadHash = input.RequestHash
	}
	if input.RequestHash == "" {
		input.RequestHash = input.PayloadHash
	}
	if input.SessionID == "" && input.Session != nil {
		input.SessionID = strings.TrimSpace(input.Session.ID)
	}
	if input.SessionID == "" && input.Message != nil {
		input.SessionID = strings.TrimSpace(input.Message.SessionID)
	}
	if input.SessionID == "" && input.Lifecycle != nil {
		input.SessionID = strings.TrimSpace(input.Lifecycle.SessionID)
	}
	if input.SessionID == "" && input.RunIntent != nil {
		input.SessionID = strings.TrimSpace(input.RunIntent.SessionID)
	}
	return input
}

func validateV3SessionMutationInput(input V3SessionMutationInput) error {
	if input.SessionID == "" {
		return errors.New("session id is required")
	}
	if input.ClientRequestID == "" {
		return errors.New("client request id is required")
	}
	if input.IdempotencyKey == "" {
		return errors.New("idempotency key is required")
	}
	if input.PayloadHash == "" {
		return errors.New("payload hash is required")
	}
	if input.Kind == "" {
		return errors.New("session mutation kind is required")
	}
	if len(input.EventPayload) > 0 && !json.Valid(input.EventPayload) {
		return errors.New("event payload must be valid json")
	}
	return nil
}

func buildV3MutationResponseBody(result V3SessionMutationStoredResult) (json.RawMessage, error) {
	body := struct {
		SessionID       string   `json:"session_id"`
		MessageID       string   `json:"message_id,omitempty"`
		RunID           string   `json:"run_id,omitempty"`
		FirstSeq        uint64   `json:"first_seq"`
		LastSeq         uint64   `json:"last_seq"`
		EventIDs        []string `json:"event_ids"`
		PayloadHash     string   `json:"payload_hash"`
		ResponseVersion string   `json:"response_version"`
		ResponseStatus  string   `json:"response_status"`
	}{
		SessionID:       result.SessionID,
		MessageID:       result.MessageID,
		RunID:           result.RunID,
		FirstSeq:        result.FirstSeq,
		LastSeq:         result.LastSeq,
		EventIDs:        append([]string(nil), result.EventIDs...),
		PayloadHash:     result.PayloadHash,
		ResponseVersion: result.ResponseVersion,
		ResponseStatus:  result.ResponseStatus,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal v3 mutation response body: %w", err)
	}
	return raw, nil
}

func cloneV3MutationConflict(in *V3SessionMutationConflict) *V3SessionMutationConflict {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneV3MutationError(in *V3SessionMutationError) *V3SessionMutationError {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func normalizeV3SessionEventType(input V3SessionMutationInput) string {
	if input.EventType != "" {
		return input.EventType
	}
	switch input.Kind {
	case V3SessionMutationCreateSession:
		return "session.created"
	case V3SessionMutationAppendMessage:
		return "session.message.appended"
	case V3SessionMutationUpsertLifecycle:
		return "session.lifecycle.updated"
	case V3SessionMutationRecordRunIntent:
		return "session.run_intent.recorded"
	default:
		return input.Kind
	}
}

func (input V3SessionMutationInput) v3EventPayload(seq uint64, message MessageSnapshot, runIntent V3SessionRunIntent) (json.RawMessage, error) {
	if len(input.EventPayload) > 0 {
		return append(json.RawMessage(nil), input.EventPayload...), nil
	}
	payload := map[string]any{
		"session_id": input.SessionID,
		"seq":        seq,
		"kind":       input.Kind,
	}
	if message.ID != "" {
		payload["message_id"] = message.ID
		payload["role"] = message.Role
	}
	if runIntent.RunID != "" {
		payload["run_id"] = runIntent.RunID
		payload["status"] = runIntent.Status
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
