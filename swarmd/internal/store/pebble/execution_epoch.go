package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	ExecutionEpochStatusActive           = "active"
	ExecutionEpochStatusSealed           = "sealed"
	ExecutionEpochBoundaryEventType      = "execution_epoch.began"
	V3SessionMutationBeginExecutionEpoch = "execution_epoch.begin"
)

// ExecutionEpoch is a bounded execution segment inside one durable V3 session.
// Root event and message sequences remain session-global.
type ExecutionEpoch struct {
	EpochID        string                  `json:"epoch_id"`
	SessionID      string                  `json:"session_id"`
	UserID         string                  `json:"user_id,omitempty"`
	AccountScopeID string                  `json:"account_scope_id,omitempty"`
	ParentEpochID  string                  `json:"parent_epoch_id,omitempty"`
	Ordinal        uint64                  `json:"ordinal"`
	Status         string                  `json:"status"`
	FirstRootSeq   uint64                  `json:"first_root_seq"`
	LastRootSeq    uint64                  `json:"last_root_seq,omitempty"`
	Boundary       ExecutionEpochBoundary  `json:"boundary"`
	ProviderPolicy ExecutionProviderPolicy `json:"provider_policy,omitempty"`
	CreatedAt      int64                   `json:"created_at"`
	UpdatedAt      int64                   `json:"updated_at"`
	SealedAt       int64                   `json:"sealed_at,omitempty"`
}

type ExecutionEpochBoundary struct {
	Reason                 string                      `json:"reason,omitempty"`
	PlanID                 string                      `json:"plan_id,omitempty"`
	CheckpointID           string                      `json:"checkpoint_id,omitempty"`
	AttemptID              string                      `json:"attempt_id,omitempty"`
	RunID                  string                      `json:"run_id,omitempty"`
	RunSessionID           string                      `json:"run_session_id,omitempty"`
	ParentSessionID        string                      `json:"parent_session_id,omitempty"`
	SourceMessageID        string                      `json:"source_message_id,omitempty"`
	PredecessorLastRootSeq uint64                      `json:"predecessor_last_root_seq,omitempty"`
	LegacyPrefix           *ExecutionEpochLegacyPrefix `json:"legacy_prefix,omitempty"`
}

// ExecutionEpochLegacyPrefix captures fixed-point legacy state without scanning history.
type ExecutionEpochLegacyPrefix struct {
	LastRootSeq             uint64 `json:"last_root_seq"`
	ProjectionHighWatermark uint64 `json:"projection_high_watermark"`
	ActivePlanID            string `json:"active_plan_id,omitempty"`
	LifecycleGeneration     uint64 `json:"lifecycle_generation,omitempty"`
}

type ExecutionProviderPolicy struct {
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}

type BeginExecutionEpochInput struct {
	SessionID       string                  `json:"session_id"`
	UserID          string                  `json:"user_id,omitempty"`
	AccountScopeID  string                  `json:"account_scope_id,omitempty"`
	ClientRequestID string                  `json:"client_request_id"`
	PayloadHash     string                  `json:"payload_hash"`
	EpochID         string                  `json:"epoch_id,omitempty"`
	Reason          string                  `json:"reason,omitempty"`
	PlanID          string                  `json:"plan_id,omitempty"`
	CheckpointID    string                  `json:"checkpoint_id,omitempty"`
	AttemptID       string                  `json:"attempt_id,omitempty"`
	RunSessionID    string                  `json:"run_session_id,omitempty"`
	ParentSessionID string                  `json:"parent_session_id,omitempty"`
	SourceMessageID string                  `json:"source_message_id,omitempty"`
	ProviderPolicy  ExecutionProviderPolicy `json:"provider_policy,omitempty"`
	RunID           string                  `json:"run_id,omitempty"`
	NowUnixMs       int64                   `json:"now_unix_ms,omitempty"`
}

type BeginExecutionEpochResult struct {
	Epoch       ExecutionEpoch         `json:"epoch"`
	Predecessor ExecutionEpoch         `json:"predecessor"`
	Event       V3SessionEvent         `json:"event"`
	Projection  V3SessionProjection    `json:"projection"`
	Outbox      V3RealtimeOutboxRecord `json:"realtime_outbox"`
	Replayed    bool                   `json:"replayed,omitempty"`
}

// SealExecutionEpochInput names the epoch being sealed so a delayed executor
// cannot accidentally seal a newer epoch for the same root session.
type SealExecutionEpochInput struct {
	SessionID string `json:"session_id"`
	EpochID   string `json:"epoch_id"`
	NowUnixMs int64  `json:"now_unix_ms,omitempty"`
}

func KeyExecutionEpoch(sessionID, epochID string) string {
	return fmt.Sprintf("v3/execution_epoch/%s/%s", keyPart(sessionID), keyPart(epochID))
}
func ExecutionEpochPrefix(sessionID string) string {
	return fmt.Sprintf("v3/execution_epoch/%s/", keyPart(sessionID))
}
func KeyExecutionEpochActive(sessionID string) string {
	return fmt.Sprintf("v3/execution_epoch_active/%s", keyPart(sessionID))
}
func KeyExecutionEpochOrdinal(sessionID string, ordinal uint64) string {
	return fmt.Sprintf("v3/execution_epoch_by_ordinal/%s/%020d", keyPart(sessionID), ordinal)
}
func KeyExecutionEpochBoundary(sessionID, planID, checkpointID string) string {
	return fmt.Sprintf("v3/execution_epoch_boundary/%s/%s/%s", keyPart(sessionID), keyPart(planID), keyPart(checkpointID))
}

func NewInitialExecutionEpoch(sessionID, userID, accountScopeID string, firstSeq uint64, now int64) ExecutionEpoch {
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	return ExecutionEpoch{EpochID: "epoch-00000000000000000001", SessionID: strings.TrimSpace(sessionID), UserID: strings.TrimSpace(userID), AccountScopeID: strings.TrimSpace(accountScopeID), Ordinal: 1, Status: ExecutionEpochStatusActive, FirstRootSeq: firstSeq, Boundary: ExecutionEpochBoundary{Reason: "session_created"}, CreatedAt: now, UpdatedAt: now}
}

func setExecutionEpochInBatch(batch *pebble.Batch, epoch ExecutionEpoch, active bool) error {
	payload, err := json.Marshal(epoch)
	if err != nil {
		return fmt.Errorf("marshal execution epoch: %w", err)
	}
	if err := batch.Set([]byte(KeyExecutionEpoch(epoch.SessionID, epoch.EpochID)), payload, nil); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeyExecutionEpochOrdinal(epoch.SessionID, epoch.Ordinal)), []byte(epoch.EpochID), nil); err != nil {
		return err
	}
	if active {
		return batch.Set([]byte(KeyExecutionEpochActive(epoch.SessionID)), payload, nil)
	}
	return nil
}

func (s *SessionStore) GetExecutionEpoch(sessionID, epochID string) (ExecutionEpoch, bool, error) {
	var epoch ExecutionEpoch
	ok, err := s.store.GetJSON(KeyExecutionEpoch(strings.TrimSpace(sessionID), strings.TrimSpace(epochID)), &epoch)
	return epoch, ok, err
}
func (s *SessionStore) GetActiveExecutionEpoch(sessionID string) (ExecutionEpoch, bool, error) {
	var epoch ExecutionEpoch
	ok, err := s.store.GetJSON(KeyExecutionEpochActive(strings.TrimSpace(sessionID)), &epoch)
	return epoch, ok, err
}

// SealExecutionEpoch closes the named epoch at the durable root sequence high
// watermark. It performs constant work and never infers boundaries from messages.
func (s *SessionStore) SealExecutionEpoch(input SealExecutionEpochInput) (ExecutionEpoch, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.EpochID = strings.TrimSpace(input.EpochID)
	if s == nil || s.store == nil {
		return ExecutionEpoch{}, errors.New("session store is not configured")
	}
	if input.SessionID == "" || input.EpochID == "" {
		return ExecutionEpoch{}, errors.New("session id and epoch id are required")
	}
	unlock := s.store.sessionMutations.lockSessions(input.SessionID)
	defer unlock()
	active, ok, err := s.GetActiveExecutionEpoch(input.SessionID)
	if err != nil {
		return ExecutionEpoch{}, err
	}
	if !ok || active.EpochID != input.EpochID {
		return ExecutionEpoch{}, fmt.Errorf("execution epoch %q is not active", input.EpochID)
	}
	seq, err := s.readV3SessionSequence(input.SessionID)
	if err != nil {
		return ExecutionEpoch{}, err
	}
	if seq < active.FirstRootSeq {
		return ExecutionEpoch{}, fmt.Errorf("execution epoch %q starts after root high watermark", input.EpochID)
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	active.Status = ExecutionEpochStatusSealed
	active.LastRootSeq = seq
	active.UpdatedAt = now
	active.SealedAt = now
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := setExecutionEpochInBatch(batch, active, true); err != nil {
		return ExecutionEpoch{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return ExecutionEpoch{}, err
	}
	return active, nil
}

// RepairActiveExecutionEpoch restores bounded indexes from an explicitly named
// durable epoch. The caller supplies authority; this method does not scan or
// infer an epoch from transcript content.
func (s *SessionStore) RepairActiveExecutionEpoch(sessionID, epochID string) (ExecutionEpoch, error) {
	sessionID = strings.TrimSpace(sessionID)
	epochID = strings.TrimSpace(epochID)
	if s == nil || s.store == nil {
		return ExecutionEpoch{}, errors.New("session store is not configured")
	}
	if sessionID == "" || epochID == "" {
		return ExecutionEpoch{}, errors.New("session id and epoch id are required")
	}
	unlock := s.store.sessionMutations.lockSessions(sessionID)
	defer unlock()
	epoch, ok, err := s.GetExecutionEpoch(sessionID, epochID)
	if err != nil {
		return ExecutionEpoch{}, err
	}
	if !ok || epoch.SessionID != sessionID {
		return ExecutionEpoch{}, fmt.Errorf("execution epoch %q not found", epochID)
	}
	if epoch.Status != ExecutionEpochStatusActive {
		return ExecutionEpoch{}, fmt.Errorf("execution epoch %q is not active", epochID)
	}
	seq, err := s.readV3SessionSequence(sessionID)
	if err != nil {
		return ExecutionEpoch{}, err
	}
	if seq < epoch.FirstRootSeq {
		return ExecutionEpoch{}, fmt.Errorf("execution epoch %q starts after root high watermark", epochID)
	}
	epoch.LastRootSeq = seq
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := setExecutionEpochInBatch(batch, epoch, true); err != nil {
		return ExecutionEpoch{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return ExecutionEpoch{}, err
	}
	return epoch, nil
}

func (s *SessionStore) BeginExecutionEpoch(input BeginExecutionEpochInput) (BeginExecutionEpochResult, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.ClientRequestID = strings.TrimSpace(input.ClientRequestID)
	input.PayloadHash = strings.TrimSpace(input.PayloadHash)
	if s == nil || s.store == nil {
		return BeginExecutionEpochResult{}, errors.New("session store is not configured")
	}
	if input.SessionID == "" || input.ClientRequestID == "" || input.PayloadHash == "" {
		return BeginExecutionEpochResult{}, errors.New("session id, client request id, and payload hash are required")
	}
	unlock := s.store.sessionMutations.lockSessions(input.SessionID)
	defer unlock()
	idemKey := KeyV3SessionOperationIdempotency(input.AccountScopeID, input.SessionID, V3SessionMutationBeginExecutionEpoch, input.ClientRequestID)
	if record, ok, err := s.getV3SessionIdempotencyRecordByKey(idemKey); err != nil {
		return BeginExecutionEpochResult{}, err
	} else if ok {
		if record.PayloadHash != input.PayloadHash {
			return BeginExecutionEpochResult{}, ErrV3IdempotencyConflict
		}
		epoch, exists, err := s.GetExecutionEpoch(input.SessionID, record.Result.RunID)
		if err != nil || !exists {
			return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch is unavailable: %w", err)
		}
		event, _, err := s.GetV3SessionEvent(input.SessionID, record.Result.LastSeq)
		if err != nil {
			return BeginExecutionEpochResult{}, err
		}
		projection, _, err := s.GetV3SessionProjection(input.SessionID)
		if err != nil {
			return BeginExecutionEpochResult{}, err
		}
		return BeginExecutionEpochResult{Epoch: epoch, Event: event, Projection: projection, Replayed: true}, nil
	}
	return s.beginFreshExecutionEpoch(input, idemKey)
}

func (s *SessionStore) beginFreshExecutionEpoch(input BeginExecutionEpochInput, idemKey string) (BeginExecutionEpochResult, error) {
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	seq, err := s.readV3SessionSequence(input.SessionID)
	if err != nil {
		return BeginExecutionEpochResult{}, err
	}
	predecessor, hasPredecessor, err := s.GetActiveExecutionEpoch(input.SessionID)
	if err != nil {
		return BeginExecutionEpochResult{}, err
	}
	legacy := (*ExecutionEpochLegacyPrefix)(nil)
	if !hasPredecessor {
		legacy, err = s.readLegacyEpochPrefix(input.SessionID, seq)
		if err != nil {
			return BeginExecutionEpochResult{}, err
		}
		predecessor = NewInitialExecutionEpoch(input.SessionID, input.UserID, input.AccountScopeID, 1, now)
		predecessor.Boundary.Reason = "legacy_prefix"
		predecessor.Boundary.LegacyPrefix = legacy
		predecessor.LastRootSeq = seq
	}
	boundarySeq := seq + 1
	predecessor.Status = ExecutionEpochStatusSealed
	predecessor.LastRootSeq = seq
	predecessor.SealedAt = now
	predecessor.UpdatedAt = now
	epochID := strings.TrimSpace(input.EpochID)
	if epochID == "" {
		epochID = fmt.Sprintf("epoch-%020d", predecessor.Ordinal+1)
	}
	epoch := ExecutionEpoch{EpochID: epochID, SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, ParentEpochID: predecessor.EpochID, Ordinal: predecessor.Ordinal + 1, Status: ExecutionEpochStatusActive, FirstRootSeq: boundarySeq, Boundary: ExecutionEpochBoundary{Reason: strings.TrimSpace(input.Reason), PlanID: strings.TrimSpace(input.PlanID), CheckpointID: strings.TrimSpace(input.CheckpointID), AttemptID: strings.TrimSpace(input.AttemptID), RunID: strings.TrimSpace(input.RunID), RunSessionID: strings.TrimSpace(input.RunSessionID), ParentSessionID: strings.TrimSpace(input.ParentSessionID), SourceMessageID: strings.TrimSpace(input.SourceMessageID), PredecessorLastRootSeq: seq}, ProviderPolicy: input.ProviderPolicy, CreatedAt: now, UpdatedAt: now}
	payload, _ := json.Marshal(map[string]any{"epoch_id": epoch.EpochID, "parent_epoch_id": epoch.ParentEpochID, "ordinal": epoch.Ordinal, "reason": epoch.Boundary.Reason, "plan_id": epoch.Boundary.PlanID, "checkpoint_id": epoch.Boundary.CheckpointID, "attempt_id": epoch.Boundary.AttemptID, "run_id": epoch.Boundary.RunID, "run_session_id": epoch.Boundary.RunSessionID, "parent_session_id": epoch.Boundary.ParentSessionID})
	event := V3SessionEvent{ID: fmt.Sprintf("v3evt_%s_%020d", input.SessionID, boundarySeq), SessionID: input.SessionID, Seq: boundarySeq, EventType: ExecutionEpochBoundaryEventType, Payload: payload, TsUnixMs: now, EpochID: epoch.EpochID}
	projection := V3SessionProjection{SessionID: input.SessionID, LastEventSeq: boundarySeq, ProjectionHighWatermarkSeq: boundarySeq, UpdatedAt: now}
	reserved, err := s.store.sessionMutations.reserveOutbox(s.store, 1)
	if err != nil {
		return BeginExecutionEpochResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			s.store.sessionMutations.abandonOutbox(reserved)
		}
	}()
	outbox := V3RealtimeOutboxRecord{EndpointSeq: reserved[0], EndpointCursor: V3RealtimeOutboxCursor(reserved[0]), SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, Event: event, Projection: projection, CreatedAt: now}
	if session, ok, getErr := s.GetSession(input.SessionID); getErr != nil {
		return BeginExecutionEpochResult{}, getErr
	} else if ok {
		outbox.Membership = newV3RealtimeOutboxMembershipFromSession(session, now)
	}
	stored := V3SessionMutationStoredResult{SessionID: input.SessionID, RunID: epoch.EpochID, FirstSeq: boundarySeq, LastSeq: boundarySeq, EventIDs: []string{event.ID}, PayloadHash: input.PayloadHash, ResponseVersion: V3SessionMutationResponseVersion, ResponseStatus: V3SessionMutationStatusCompleted, EventType: event.EventType, LastEventSeq: boundarySeq, ProjectionHighWatermarkSeq: boundarySeq, AppliedAt: now}
	idem := V3SessionIdempotencyRecord{SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, Operation: V3SessionMutationBeginExecutionEpoch, ClientRequestID: input.ClientRequestID, Key: input.ClientRequestID, PayloadHash: input.PayloadHash, Kind: V3SessionMutationBeginExecutionEpoch, Status: V3SessionMutationStatusCompleted, Result: stored, CreatedAt: now, CompletedAt: now}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := setExecutionEpochInBatch(batch, predecessor, false); err != nil {
		return BeginExecutionEpochResult{}, err
	}
	if err := setExecutionEpochInBatch(batch, epoch, true); err != nil {
		return BeginExecutionEpochResult{}, err
	}
	if epoch.Boundary.PlanID != "" || epoch.Boundary.CheckpointID != "" {
		if err := batch.Set([]byte(KeyExecutionEpochBoundary(input.SessionID, epoch.Boundary.PlanID, epoch.Boundary.CheckpointID)), []byte(epoch.EpochID), nil); err != nil {
			return BeginExecutionEpochResult{}, err
		}
	}
	for key, value := range map[string]any{KeyV3SessionEvent(input.SessionID, boundarySeq): event, KeyV3SessionProjection(input.SessionID): projection, KeyV3RealtimeOutbox(reserved[0]): outbox, idemKey: idem} {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		if err := batch.Set([]byte(key), raw, nil); err != nil {
			return BeginExecutionEpochResult{}, err
		}
	}
	ref, err := marshalV3RealtimeOutboxReference(outbox)
	if err != nil {
		return BeginExecutionEpochResult{}, err
	}
	for _, key := range []string{KeyV3RealtimeOutboxBySessionEndpoint(input.SessionID, reserved[0]), KeyV3RealtimeOutboxBySessionSeq(input.SessionID, boundarySeq), KeyV3RealtimeOutboxByAuthScope(input.AccountScopeID, input.UserID, reserved[0])} {
		if err := batch.Set([]byte(key), ref, nil); err != nil {
			return BeginExecutionEpochResult{}, err
		}
	}
	if err := batch.Set([]byte(KeyV3SessionSequence(input.SessionID)), uint64ToBytes(boundarySeq), nil); err != nil {
		return BeginExecutionEpochResult{}, err
	}
	if input.RunID != "" {
		run, ok, getErr := s.GetV3SessionRunIntent(input.SessionID, input.RunID)
		if getErr != nil {
			return BeginExecutionEpochResult{}, getErr
		}
		if !ok {
			run = V3SessionRunIntent{SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, RunID: strings.TrimSpace(input.RunID), Status: V3RunIntentPendingExecutor, CreatedAt: now}
		}
		run.EpochID = epoch.EpochID
		run.PlanID = epoch.Boundary.PlanID
		run.CheckpointID = epoch.Boundary.CheckpointID
		run.AttemptID = epoch.Boundary.AttemptID
		run.RunSessionID = epoch.Boundary.RunSessionID
		run.ParentSessionID = epoch.Boundary.ParentSessionID
		run.UpdatedAt = now
		run.EventSeq = boundarySeq
		raw, marshalErr := json.Marshal(run)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		if err := batch.Set([]byte(KeyV3SessionRunIntent(input.SessionID, input.RunID)), raw, nil); err != nil {
			return BeginExecutionEpochResult{}, err
		}
		if err := batch.Set([]byte(KeyV3SessionRunIntentStatus(run.Status, run.UpdatedAt, run.AccountScopeID, run.SessionID, run.RunID)), raw, nil); err != nil {
			return BeginExecutionEpochResult{}, err
		}
		previousState, previousStateOK, stateErr := s.GetV3SessionRunState(input.SessionID)
		if stateErr != nil {
			return BeginExecutionEpochResult{}, stateErr
		}
		if err := s.setV3SessionRunStateInBatch(batch, run, previousState, previousStateOK); err != nil {
			return BeginExecutionEpochResult{}, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return BeginExecutionEpochResult{}, err
	}
	committed = true
	if err := s.store.sessionMutations.commitOutbox(s.store, reserved); err != nil {
		return BeginExecutionEpochResult{}, err
	}
	return BeginExecutionEpochResult{Epoch: epoch, Predecessor: predecessor, Event: event, Projection: projection, Outbox: outbox}, nil
}

func (s *SessionStore) readLegacyEpochPrefix(sessionID string, seq uint64) (*ExecutionEpochLegacyPrefix, error) {
	prefix := &ExecutionEpochLegacyPrefix{LastRootSeq: seq}
	if projection, ok, err := s.GetV3SessionProjection(sessionID); err != nil {
		return nil, err
	} else if ok {
		prefix.ProjectionHighWatermark = projection.ProjectionHighWatermarkSeq
	}
	var active SessionPlanActive
	if ok, err := s.store.GetJSON(KeySessionPlanActive(sessionID), &active); err != nil {
		return nil, err
	} else if ok {
		prefix.ActivePlanID = active.PlanID
	}
	var lifecycle SessionLifecycleSnapshot
	if ok, err := s.store.GetJSON(KeySessionLifecycle(sessionID), &lifecycle); err != nil {
		return nil, err
	} else if ok {
		prefix.LifecycleGeneration = lifecycle.Generation
	}
	return prefix, nil
}
