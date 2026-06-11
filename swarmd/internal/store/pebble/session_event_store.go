package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	V3SessionMutationCreateSession    = "session.create"
	V3SessionMutationAppendMessage    = "message.append"
	V3SessionMutationUpsertLifecycle  = "lifecycle.upsert"
	V3SessionMutationRecordRunIntent  = "run_intent.record"
	V3SessionMutationRecordDiagnostic = "diagnostic.record"
	V3SessionMutationRecordUsage      = "usage.record"
	V3SessionMutationUpdateMode       = "session.mode.update"
	V3SessionMutationUpdatePreference = "session.preference.update"
	V3SessionMutationUpdateMetadata   = "session.metadata.update"
	V3SessionMutationUpdateTitle      = "session.title.update"

	V3SessionMutationResponseVersion = "v3.session_mutation.result.v1"
	V3SessionMutationStatusCompleted = "completed"
	V3SessionMutationStatusConflict  = "idempotency_conflict"

	V3RunIntentPendingExecutor = "pending_executor"
	V3RunIntentRunning         = "running"
	V3RunIntentCompleted       = "completed"
	V3RunIntentFailed          = "failed"
	V3RunIntentCancelled       = "cancelled"
	V3RunIntentExpired         = "expired"
	V3RunIntentInterrupted     = "interrupted"
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
	TurnUsage       *SessionTurnUsageSnapshot `json:"turn_usage,omitempty"`
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
	TurnUsage       *SessionTurnUsageSnapshot  `json:"turn_usage,omitempty"`
	UsageSummary    *SessionUsageSummary       `json:"usage_summary,omitempty"`
	Projection      V3SessionProjection        `json:"projection"`
	Idempotency     V3SessionIdempotencyRecord `json:"idempotency"`
	RealtimeOutbox  *V3RealtimeOutboxRecord    `json:"realtime_outbox,omitempty"`
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

type V3RealtimeOutboxRecord struct {
	EndpointSeq    uint64              `json:"endpoint_seq"`
	EndpointCursor string              `json:"endpoint_cursor"`
	SessionID      string              `json:"session_id"`
	UserID         string              `json:"user_id,omitempty"`
	AccountScopeID string              `json:"account_scope_id,omitempty"`
	Event          V3SessionEvent      `json:"event"`
	Projection     V3SessionProjection `json:"projection"`
	CreatedAt      int64               `json:"created_at"`
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

type V3SessionReplay struct {
	Session          *SessionSnapshot          `json:"session,omitempty"`
	Projection       V3SessionProjection       `json:"projection"`
	Lifecycle        *SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
	Messages         []MessageSnapshot         `json:"messages"`
	RunIntents       []V3SessionRunIntent      `json:"run_intents,omitempty"`
	Events           []V3SessionEvent          `json:"events"`
	HighWatermarkSeq uint64                    `json:"high_watermark_seq"`
	NextSeq          uint64                    `json:"next_seq"`
}

type V3SessionHydration struct {
	Session    SessionSnapshot     `json:"session"`
	Projection V3SessionProjection `json:"projection"`
	Messages   []MessageSnapshot   `json:"messages"`
	Events     []V3SessionEvent    `json:"events"`
}

type v3SessionEventReplayPayload struct {
	SessionID    string                    `json:"session_id,omitempty"`
	Seq          uint64                    `json:"seq,omitempty"`
	Kind         string                    `json:"kind,omitempty"`
	Session      *SessionSnapshot          `json:"session,omitempty"`
	Message      *MessageSnapshot          `json:"message,omitempty"`
	Lifecycle    *SessionLifecycleSnapshot `json:"lifecycle,omitempty"`
	RunIntent    *V3SessionRunIntent       `json:"run_intent,omitempty"`
	TurnUsage    *SessionTurnUsageSnapshot `json:"turn_usage,omitempty"`
	UsageSummary *SessionUsageSummary      `json:"usage_summary,omitempty"`
	MessageID    string                    `json:"message_id,omitempty"`
	Role         string                    `json:"role,omitempty"`
	RunID        string                    `json:"run_id,omitempty"`
	Status       string                    `json:"status,omitempty"`
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

func V3SessionRunIntentPrefix(sessionID string) string {
	return fmt.Sprintf("v3/session_run_intent/%s/", keyPart(sessionID))
}

func KeyV3SessionRunIntentActive(sessionID string) string {
	return fmt.Sprintf("v3/session_run_intent_active/%s", keyPart(sessionID))
}

func KeyV3SessionRunIntentStatus(status string, updatedAt int64, accountScopeID, sessionID, runID string) string {
	return fmt.Sprintf("v3/session_run_intent_status/%s/%020d/%s/%s/%s", keyPart(status), updatedAt, keyPart(accountScopeID), keyPart(sessionID), keyPart(runID))
}

func V3SessionRunIntentStatusPrefix(status string) string {
	part := keyPart(status)
	if part == "" {
		return "v3/session_run_intent_status/"
	}
	return fmt.Sprintf("v3/session_run_intent_status/%s/", part)
}

func KeyV3RealtimeOutboxSequence() string {
	return "v3/realtime_outbox_seq"
}

func KeyV3RealtimeOutbox(endpointSeq uint64) string {
	return fmt.Sprintf("v3/realtime_outbox/%020d", endpointSeq)
}

func V3RealtimeOutboxPrefix() string {
	return "v3/realtime_outbox/"
}

func V3RealtimeOutboxCursor(endpointSeq uint64) string {
	return fmt.Sprintf("cursor-%d", endpointSeq)
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
	currentOutboxSeq, err := s.readV3RealtimeOutboxSequence()
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	seq := currentSeq + 1
	endpointSeq := currentOutboxSeq + 1
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	lifecycle, lifecycleProvided := prepareV3LifecycleForMutation(input, seq, now)
	runIntent, runIntentProvided := prepareV3RunIntentForMutation(input, seq, now)
	if runIntentProvided {
		prepared, err := s.validateV3RunIntentTransition(input.SessionID, runIntent)
		if err != nil {
			return V3SessionMutationResult{}, err
		}
		runIntent = prepared
	}
	session, sessionProvided, err := s.prepareV3SessionForMutation(input, seq, now)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	message, messageProvided, err := s.prepareV3MessageForMutation(input, session, seq, now)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	turnUsage, usageSummary, usageProvided, err := s.prepareV3UsageForMutation(input, now)
	if err != nil {
		return V3SessionMutationResult{}, err
	}
	payload, err := input.v3EventPayload(seq, session, message, lifecycle, runIntent, turnUsage, usageSummary)
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
	if usageProvided {
		storedResult.RunID = turnUsage.RunID
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
	realtimeOutbox := V3RealtimeOutboxRecord{
		EndpointSeq:    endpointSeq,
		EndpointCursor: V3RealtimeOutboxCursor(endpointSeq),
		SessionID:      input.SessionID,
		UserID:         input.UserID,
		AccountScopeID: input.AccountScopeID,
		Event:          event,
		Projection:     projection,
		CreatedAt:      now,
	}
	realtimeOutboxPayload, err := json.Marshal(realtimeOutbox)
	if err != nil {
		return V3SessionMutationResult{}, fmt.Errorf("marshal v3 realtime outbox event: %w", err)
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
	if err := batch.Set([]byte(KeyV3RealtimeOutboxSequence()), uint64ToBytes(endpointSeq), nil); err != nil {
		return V3SessionMutationResult{}, err
	}
	if err := batch.Set([]byte(KeyV3RealtimeOutbox(endpointSeq)), realtimeOutboxPayload, nil); err != nil {
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
	if usageProvided {
		usagePayload, err := json.Marshal(turnUsage)
		if err != nil {
			return V3SessionMutationResult{}, fmt.Errorf("marshal v3 turn usage %q/%q: %w", turnUsage.SessionID, turnUsage.RunID, err)
		}
		if err := batch.Set([]byte(KeySessionTurnUsage(turnUsage.SessionID, turnUsage.RunID)), usagePayload, nil); err != nil {
			return V3SessionMutationResult{}, err
		}
		if turnUsage.AccountScopeID != "" {
			if err := batch.Set([]byte(KeySessionTurnUsageByAccount(turnUsage.AccountScopeID, turnUsage.SessionID, turnUsage.RunID)), []byte(turnUsage.RunID), nil); err != nil {
				return V3SessionMutationResult{}, err
			}
		}
		summaryPayload, err := json.Marshal(usageSummary)
		if err != nil {
			return V3SessionMutationResult{}, fmt.Errorf("marshal v3 usage summary %q: %w", usageSummary.SessionID, err)
		}
		if err := batch.Set([]byte(KeySessionUsageSummary(usageSummary.SessionID)), summaryPayload, nil); err != nil {
			return V3SessionMutationResult{}, err
		}
		if usageSummary.AccountScopeID != "" {
			if err := batch.Set([]byte(KeySessionUsageSummaryByAccount(usageSummary.AccountScopeID, usageSummary.SessionID)), summaryPayload, nil); err != nil {
				return V3SessionMutationResult{}, err
			}
		}
	}
	if runIntentProvided {
		previousRunIntent, previousRunIntentOK, err := s.GetV3SessionRunIntent(runIntent.SessionID, runIntent.RunID)
		if err != nil {
			return V3SessionMutationResult{}, err
		}
		runPayload, err := json.Marshal(runIntent)
		if err != nil {
			return V3SessionMutationResult{}, fmt.Errorf("marshal v3 run intent %q: %w", runIntent.RunID, err)
		}
		if err := batch.Set([]byte(KeyV3SessionRunIntent(runIntent.SessionID, runIntent.RunID)), runPayload, nil); err != nil {
			return V3SessionMutationResult{}, err
		}
		if previousRunIntentOK {
			previousStatusKey := KeyV3SessionRunIntentStatus(previousRunIntent.Status, previousRunIntent.UpdatedAt, previousRunIntent.AccountScopeID, previousRunIntent.SessionID, previousRunIntent.RunID)
			if err := batch.Delete([]byte(previousStatusKey), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return V3SessionMutationResult{}, err
			}
		}
		statusKey := KeyV3SessionRunIntentStatus(runIntent.Status, runIntent.UpdatedAt, runIntent.AccountScopeID, runIntent.SessionID, runIntent.RunID)
		if err := batch.Set([]byte(statusKey), runPayload, nil); err != nil {
			return V3SessionMutationResult{}, err
		}
		activeKey := KeyV3SessionRunIntentActive(runIntent.SessionID)
		if isV3RunIntentTerminal(runIntent.Status) {
			if err := batch.Delete([]byte(activeKey), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return V3SessionMutationResult{}, err
			}
		} else if runIntent.Status == V3RunIntentRunning {
			if err := batch.Set([]byte(activeKey), []byte(runIntent.RunID), nil); err != nil {
				return V3SessionMutationResult{}, err
			}
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
		RealtimeOutbox:  &realtimeOutbox,
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
	if usageProvided {
		result.TurnUsage = &turnUsage
		result.UsageSummary = &usageSummary
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
	return listV3SessionEventsFromReader(s.store.db, sessionID, afterSeq, limit)
}

func listV3SessionEventsFromReader(reader pebble.Reader, sessionID string, afterSeq uint64, limit int) ([]V3SessionEvent, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	if afterSeq == ^uint64(0) {
		return []V3SessionEvent{}, nil
	}
	out := make([]V3SessionEvent, 0, limit)
	prefix := V3SessionEventPrefix(sessionID)
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: KeyV3SessionEvent(sessionID, afterSeq+1), Limit: limit}, func(_ string, value []byte) (bool, error) {
		var event V3SessionEvent
		if err := json.Unmarshal(value, &event); err != nil {
			return false, err
		}
		if event.Seq <= afterSeq {
			return true, nil
		}
		out = append(out, event)
		return len(out) < limit, nil
	})
	return out, err
}

func (s *SessionStore) ReplayV3SessionEvents(sessionID string, afterSeq uint64, limit int) (V3SessionReplay, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return V3SessionReplay{}, errors.New("session id is required")
	}
	events, err := listV3SessionEventsFromReader(s.store.db, sessionID, afterSeq, limit)
	if err != nil {
		return V3SessionReplay{}, err
	}
	replay := V3SessionReplay{
		Events:   append([]V3SessionEvent(nil), events...),
		Messages: []MessageSnapshot{},
		NextSeq:  afterSeq,
	}
	seenMessages := map[string]bool{}
	seenRunIntents := map[string]bool{}
	expectedSeq := afterSeq + 1
	for _, event := range events {
		if event.SessionID != sessionID {
			return V3SessionReplay{}, fmt.Errorf("v3 session event %q belongs to session %q, want %q", event.ID, event.SessionID, sessionID)
		}
		if event.Seq == 0 {
			return V3SessionReplay{}, fmt.Errorf("v3 session event %q has zero sequence", event.ID)
		}
		if event.Seq != expectedSeq {
			return V3SessionReplay{}, fmt.Errorf("v3 session event sequence gap at %d, want %d", event.Seq, expectedSeq)
		}
		expectedSeq++
		var payload v3SessionEventReplayPayload
		if len(event.Payload) > 0 {
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return V3SessionReplay{}, fmt.Errorf("decode v3 session event %d payload: %w", event.Seq, err)
			}
		}
		if payload.Session != nil {
			session := normalizeSessionOwnership(*payload.Session)
			session.ID = sessionID
			session.Metadata = cloneSessionMetadataMap(session.Metadata)
			replay.Session = &session
		}
		if payload.Lifecycle != nil {
			lifecycle := *payload.Lifecycle
			lifecycle.SessionID = sessionID
			replay.Lifecycle = &lifecycle
			if replay.Session != nil {
				replay.Session.Lifecycle = &lifecycle
			}
		}
		if payload.Message != nil && payload.Message.ID != "" && !seenMessages[payload.Message.ID] {
			message := sanitizeMessageSnapshot(*payload.Message)
			message.SessionID = sessionID
			if message.GlobalSeq == 0 {
				message.GlobalSeq = event.Seq
			}
			replay.Messages = append(replay.Messages, message)
			seenMessages[message.ID] = true
		}
		if payload.RunIntent != nil && payload.RunIntent.RunID != "" && !seenRunIntents[payload.RunIntent.RunID] {
			intent := *payload.RunIntent
			intent.SessionID = sessionID
			if intent.EventSeq == 0 {
				intent.EventSeq = event.Seq
			}
			replay.RunIntents = append(replay.RunIntents, intent)
			seenRunIntents[intent.RunID] = true
		}
		replay.HighWatermarkSeq = event.Seq
	}
	if stored, ok, err := s.getSessionFromReader(s.store.db, sessionID); err != nil {
		return V3SessionReplay{}, err
	} else if ok && replay.Session == nil {
		stored.Metadata = cloneSessionMetadataMap(stored.Metadata)
		replay.Session = &stored
	}
	if replay.Lifecycle == nil {
		if lifecycle, ok, err := getSessionLifecycleFromReader(s.store.db, sessionID); err != nil {
			return V3SessionReplay{}, err
		} else if ok {
			replay.Lifecycle = &lifecycle
			if replay.Session != nil {
				replay.Session.Lifecycle = &lifecycle
			}
		}
	}
	if replay.HighWatermarkSeq > afterSeq {
		messages, err := listV3SessionMessagesFromReader(s.store.db, sessionID, afterSeq, limit)
		if err != nil {
			return V3SessionReplay{}, err
		}
		for _, message := range messages {
			if message.GlobalSeq > replay.HighWatermarkSeq {
				continue
			}
			if message.ID != "" && !seenMessages[message.ID] {
				replay.Messages = append(replay.Messages, message)
				seenMessages[message.ID] = true
			}
		}
		intents, err := s.ListV3SessionRunIntents(sessionID, afterSeq, limit)
		if err != nil {
			return V3SessionReplay{}, err
		}
		for _, intent := range intents {
			if intent.EventSeq > replay.HighWatermarkSeq {
				continue
			}
			if intent.RunID != "" && !seenRunIntents[intent.RunID] {
				replay.RunIntents = append(replay.RunIntents, intent)
				seenRunIntents[intent.RunID] = true
			}
		}
	}
	sort.SliceStable(replay.Messages, func(i, j int) bool { return replay.Messages[i].GlobalSeq < replay.Messages[j].GlobalSeq })
	sort.SliceStable(replay.RunIntents, func(i, j int) bool { return replay.RunIntents[i].EventSeq < replay.RunIntents[j].EventSeq })
	if projection, ok, err := getV3SessionProjectionFromReader(s.store.db, sessionID); err != nil {
		return V3SessionReplay{}, err
	} else if ok {
		replay.Projection = projection
	} else if replay.HighWatermarkSeq > 0 {
		replay.Projection = V3SessionProjection{SessionID: sessionID, LastEventSeq: replay.HighWatermarkSeq, ProjectionHighWatermarkSeq: replay.HighWatermarkSeq}
	}
	if replay.NextSeq == 0 && replay.HighWatermarkSeq == 0 {
		replay.NextSeq = afterSeq
	} else if replay.HighWatermarkSeq > 0 {
		replay.NextSeq = replay.HighWatermarkSeq
	}
	if replay.Projection.ProjectionHighWatermarkSeq < replay.HighWatermarkSeq {
		return V3SessionReplay{}, fmt.Errorf("v3 session projection high watermark %d is behind replay high watermark %d", replay.Projection.ProjectionHighWatermarkSeq, replay.HighWatermarkSeq)
	}
	return replay, nil
}

func (s *SessionStore) GetV3SessionProjection(sessionID string) (V3SessionProjection, bool, error) {
	return getV3SessionProjectionFromReader(s.store.db, sessionID)
}

func getV3SessionProjectionFromReader(reader pebble.Reader, sessionID string) (V3SessionProjection, bool, error) {
	var projection V3SessionProjection
	ok, err := getJSONFromReader(reader, KeyV3SessionProjection(strings.TrimSpace(sessionID)), &projection)
	if err != nil || !ok {
		return V3SessionProjection{}, ok, err
	}
	return projection, true, nil
}

func (s *SessionStore) HydrateV3SessionSnapshot(sessionID string, messageLimit, eventLimit int) (hydration V3SessionHydration, found bool, err error) {
	snapshot := s.store.db.NewSnapshot()
	defer func() {
		if closeErr := snapshot.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return s.hydrateV3SessionSnapshotFromReader(snapshot, sessionID, messageLimit, eventLimit)
}

func (s *SessionStore) hydrateV3SessionSnapshotFromReader(reader pebble.Reader, sessionID string, messageLimit, eventLimit int) (V3SessionHydration, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return V3SessionHydration{}, false, errors.New("session id is required")
	}
	session, ok, err := s.getSessionFromReader(reader, sessionID)
	if err != nil || !ok {
		return V3SessionHydration{}, ok, err
	}
	projection, projectionOK, err := getV3SessionProjectionFromReader(reader, sessionID)
	if err != nil || !projectionOK {
		return V3SessionHydration{}, projectionOK, err
	}
	messages := []MessageSnapshot{}
	if messageLimit > 0 {
		messages, err = listV3SessionMessageTailFromReader(reader, sessionID, messageLimit)
		if err != nil {
			return V3SessionHydration{}, false, err
		}
	}
	events := []V3SessionEvent{}
	if eventLimit > 0 {
		events, err = listV3SessionEventsFromReader(reader, sessionID, 0, eventLimit)
		if err != nil {
			return V3SessionHydration{}, false, err
		}
	}
	return V3SessionHydration{Session: session, Projection: projection, Messages: messages, Events: events}, true, nil
}

func (s *SessionStore) GetV3SessionIdempotencyRecord(accountScopeID, sessionID, idempotencyKey string) (V3SessionIdempotencyRecord, bool, error) {
	return s.getV3SessionIdempotencyRecordByKey(KeyV3SessionIdempotency(accountScopeID, sessionID, idempotencyKey))
}

func (s *SessionStore) GetV3SessionOperationIdempotencyRecord(accountScopeID, sessionID, operation, clientRequestID string) (V3SessionIdempotencyRecord, bool, error) {
	return s.getV3SessionIdempotencyRecordByKey(KeyV3SessionOperationIdempotency(accountScopeID, sessionID, operation, clientRequestID))
}

func (s *SessionStore) ListV3SessionMessages(sessionID string, afterSeq uint64, limit int) ([]MessageSnapshot, error) {
	return listV3SessionMessagesFromReader(s.store.db, sessionID, afterSeq, limit)
}

func (s *SessionStore) ListV3SessionMessageTail(sessionID string, limit int) ([]MessageSnapshot, error) {
	return listV3SessionMessageTailFromReader(s.store.db, sessionID, limit)
}

func (s *SessionStore) ListV3SessionMessagesBefore(sessionID string, beforeSeq uint64, limit int) ([]MessageSnapshot, error) {
	return listV3SessionMessagesBeforeFromReader(s.store.db, sessionID, beforeSeq, limit)
}

func listV3SessionMessagesFromReader(reader pebble.Reader, sessionID string, afterSeq uint64, limit int) ([]MessageSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	if afterSeq == ^uint64(0) {
		return []MessageSnapshot{}, nil
	}
	out := make([]MessageSnapshot, 0, limit)
	prefix := V3SessionMessagePrefix(sessionID)
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: KeyV3SessionMessage(sessionID, afterSeq+1), Limit: limit}, func(_ string, value []byte) (bool, error) {
		var message MessageSnapshot
		if err := json.Unmarshal(value, &message); err != nil {
			return false, err
		}
		if message.GlobalSeq <= afterSeq {
			return true, nil
		}
		message.Metadata = sanitizeMessageMetadata(message.Metadata)
		out = append(out, message)
		return len(out) < limit, nil
	})
	return out, err
}

func listV3SessionMessageTailFromReader(reader pebble.Reader, sessionID string, limit int) ([]MessageSnapshot, error) {
	return listV3SessionMessagesBeforeFromReader(reader, sessionID, 0, limit)
}

func listV3SessionMessagesBeforeFromReader(reader pebble.Reader, sessionID string, beforeSeq uint64, limit int) ([]MessageSnapshot, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]MessageSnapshot, 0, limit)
	prefix := V3SessionMessagePrefix(sessionID)
	startKey := ""
	if beforeSeq > 0 {
		startKey = KeyV3SessionMessage(sessionID, beforeSeq)
	}
	err := scanRangeFromReader(reader, scanRangeOptions{Prefix: prefix, StartKey: startKey, Limit: limit, Reverse: true}, func(_ string, value []byte) (bool, error) {
		var message MessageSnapshot
		if err := json.Unmarshal(value, &message); err != nil {
			return false, err
		}
		if beforeSeq > 0 && message.GlobalSeq >= beforeSeq {
			return true, nil
		}
		message.Metadata = sanitizeMessageMetadata(message.Metadata)
		out = append(out, message)
		return len(out) < limit, nil
	})
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *SessionStore) GetV3SessionRunIntent(sessionID, runID string) (V3SessionRunIntent, bool, error) {
	var intent V3SessionRunIntent
	ok, err := s.store.GetJSON(KeyV3SessionRunIntent(strings.TrimSpace(sessionID), strings.TrimSpace(runID)), &intent)
	if err != nil || !ok {
		return V3SessionRunIntent{}, ok, err
	}
	return intent, true, nil
}

func (s *SessionStore) GetV3SessionActiveRunIntent(sessionID string) (V3SessionRunIntent, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return V3SessionRunIntent{}, false, errors.New("session id is required")
	}
	raw, ok, err := s.store.GetBytes(KeyV3SessionRunIntentActive(sessionID))
	if err != nil || !ok {
		return V3SessionRunIntent{}, ok, err
	}
	runID := strings.TrimSpace(string(raw))
	if runID == "" {
		return V3SessionRunIntent{}, false, nil
	}
	return s.GetV3SessionRunIntent(sessionID, runID)
}

func (s *SessionStore) ListV3SessionRunIntents(sessionID string, afterSeq uint64, limit int) ([]V3SessionRunIntent, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]V3SessionRunIntent, 0, limit)
	err := s.store.IteratePrefix(V3SessionRunIntentPrefix(sessionID), 100000, func(_ string, value []byte) error {
		if len(out) >= limit {
			return nil
		}
		var intent V3SessionRunIntent
		if err := json.Unmarshal(value, &intent); err != nil {
			return err
		}
		if intent.EventSeq <= afterSeq {
			return nil
		}
		out = append(out, intent)
		return nil
	})
	return out, err
}

func (s *SessionStore) ListV3SessionRunIntentsByStatus(status string, limit int) ([]V3SessionRunIntent, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return nil, errors.New("run intent status is required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]V3SessionRunIntent, 0, limit)
	err := s.store.IteratePrefix(V3SessionRunIntentStatusPrefix(status), 100000, func(_ string, value []byte) error {
		if len(out) >= limit {
			return nil
		}
		var intent V3SessionRunIntent
		if err := json.Unmarshal(value, &intent); err != nil {
			return err
		}
		if strings.TrimSpace(intent.Status) != status {
			return nil
		}
		out = append(out, intent)
		return nil
	})
	return out, err
}

func (s *SessionStore) ListV3SessionRecoverableRunIntents(staleRunningBeforeUnixMs int64, limit int) ([]V3SessionRunIntent, error) {
	if limit <= 0 {
		limit = 500
	}
	out := make([]V3SessionRunIntent, 0, limit)
	appendStatus := func(status string, include func(V3SessionRunIntent) bool) error {
		if len(out) >= limit {
			return nil
		}
		intents, err := s.ListV3SessionRunIntentsByStatus(status, limit-len(out))
		if err != nil {
			return err
		}
		for _, intent := range intents {
			if len(out) >= limit {
				break
			}
			if include == nil || include(intent) {
				out = append(out, intent)
			}
		}
		return nil
	}
	if err := appendStatus(V3RunIntentPendingExecutor, nil); err != nil {
		return nil, err
	}
	if staleRunningBeforeUnixMs > 0 {
		if err := appendStatus(V3RunIntentRunning, func(intent V3SessionRunIntent) bool {
			return intent.UpdatedAt > 0 && intent.UpdatedAt < staleRunningBeforeUnixMs
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SessionStore) GetV3RealtimeOutbox(endpointSeq uint64) (V3RealtimeOutboxRecord, bool, error) {
	var record V3RealtimeOutboxRecord
	if endpointSeq == 0 {
		return V3RealtimeOutboxRecord{}, false, errors.New("v3 realtime endpoint seq is required")
	}
	ok, err := s.store.GetJSON(KeyV3RealtimeOutbox(endpointSeq), &record)
	if err != nil || !ok {
		return V3RealtimeOutboxRecord{}, ok, err
	}
	return record, true, nil
}

func (s *SessionStore) ListV3RealtimeOutboxAfter(afterEndpointSeq uint64, limit int) ([]V3RealtimeOutboxRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	if afterEndpointSeq == ^uint64(0) {
		return []V3RealtimeOutboxRecord{}, nil
	}
	out := make([]V3RealtimeOutboxRecord, 0, limit)
	prefix := V3RealtimeOutboxPrefix()
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: prefix, StartKey: KeyV3RealtimeOutbox(afterEndpointSeq + 1), Limit: limit}, func(_ string, value []byte) (bool, error) {
		var record V3RealtimeOutboxRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return false, err
		}
		if record.EndpointSeq <= afterEndpointSeq {
			return true, nil
		}
		out = append(out, record)
		return len(out) < limit, nil
	})
	return out, err
}

func (s *SessionStore) ListV3RealtimeOutboxForSessionAfterSeq(sessionID string, afterSeq uint64, limit int) ([]V3RealtimeOutboxRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	records, err := s.ListV3RealtimeOutboxAfter(0, 100000)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]V3RealtimeOutboxRecord, 0, limit)
	for _, record := range records {
		if len(out) >= limit {
			break
		}
		if record.SessionID != sessionID || record.Event.Seq <= afterSeq {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *SessionStore) readV3RealtimeOutboxSequence() (uint64, error) {
	raw, ok, err := s.store.GetBytes(KeyV3RealtimeOutboxSequence())
	if err != nil || !ok {
		return 0, err
	}
	seq, err := bytesToUint64(raw)
	if err != nil {
		return 0, fmt.Errorf("decode v3 realtime outbox sequence: %w", err)
	}
	return seq, nil
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

func (s *SessionStore) validateV3RunIntentTransition(sessionID string, incoming V3SessionRunIntent) (V3SessionRunIntent, error) {
	status := strings.TrimSpace(incoming.Status)
	if status == "" {
		status = V3RunIntentPendingExecutor
	}
	existing, ok, err := s.GetV3SessionRunIntent(sessionID, incoming.RunID)
	if err != nil {
		return V3SessionRunIntent{}, err
	}
	if ok {
		if existing.CreatedAt != 0 {
			incoming.CreatedAt = existing.CreatedAt
		} else if incoming.CreatedAt == 0 {
			incoming.CreatedAt = existing.UpdatedAt
		}
		switch status {
		case V3RunIntentPendingExecutor:
			if existing.Status != V3RunIntentRunning {
				return V3SessionRunIntent{}, fmt.Errorf("v3 run %q is %s, cannot reset pending", incoming.RunID, existing.Status)
			}
		case V3RunIntentRunning:
			if existing.Status != V3RunIntentPendingExecutor && existing.Status != V3RunIntentRunning {
				return V3SessionRunIntent{}, fmt.Errorf("v3 run %q is %s, cannot claim or update", incoming.RunID, existing.Status)
			}
		case V3RunIntentCompleted, V3RunIntentFailed, V3RunIntentCancelled, V3RunIntentExpired, V3RunIntentInterrupted:
			if existing.Status != V3RunIntentRunning && existing.Status != V3RunIntentPendingExecutor {
				return V3SessionRunIntent{}, fmt.Errorf("v3 run %q is %s, cannot terminate", incoming.RunID, existing.Status)
			}
		}
	} else if status != V3RunIntentPendingExecutor && status != V3RunIntentDispatchBlocked {
		return V3SessionRunIntent{}, fmt.Errorf("v3 run %q is missing pending intent", incoming.RunID)
	}
	if active, activeOK, err := s.GetV3SessionActiveRunIntent(sessionID); err != nil {
		return V3SessionRunIntent{}, err
	} else if activeOK && active.RunID != incoming.RunID && status == V3RunIntentRunning {
		return V3SessionRunIntent{}, fmt.Errorf("session %q already has active v3 run %q", sessionID, active.RunID)
	}
	return incoming, nil
}

func isV3RunIntentTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case V3RunIntentCompleted, V3RunIntentFailed, V3RunIntentCancelled, V3RunIntentExpired, V3RunIntentInterrupted, V3RunIntentDispatchBlocked:
		return true
	default:
		return false
	}
}

func (s *SessionStore) prepareV3UsageForMutation(input V3SessionMutationInput, now int64) (SessionTurnUsageSnapshot, SessionUsageSummary, bool, error) {
	if input.TurnUsage == nil {
		return SessionTurnUsageSnapshot{}, SessionUsageSummary{}, false, nil
	}
	usage := sanitizeTurnUsageSnapshot(*input.TurnUsage)
	usage.SessionID = input.SessionID
	if usage.RunID == "" {
		return SessionTurnUsageSnapshot{}, SessionUsageSummary{}, false, errors.New("turn usage run id is required")
	}
	session, ok, err := s.GetSession(input.SessionID)
	if err != nil {
		return SessionTurnUsageSnapshot{}, SessionUsageSummary{}, false, err
	}
	if !ok {
		return SessionTurnUsageSnapshot{}, SessionUsageSummary{}, false, fmt.Errorf("session %q not found", input.SessionID)
	}
	previous, hadPrevious, err := s.GetTurnUsage(input.SessionID, usage.RunID)
	if err != nil {
		return SessionTurnUsageSnapshot{}, SessionUsageSummary{}, false, err
	}
	summary, hasSummary, err := s.GetUsageSummary(input.SessionID)
	if err != nil {
		return SessionTurnUsageSnapshot{}, SessionUsageSummary{}, false, err
	}
	if !hasSummary {
		summary = SessionUsageSummary{SessionID: input.SessionID}
	}
	if hadPrevious {
		summary = applyUsageDelta(summary, previous, -1)
	} else {
		summary.TurnCount++
	}
	usage.UserID = firstNonEmpty(usage.UserID, input.UserID, session.UserID)
	usage.AccountScopeID = firstNonEmpty(usage.AccountScopeID, input.AccountScopeID, session.AccountScopeID)
	if usage.CreatedAt <= 0 {
		if hadPrevious && previous.CreatedAt > 0 {
			usage.CreatedAt = previous.CreatedAt
		} else {
			usage.CreatedAt = now
		}
	}
	usage.UpdatedAt = now
	if usage.ContextWindow > 0 {
		summary.ContextWindow = usage.ContextWindow
	} else if summary.ContextWindow > 0 {
		usage.ContextWindow = summary.ContextWindow
	}
	summary = applyUsageDelta(summary, usage, 1)
	summary.SessionID = input.SessionID
	summary.UserID = firstNonEmpty(summary.UserID, usage.UserID)
	summary.AccountScopeID = firstNonEmpty(summary.AccountScopeID, usage.AccountScopeID)
	if usage.Provider != "" {
		summary.Provider = usage.Provider
	}
	if usage.Model != "" {
		summary.Model = usage.Model
	}
	if usage.Source != "" {
		summary.Source = usage.Source
	}
	summary.LastTransport = strings.ToLower(strings.TrimSpace(usage.Transport))
	if usage.ConnectedViaWS != nil {
		connected := *usage.ConnectedViaWS
		summary.LastConnectedViaWS = &connected
	} else {
		summary.LastConnectedViaWS = nil
	}
	summary.LastRunID = usage.RunID
	summary.UpdatedAt = now
	if summary.ContextWindow > 0 {
		used := usage.TotalTokens
		if used <= 0 && usage.InputTokens > 0 {
			used = usage.InputTokens
		}
		if used <= 0 {
			used = summary.TotalTokens
		}
		if used < 0 {
			used = 0
		}
		remaining := int64(summary.ContextWindow) - used
		if remaining < 0 {
			remaining = 0
		}
		summary.RemainingTokens = remaining
	} else {
		summary.RemainingTokens = 0
	}
	return usage, summary, true, nil
}

func applyUsageDelta(summary SessionUsageSummary, usage SessionTurnUsageSnapshot, sign int64) SessionUsageSummary {
	summary.InputTokens += sign * usage.InputTokens
	summary.OutputTokens += sign * usage.OutputTokens
	summary.ThinkingTokens += sign * usage.ThinkingTokens
	summary.CacheReadTokens += sign * usage.CacheReadTokens
	summary.CacheWriteTokens += sign * usage.CacheWriteTokens
	summary.TotalTokens += sign * usage.TotalTokens
	return summary
}

func (s *SessionStore) prepareV3SessionForMutation(input V3SessionMutationInput, seq uint64, now int64) (SessionSnapshot, bool, error) {
	if input.Session != nil {
		if input.Kind == V3SessionMutationCreateSession && seq > 1 {
			return SessionSnapshot{}, false, fmt.Errorf("session %q already exists", input.SessionID)
		}
		session := normalizeSessionOwnership(*input.Session)
		session.ID = input.SessionID
		if input.Kind != V3SessionMutationCreateSession {
			current, ok, err := s.GetSession(input.SessionID)
			if err != nil {
				return SessionSnapshot{}, false, err
			}
			if !ok {
				return SessionSnapshot{}, false, fmt.Errorf("session %q not found", input.SessionID)
			}
			if session.UserID == "" {
				session.UserID = current.UserID
			}
			if session.AccountScopeID == "" {
				session.AccountScopeID = current.AccountScopeID
			}
			if session.WorkspacePath == "" {
				session.WorkspacePath = current.WorkspacePath
			}
			if session.WorkspaceName == "" {
				session.WorkspaceName = current.WorkspaceName
			}
			if session.Title == "" {
				session.Title = current.Title
			}
			if session.Mode == "" {
				session.Mode = current.Mode
			}
			if session.CreatedAt == 0 {
				session.CreatedAt = current.CreatedAt
			}
			if input.Kind != V3SessionMutationUpdateMetadata && len(session.Metadata) == 0 && len(current.Metadata) > 0 {
				session.Metadata = cloneSessionMetadataMap(current.Metadata)
			}
		}
		if session.UserID == "" {
			session.UserID = input.UserID
		}
		if session.AccountScopeID == "" {
			session.AccountScopeID = input.AccountScopeID
		}
		if session.CreatedAt == 0 {
			session.CreatedAt = now
		}
		session.UpdatedAt = now
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

func (s *SessionStore) prepareV3MessageForMutation(input V3SessionMutationInput, session SessionSnapshot, seq uint64, now int64) (MessageSnapshot, bool, error) {
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
	case V3SessionMutationRecordDiagnostic:
		return "session.diagnostic"
	case V3SessionMutationRecordUsage:
		return "run.usage.updated"
	case V3SessionMutationUpdateMode:
		return "session.mode.updated"
	case V3SessionMutationUpdatePreference:
		return "session.preference.updated"
	case V3SessionMutationUpdateMetadata:
		return "session.metadata.updated"
	case V3SessionMutationUpdateTitle:
		return "session.title.updated"
	default:
		return input.Kind
	}
}

func (input V3SessionMutationInput) v3EventPayload(seq uint64, session SessionSnapshot, message MessageSnapshot, lifecycle SessionLifecycleSnapshot, runIntent V3SessionRunIntent, turnUsage SessionTurnUsageSnapshot, usageSummary SessionUsageSummary) (json.RawMessage, error) {
	if len(input.EventPayload) > 0 {
		return append(json.RawMessage(nil), input.EventPayload...), nil
	}
	payload := v3SessionEventReplayPayload{
		SessionID: input.SessionID,
		Seq:       seq,
		Kind:      input.Kind,
	}
	if session.ID != "" {
		snapshot := session
		snapshot.Metadata = cloneSessionMetadataMap(snapshot.Metadata)
		payload.Session = &snapshot
	}
	if message.ID != "" {
		msg := sanitizeMessageSnapshot(message)
		payload.Message = &msg
		payload.MessageID = msg.ID
		payload.Role = msg.Role
	}
	if lifecycle.SessionID != "" {
		copy := lifecycle
		payload.Lifecycle = &copy
	}
	if runIntent.RunID != "" {
		intent := runIntent
		payload.RunIntent = &intent
		payload.RunID = intent.RunID
		payload.Status = intent.Status
	}
	if turnUsage.RunID != "" {
		usage := turnUsage
		payload.TurnUsage = &usage
		payload.RunID = usage.RunID
	}
	if usageSummary.SessionID != "" {
		summary := usageSummary
		payload.UsageSummary = &summary
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
