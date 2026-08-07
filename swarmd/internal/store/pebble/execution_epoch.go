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
	ExecutionEpochStatusActive             = "active"
	ExecutionEpochStatusSealed             = "sealed"
	ExecutionEpochBoundaryEventType        = "execution_epoch.began"
	V3SessionMutationBeginExecutionEpoch   = "execution_epoch.begin"
	ExecutionProviderLifecycleStateVersion = 1
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

// ExecutionProviderLifecycleState is the fixed-size durable authority for one
// epoch's provider lineage. It intentionally stores no transcript text and no
// provider response identifier: response IDs from store=false calls are not
// assumed to survive transport loss.
type ExecutionProviderLifecycleState struct {
	Version                       int    `json:"version"`
	SessionID                     string `json:"session_id"`
	EpochID                       string `json:"epoch_id"`
	Provider                      string `json:"provider"`
	Model                         string `json:"model"`
	ConfigurationHash             string `json:"configuration_hash"`
	ProviderLineageID             string `json:"provider_lineage_id"`
	ContextBranchID               string `json:"context_branch_id"`
	ProviderCacheKey              string `json:"provider_cache_key"`
	SessionAffinityKey            string `json:"session_affinity_key"`
	TransportAffinityKey          string `json:"transport_affinity_key"`
	PreviousProviderLineageID     string `json:"previous_provider_lineage_id,omitempty"`
	PreviousProvider              string `json:"previous_provider,omitempty"`
	PreviousModel                 string `json:"previous_model,omitempty"`
	BoundaryReason                string `json:"boundary_reason"`
	HandoffSummaryMessageID       string `json:"handoff_summary_message_id,omitempty"`
	HandoffSummaryGlobalSeq       uint64 `json:"handoff_summary_global_seq,omitempty"`
	ProviderLineageStartMessageID string `json:"provider_lineage_start_message_id,omitempty"`
	ProviderLineageStartRunID     string `json:"provider_lineage_start_run_id,omitempty"`
	ProviderLineageStartGlobalSeq uint64 `json:"provider_lineage_start_global_seq,omitempty"`
	UpdatedAt                     int64  `json:"updated_at"`
}

type BeginExecutionEpochInput struct {
	SessionID           string                  `json:"session_id"`
	UserID              string                  `json:"user_id,omitempty"`
	AccountScopeID      string                  `json:"account_scope_id,omitempty"`
	ClientRequestID     string                  `json:"client_request_id"`
	PayloadHash         string                  `json:"payload_hash"`
	EpochID             string                  `json:"epoch_id,omitempty"`
	Reason              string                  `json:"reason,omitempty"`
	PlanID              string                  `json:"plan_id,omitempty"`
	CheckpointID        string                  `json:"checkpoint_id,omitempty"`
	AttemptID           string                  `json:"attempt_id,omitempty"`
	RunSessionID        string                  `json:"run_session_id,omitempty"`
	ParentSessionID     string                  `json:"parent_session_id,omitempty"`
	ResumeContext       bool                    `json:"resume_context,omitempty"`
	SourceMessageID     string                  `json:"source_message_id,omitempty"`
	FinalHandoffMessage *MessageSnapshot        `json:"final_handoff_message,omitempty"`
	TriggerMessage      *MessageSnapshot        `json:"trigger_message,omitempty"`
	SkipRunIntent       bool                    `json:"skip_run_intent,omitempty"`
	ProviderPolicy      ExecutionProviderPolicy `json:"provider_policy,omitempty"`
	RunID               string                  `json:"run_id,omitempty"`
	NowUnixMs           int64                   `json:"now_unix_ms,omitempty"`
}

type BeginExecutionEpochResult struct {
	Epoch               ExecutionEpoch          `json:"epoch"`
	Predecessor         ExecutionEpoch          `json:"predecessor"`
	Event               V3SessionEvent          `json:"event"`
	Projection          V3SessionProjection     `json:"projection"`
	Outbox              V3RealtimeOutboxRecord  `json:"realtime_outbox"`
	FinalHandoffMessage *MessageSnapshot        `json:"final_handoff_message,omitempty"`
	FinalHandoffEvent   *V3SessionEvent         `json:"final_handoff_event,omitempty"`
	FinalHandoffOutbox  *V3RealtimeOutboxRecord `json:"final_handoff_realtime_outbox,omitempty"`
	TriggerMessage      *MessageSnapshot        `json:"trigger_message,omitempty"`
	TriggerEvent        *V3SessionEvent         `json:"trigger_event,omitempty"`
	TriggerOutbox       *V3RealtimeOutboxRecord `json:"trigger_realtime_outbox,omitempty"`
	RunIntent           *V3SessionRunIntent     `json:"run_intent,omitempty"`
	Replayed            bool                    `json:"replayed,omitempty"`
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
func KeyExecutionEpochLatest(sessionID string) string {
	return fmt.Sprintf("v3/execution_epoch_latest/%s", keyPart(sessionID))
}
func KeyExecutionProviderLifecycleState(sessionID, epochID string) string {
	return fmt.Sprintf("v3/execution_provider_lifecycle/%s/%s", keyPart(sessionID), keyPart(epochID))
}
func ExecutionProviderLifecycleStatePrefix(sessionID string) string {
	return fmt.Sprintf("v3/execution_provider_lifecycle/%s/", keyPart(sessionID))
}
func KeyExecutionEpochOrdinal(sessionID string, ordinal uint64) string {
	return fmt.Sprintf("v3/execution_epoch_by_ordinal/%s/%020d", keyPart(sessionID), ordinal)
}
func ExecutionEpochOrdinalPrefix(sessionID string) string {
	return fmt.Sprintf("v3/execution_epoch_by_ordinal/%s/", keyPart(sessionID))
}
func KeyExecutionEpochBoundary(sessionID, planID, checkpointID, attemptID, reason, runID, sourceMessageID string) string {
	key := executionEpochBoundaryLegacyKey(sessionID, planID, checkpointID, attemptID, reason)
	normalizedReason := strings.ToLower(strings.TrimSpace(reason))
	if normalizedReason == "post_checkpoint_followup" && strings.TrimSpace(runID) != "" {
		return key + "/" + keyPart(runID)
	}
	if strings.HasPrefix(normalizedReason, "context_compaction_") && strings.TrimSpace(sourceMessageID) != "" {
		return key + "/" + keyPart(sourceMessageID)
	}
	return key
}

func executionEpochBoundaryLegacyKey(sessionID, planID, checkpointID, attemptID, reason string) string {
	return fmt.Sprintf("v3/execution_epoch_boundary/%s/%s/%s/%s/%s", keyPart(sessionID), keyPart(planID), keyPart(checkpointID), keyPart(attemptID), keyPart(reason))
}
func ExecutionEpochBoundaryPrefix(sessionID string) string {
	return fmt.Sprintf("v3/execution_epoch_boundary/%s/", keyPart(sessionID))
}

func NewInitialExecutionEpoch(sessionID, userID, accountScopeID string, firstSeq uint64, now int64) ExecutionEpoch {
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	return ExecutionEpoch{EpochID: "epoch-00000000000000000001", SessionID: strings.TrimSpace(sessionID), UserID: strings.TrimSpace(userID), AccountScopeID: strings.TrimSpace(accountScopeID), Ordinal: 1, Status: ExecutionEpochStatusActive, FirstRootSeq: firstSeq, Boundary: ExecutionEpochBoundary{Reason: "session_created"}, CreatedAt: now, UpdatedAt: now}
}

func setExecutionEpochInBatch(batch *pebble.Batch, epoch ExecutionEpoch, active bool) error {
	if epoch.SessionID == "" || epoch.EpochID == "" || epoch.Ordinal == 0 {
		return errors.New("execution epoch session id, epoch id, and ordinal are required")
	}
	if active && epoch.Status != ExecutionEpochStatusActive {
		return fmt.Errorf("execution epoch %q cannot be the active index with status %q", epoch.EpochID, epoch.Status)
	}
	encodeStart := time.Now()
	payload, err := json.Marshal(epoch)
	observeExecutionEpochEncode(encodeStart)
	if err != nil {
		return fmt.Errorf("marshal execution epoch: %w", err)
	}
	if err := batch.Set([]byte(KeyExecutionEpoch(epoch.SessionID, epoch.EpochID)), payload, nil); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeyExecutionEpochOrdinal(epoch.SessionID, epoch.Ordinal)), []byte(epoch.EpochID), nil); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeyExecutionEpochLatest(epoch.SessionID)), payload, nil); err != nil {
		return err
	}
	if active {
		return batch.Set([]byte(KeyExecutionEpochActive(epoch.SessionID)), payload, nil)
	}
	return nil
}

func (s *SessionStore) GetExecutionEpoch(sessionID, epochID string) (ExecutionEpoch, bool, error) {
	return s.getExecutionEpochByKey(KeyExecutionEpoch(strings.TrimSpace(sessionID), strings.TrimSpace(epochID)))
}
func (s *SessionStore) GetActiveExecutionEpoch(sessionID string) (ExecutionEpoch, bool, error) {
	return s.getExecutionEpochByKey(KeyExecutionEpochActive(strings.TrimSpace(sessionID)))
}
func (s *SessionStore) GetLatestExecutionEpoch(sessionID string) (ExecutionEpoch, bool, error) {
	return s.getExecutionEpochByKey(KeyExecutionEpochLatest(strings.TrimSpace(sessionID)))
}

func (s *SessionStore) getLatestExecutionEpoch(sessionID string) (ExecutionEpoch, bool, error) {
	return s.GetLatestExecutionEpoch(sessionID)
}

func (s *SessionStore) getExecutionEpochByKey(key string) (ExecutionEpoch, bool, error) {
	return getExecutionEpochByKeyFromReader(s.store.db, key)
}

func getExecutionEpochByKeyFromReader(reader pebble.Reader, key string) (ExecutionEpoch, bool, error) {
	var epoch ExecutionEpoch
	readStart := time.Now()
	value, closer, err := reader.Get([]byte(key))
	observeExecutionEpochPointRead(readStart)
	if errors.Is(err, pebble.ErrNotFound) {
		return epoch, false, nil
	}
	if err != nil {
		return epoch, false, fmt.Errorf("get json key %q: %w", key, err)
	}
	payload := append([]byte(nil), value...)
	if closeErr := closer.Close(); closeErr != nil {
		return epoch, false, closeErr
	}
	decodeStart := time.Now()
	err = json.Unmarshal(payload, &epoch)
	observeExecutionEpochDecode(decodeStart)
	if err != nil {
		return ExecutionEpoch{}, false, fmt.Errorf("unmarshal json key %q: %w", key, err)
	}
	return epoch, true, nil
}

func (s *SessionStore) GetExecutionProviderLifecycleState(sessionID, epochID string) (ExecutionProviderLifecycleState, bool, error) {
	var state ExecutionProviderLifecycleState
	sessionID = strings.TrimSpace(sessionID)
	epochID = strings.TrimSpace(epochID)
	if sessionID == "" || epochID == "" {
		return state, false, errors.New("session id and epoch id are required")
	}
	ok, err := s.store.GetJSON(KeyExecutionProviderLifecycleState(sessionID, epochID), &state)
	if err != nil || !ok {
		return ExecutionProviderLifecycleState{}, ok, err
	}
	if state.Version != ExecutionProviderLifecycleStateVersion || state.SessionID != sessionID || state.EpochID != epochID {
		return ExecutionProviderLifecycleState{}, false, fmt.Errorf("execution provider lifecycle state for epoch %q is incompatible", epochID)
	}
	return state, true, nil
}

func (s *SessionStore) PutExecutionProviderLifecycleState(state ExecutionProviderLifecycleState) error {
	state.SessionID = strings.TrimSpace(state.SessionID)
	state.EpochID = strings.TrimSpace(state.EpochID)
	state.Provider = strings.ToLower(strings.TrimSpace(state.Provider))
	state.Model = strings.TrimSpace(state.Model)
	state.ConfigurationHash = strings.TrimSpace(state.ConfigurationHash)
	state.ProviderLineageID = strings.TrimSpace(state.ProviderLineageID)
	state.ContextBranchID = strings.TrimSpace(state.ContextBranchID)
	if state.SessionID == "" || state.EpochID == "" || state.Provider == "" || state.Model == "" || state.ConfigurationHash == "" || state.ProviderLineageID == "" || state.ContextBranchID == "" {
		return errors.New("execution provider lifecycle identity is incomplete")
	}
	if _, ok, err := s.GetExecutionEpoch(state.SessionID, state.EpochID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("execution epoch %q not found", state.EpochID)
	}
	state.Version = ExecutionProviderLifecycleStateVersion
	if state.UpdatedAt == 0 {
		state.UpdatedAt = time.Now().UnixMilli()
	}
	return s.store.PutJSON(KeyExecutionProviderLifecycleState(state.SessionID, state.EpochID), state)
}

// ListExecutionEpochMessages reads the explicitly named epoch and its inclusive
// message range from one Pebble snapshot. Both bounds are mandatory durable
// authority, so a delayed sealed-epoch worker cannot observe later root writes.
func (s *SessionStore) ListExecutionEpochMessages(sessionID, epochID string, limit int) (epoch ExecutionEpoch, messages []MessageSnapshot, err error) {
	rangeStart := time.Now()
	defer func() { ObserveExecutionEpochIterator(rangeStart, len(messages)) }()
	sessionID = strings.TrimSpace(sessionID)
	epochID = strings.TrimSpace(epochID)
	if sessionID == "" || epochID == "" {
		return ExecutionEpoch{}, nil, errors.New("session id and epoch id are required")
	}
	snapshot := s.store.db.NewSnapshot()
	defer func() {
		if closeErr := snapshot.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	epoch, ok, err := getExecutionEpochByKeyFromReader(snapshot, KeyExecutionEpoch(sessionID, epochID))
	if err != nil {
		return ExecutionEpoch{}, nil, err
	}
	if !ok {
		return ExecutionEpoch{}, nil, fmt.Errorf("execution epoch %q not found", epochID)
	}
	if epoch.FirstRootSeq == 0 || epoch.LastRootSeq < epoch.FirstRootSeq {
		return ExecutionEpoch{}, nil, fmt.Errorf("execution epoch %q has invalid message bounds", epochID)
	}
	messages, err = listV3SessionMessagesRangeFromReader(snapshot, sessionID, epoch.FirstRootSeq, epoch.LastRootSeq, limit)
	return epoch, messages, err
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
	if err := setExecutionEpochInBatch(batch, active, false); err != nil {
		return ExecutionEpoch{}, err
	}
	if err := batch.Delete([]byte(KeyExecutionEpochActive(input.SessionID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
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
	epoch.UpdatedAt = time.Now().UnixMilli()
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
	boundaryStart := time.Now()
	defer observeExecutionEpochBoundary(boundaryStart)
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
		boundarySeq := record.Result.FirstSeq
		if input.FinalHandoffMessage != nil {
			boundarySeq++
		}
		event, eventOK, err := s.GetV3SessionEvent(input.SessionID, boundarySeq)
		if err != nil || !eventOK || event.EpochID != epoch.EpochID || event.EventType != ExecutionEpochBoundaryEventType {
			return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch event is unavailable or inconsistent: %w", err)
		}
		currentProjection, projectionOK, err := s.GetV3SessionProjection(input.SessionID)
		if err != nil || !projectionOK || currentProjection.LastEventSeq < event.Seq {
			return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch projection is unavailable or inconsistent: %w", err)
		}
		predecessor, predecessorOK, err := s.GetExecutionEpoch(input.SessionID, epoch.ParentEpochID)
		if err != nil || !predecessorOK || predecessor.Status != ExecutionEpochStatusSealed || predecessor.LastRootSeq+1 != epoch.FirstRootSeq {
			return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch predecessor is unavailable or inconsistent: %w", err)
		}
		if input.FinalHandoffMessage != nil && predecessor.LastRootSeq != record.Result.FirstSeq {
			return BeginExecutionEpochResult{}, errors.New("replayed final handoff is not the predecessor epoch tail")
		}
		outboxReference, outboxOK, err := s.store.GetBytes(KeyV3RealtimeOutboxBySessionSeq(input.SessionID, event.Seq))
		if err != nil || !outboxOK {
			return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch outbox is unavailable: %w", err)
		}
		outbox, err := s.resolveV3RealtimeOutboxValue(outboxReference)
		if err != nil || outbox.Event.Seq != event.Seq {
			return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch outbox is inconsistent: %w", err)
		}
		if outbox.Projection.LastEventSeq != event.Seq || outbox.Projection.ProjectionHighWatermarkSeq != event.Seq {
			return BeginExecutionEpochResult{}, errors.New("replayed execution epoch outbox projection is inconsistent")
		}
		committedOutbox := []uint64{outbox.EndpointSeq}
		projection := outbox.Projection
		var finalHandoffMessage *MessageSnapshot
		var finalHandoffEvent *V3SessionEvent
		var finalHandoffOutbox *V3RealtimeOutboxRecord
		if input.FinalHandoffMessage != nil {
			handoff, handoffOK, handoffErr := s.GetV3SessionEvent(input.SessionID, record.Result.FirstSeq)
			if handoffErr != nil || !handoffOK || handoff.EpochID != predecessor.EpochID || handoff.EventType != "session.message.appended" {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed final handoff event is unavailable or inconsistent: %w", handoffErr)
			}
			handoffReference, handoffOutboxOK, handoffErr := s.store.GetBytes(KeyV3RealtimeOutboxBySessionSeq(input.SessionID, handoff.Seq))
			if handoffErr != nil || !handoffOutboxOK {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed final handoff outbox is unavailable: %w", handoffErr)
			}
			handoffRecord, handoffErr := s.resolveV3RealtimeOutboxValue(handoffReference)
			if handoffErr != nil || handoffRecord.Event.Seq != handoff.Seq {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed final handoff outbox is inconsistent: %w", handoffErr)
			}
			messages, handoffErr := listV3SessionMessagesRangeFromReader(s.store.db, input.SessionID, handoff.Seq, handoff.Seq, 1)
			if handoffErr != nil || len(messages) != 1 || messages[0].GlobalSeq != handoff.Seq {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed final handoff message is unavailable or inconsistent: %w", handoffErr)
			}
			finalHandoffMessage = &messages[0]
			finalHandoffEvent = &handoff
			finalHandoffOutbox = &handoffRecord
			committedOutbox = append([]uint64{handoffRecord.EndpointSeq}, committedOutbox...)
		}
		var triggerMessage *MessageSnapshot
		var triggerEvent *V3SessionEvent
		var triggerOutbox *V3RealtimeOutboxRecord
		if record.Result.LastSeq > record.Result.FirstSeq {
			trigger, triggerOK, triggerErr := s.GetV3SessionEvent(input.SessionID, record.Result.LastSeq)
			if triggerErr != nil || !triggerOK || trigger.EpochID != epoch.EpochID || trigger.EventType != "session.message.appended" {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch trigger is unavailable or inconsistent: %w", triggerErr)
			}
			triggerReference, triggerOutboxOK, triggerErr := s.store.GetBytes(KeyV3RealtimeOutboxBySessionSeq(input.SessionID, trigger.Seq))
			if triggerErr != nil || !triggerOutboxOK {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch trigger outbox is unavailable: %w", triggerErr)
			}
			triggerRecord, triggerErr := s.resolveV3RealtimeOutboxValue(triggerReference)
			if triggerErr != nil || triggerRecord.Event.Seq != trigger.Seq || triggerRecord.Projection.LastEventSeq != trigger.Seq {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch trigger outbox is inconsistent: %w", triggerErr)
			}
			messages, triggerErr := listV3SessionMessagesRangeFromReader(s.store.db, input.SessionID, trigger.Seq, trigger.Seq, 1)
			if triggerErr != nil || len(messages) != 1 || messages[0].SessionID != input.SessionID || messages[0].GlobalSeq != trigger.Seq {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch trigger message is unavailable or inconsistent: %w", triggerErr)
			}
			triggerMessage = &messages[0]
			triggerEvent = &trigger
			triggerOutbox = &triggerRecord
			projection = triggerRecord.Projection
			committedOutbox = append(committedOutbox, triggerRecord.EndpointSeq)
		}
		if err := s.store.sessionMutations.commitOutbox(s.store, committedOutbox); err != nil {
			return BeginExecutionEpochResult{}, err
		}
		var runIntent *V3SessionRunIntent
		if input.RunID != "" && !input.SkipRunIntent {
			intent, ok, intentErr := s.GetV3SessionRunIntent(input.SessionID, input.RunID)
			if intentErr != nil || !ok {
				return BeginExecutionEpochResult{}, fmt.Errorf("replayed execution epoch run intent is unavailable: %w", intentErr)
			}
			runIntent = &intent
		}
		return BeginExecutionEpochResult{Epoch: epoch, Predecessor: predecessor, Event: event, Projection: projection, Outbox: outbox, FinalHandoffMessage: finalHandoffMessage, FinalHandoffEvent: finalHandoffEvent, FinalHandoffOutbox: finalHandoffOutbox, TriggerMessage: triggerMessage, TriggerEvent: triggerEvent, TriggerOutbox: triggerOutbox, RunIntent: runIntent, Replayed: true}, nil
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
		latest, latestOK, latestErr := s.getLatestExecutionEpoch(input.SessionID)
		if latestErr != nil {
			return BeginExecutionEpochResult{}, latestErr
		}
		if latestOK {
			if latest.Status != ExecutionEpochStatusSealed || latest.LastRootSeq != seq {
				return BeginExecutionEpochResult{}, fmt.Errorf("latest execution epoch %q is inconsistent with root high watermark", latest.EpochID)
			}
			predecessor, hasPredecessor = latest, true
		}
	}
	if hasPredecessor && predecessor.Status != ExecutionEpochStatusActive && predecessor.Status != ExecutionEpochStatusSealed {
		return BeginExecutionEpochResult{}, fmt.Errorf("active execution epoch index points to %q with status %q", predecessor.EpochID, predecessor.Status)
	}
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
	finalHandoffProvided := input.FinalHandoffMessage != nil
	triggerProvided := input.TriggerMessage != nil
	finalHandoffSeq := uint64(0)
	boundarySeq := seq + 1
	if finalHandoffProvided {
		finalHandoffSeq = boundarySeq
		boundarySeq++
	}
	triggerSeq := uint64(0)
	if triggerProvided {
		triggerSeq = boundarySeq + 1
	}
	predecessor.Status = ExecutionEpochStatusSealed
	predecessor.LastRootSeq = seq
	if finalHandoffProvided {
		predecessor.LastRootSeq = finalHandoffSeq
	}
	predecessor.SealedAt = now
	predecessor.UpdatedAt = now
	epochID := strings.TrimSpace(input.EpochID)
	if epochID == "" {
		epochID = fmt.Sprintf("epoch-%020d", predecessor.Ordinal+1)
	}
	epochLastSeq := boundarySeq
	if triggerProvided {
		epochLastSeq = triggerSeq
	}
	epoch := ExecutionEpoch{EpochID: epochID, SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, ParentEpochID: predecessor.EpochID, Ordinal: predecessor.Ordinal + 1, Status: ExecutionEpochStatusActive, FirstRootSeq: boundarySeq, LastRootSeq: epochLastSeq, Boundary: ExecutionEpochBoundary{Reason: strings.TrimSpace(input.Reason), PlanID: strings.TrimSpace(input.PlanID), CheckpointID: strings.TrimSpace(input.CheckpointID), AttemptID: strings.TrimSpace(input.AttemptID), RunID: strings.TrimSpace(input.RunID), RunSessionID: strings.TrimSpace(input.RunSessionID), ParentSessionID: strings.TrimSpace(input.ParentSessionID), SourceMessageID: strings.TrimSpace(input.SourceMessageID), PredecessorLastRootSeq: predecessor.LastRootSeq}, ProviderPolicy: input.ProviderPolicy, CreatedAt: now, UpdatedAt: now}
	if existing, ok, getErr := s.GetExecutionEpoch(input.SessionID, epoch.EpochID); getErr != nil {
		return BeginExecutionEpochResult{}, getErr
	} else if ok {
		return BeginExecutionEpochResult{}, fmt.Errorf("execution epoch id collision: %q already belongs to ordinal %d", existing.EpochID, existing.Ordinal)
	}
	if ordinalEpochID, ok, getErr := s.store.GetBytes(KeyExecutionEpochOrdinal(input.SessionID, epoch.Ordinal)); getErr != nil {
		return BeginExecutionEpochResult{}, getErr
	} else if ok {
		return BeginExecutionEpochResult{}, fmt.Errorf("execution epoch ordinal collision: ordinal %d already maps to %q", epoch.Ordinal, string(ordinalEpochID))
	}
	if epoch.Boundary.PlanID != "" || epoch.Boundary.CheckpointID != "" {
		boundaryKey := KeyExecutionEpochBoundary(input.SessionID, epoch.Boundary.PlanID, epoch.Boundary.CheckpointID, epoch.Boundary.AttemptID, epoch.Boundary.Reason, epoch.Boundary.RunID, epoch.Boundary.SourceMessageID)
		if boundaryEpochID, ok, getErr := s.store.GetBytes(boundaryKey); getErr != nil {
			return BeginExecutionEpochResult{}, getErr
		} else if ok {
			return BeginExecutionEpochResult{}, fmt.Errorf("execution epoch boundary collision: plan %q checkpoint %q already maps to %q", epoch.Boundary.PlanID, epoch.Boundary.CheckpointID, string(boundaryEpochID))
		}
		if strings.EqualFold(epoch.Boundary.Reason, "post_checkpoint_followup") && epoch.Boundary.RunID != "" {
			legacyKey := executionEpochBoundaryLegacyKey(input.SessionID, epoch.Boundary.PlanID, epoch.Boundary.CheckpointID, epoch.Boundary.AttemptID, epoch.Boundary.Reason)
			if legacyEpochID, ok, getErr := s.store.GetBytes(legacyKey); getErr != nil {
				return BeginExecutionEpochResult{}, getErr
			} else if ok {
				legacyEpoch, exists, epochErr := s.GetExecutionEpoch(input.SessionID, string(legacyEpochID))
				if epochErr != nil {
					return BeginExecutionEpochResult{}, epochErr
				}
				if !exists || strings.TrimSpace(legacyEpoch.Boundary.RunID) == "" || strings.TrimSpace(legacyEpoch.Boundary.RunID) == epoch.Boundary.RunID {
					return BeginExecutionEpochResult{}, fmt.Errorf("execution epoch boundary collision: plan %q checkpoint %q already maps to %q", epoch.Boundary.PlanID, epoch.Boundary.CheckpointID, string(legacyEpochID))
				}
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{"epoch_id": epoch.EpochID, "parent_epoch_id": epoch.ParentEpochID, "ordinal": epoch.Ordinal, "reason": epoch.Boundary.Reason, "plan_id": epoch.Boundary.PlanID, "checkpoint_id": epoch.Boundary.CheckpointID, "attempt_id": epoch.Boundary.AttemptID, "run_id": epoch.Boundary.RunID, "run_session_id": epoch.Boundary.RunSessionID, "parent_session_id": epoch.Boundary.ParentSessionID})
	event := V3SessionEvent{ID: fmt.Sprintf("v3evt_%s_%020d", input.SessionID, boundarySeq), SessionID: input.SessionID, Seq: boundarySeq, EventType: ExecutionEpochBoundaryEventType, Payload: payload, TsUnixMs: now, EpochID: epoch.EpochID}
	projection := V3SessionProjection{SessionID: input.SessionID, LastEventSeq: epochLastSeq, ProjectionHighWatermarkSeq: epochLastSeq, UpdatedAt: now}
	outboxCount := 1
	if finalHandoffProvided {
		outboxCount++
	}
	if triggerProvided {
		outboxCount++
	}
	reserved, err := s.store.sessionMutations.reserveOutbox(s.store, outboxCount)
	if err != nil {
		return BeginExecutionEpochResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			s.store.sessionMutations.abandonOutbox(reserved)
		}
	}()
	boundaryOutboxIndex := 0
	if finalHandoffProvided {
		boundaryOutboxIndex = 1
	}
	outbox := V3RealtimeOutboxRecord{EndpointSeq: reserved[boundaryOutboxIndex], EndpointCursor: V3RealtimeOutboxCursor(reserved[boundaryOutboxIndex]), SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, Event: event, Projection: V3SessionProjection{SessionID: input.SessionID, LastEventSeq: boundarySeq, ProjectionHighWatermarkSeq: boundarySeq, UpdatedAt: now}, CreatedAt: now}
	var finalHandoffMessage MessageSnapshot
	var finalHandoffEvent *V3SessionEvent
	var finalHandoffOutbox *V3RealtimeOutboxRecord
	if finalHandoffProvided {
		finalHandoffMessage = sanitizeMessageSnapshot(*input.FinalHandoffMessage)
		finalHandoffMessage.SessionID = input.SessionID
		finalHandoffMessage.UserID = strings.TrimSpace(firstNonEmptyString(finalHandoffMessage.UserID, input.UserID))
		finalHandoffMessage.AccountScopeID = strings.TrimSpace(firstNonEmptyString(finalHandoffMessage.AccountScopeID, input.AccountScopeID))
		finalHandoffMessage.GlobalSeq = finalHandoffSeq
		if finalHandoffMessage.ID == "" {
			finalHandoffMessage.ID = fmt.Sprintf("v3msg_%s_%020d", input.SessionID, finalHandoffSeq)
		}
		if finalHandoffMessage.Role == "" || finalHandoffMessage.Content == "" {
			return BeginExecutionEpochResult{}, errors.New("final handoff message role and content are required")
		}
		if finalHandoffMessage.CreatedAt == 0 {
			finalHandoffMessage.CreatedAt = now
		}
		handoffPayload, marshalErr := (V3SessionMutationInput{Kind: V3SessionMutationAppendMessage, Message: &finalHandoffMessage}).v3EventPayload(finalHandoffSeq, SessionSnapshot{}, finalHandoffMessage, SessionLifecycleSnapshot{}, V3SessionRunIntent{}, SessionTurnUsageSnapshot{}, SessionUsageSummary{})
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		handoffEvent := V3SessionEvent{ID: fmt.Sprintf("v3evt_%s_%020d", input.SessionID, finalHandoffSeq), SessionID: input.SessionID, Seq: finalHandoffSeq, EventType: "session.message.appended", Payload: handoffPayload, TsUnixMs: now, EpochID: predecessor.EpochID}
		finalHandoffEvent = &handoffEvent
		handoffRecord := V3RealtimeOutboxRecord{EndpointSeq: reserved[0], EndpointCursor: V3RealtimeOutboxCursor(reserved[0]), SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, Event: handoffEvent, Projection: V3SessionProjection{SessionID: input.SessionID, LastEventSeq: finalHandoffSeq, ProjectionHighWatermarkSeq: finalHandoffSeq, UpdatedAt: now}, CreatedAt: now}
		finalHandoffOutbox = &handoffRecord
	}
	var triggerMessage MessageSnapshot
	var triggerEvent *V3SessionEvent
	var triggerOutbox *V3RealtimeOutboxRecord
	if triggerProvided {
		triggerMessage = sanitizeMessageSnapshot(*input.TriggerMessage)
		triggerMessage.SessionID = input.SessionID
		triggerMessage.UserID = strings.TrimSpace(firstNonEmptyString(triggerMessage.UserID, input.UserID))
		triggerMessage.AccountScopeID = strings.TrimSpace(firstNonEmptyString(triggerMessage.AccountScopeID, input.AccountScopeID))
		triggerMessage.GlobalSeq = triggerSeq
		if triggerMessage.ID == "" {
			triggerMessage.ID = fmt.Sprintf("v3msg_%s_%020d", input.SessionID, triggerSeq)
		}
		if triggerMessage.Role == "" || triggerMessage.Content == "" {
			return BeginExecutionEpochResult{}, errors.New("trigger message role and content are required")
		}
		if triggerMessage.CreatedAt == 0 {
			triggerMessage.CreatedAt = now
		}
		triggerPayload, marshalErr := (V3SessionMutationInput{Kind: V3SessionMutationAppendMessage, Message: &triggerMessage}).v3EventPayload(triggerSeq, SessionSnapshot{}, triggerMessage, SessionLifecycleSnapshot{}, V3SessionRunIntent{}, SessionTurnUsageSnapshot{}, SessionUsageSummary{})
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		trigger := V3SessionEvent{ID: fmt.Sprintf("v3evt_%s_%020d", input.SessionID, triggerSeq), SessionID: input.SessionID, Seq: triggerSeq, EventType: "session.message.appended", Payload: triggerPayload, TsUnixMs: now, EpochID: epoch.EpochID}
		triggerEvent = &trigger
		triggerOutboxIndex := boundaryOutboxIndex + 1
		triggerRecord := V3RealtimeOutboxRecord{EndpointSeq: reserved[triggerOutboxIndex], EndpointCursor: V3RealtimeOutboxCursor(reserved[triggerOutboxIndex]), SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, Event: trigger, Projection: projection, CreatedAt: now}
		triggerOutbox = &triggerRecord
	}
	if session, ok, getErr := s.GetSession(input.SessionID); getErr != nil {
		return BeginExecutionEpochResult{}, getErr
	} else if ok {
		membership := newV3RealtimeOutboxMembershipFromSession(session, now)
		outbox.Membership = membership
		if finalHandoffOutbox != nil {
			finalHandoffOutbox.Membership = membership
		}
		if triggerOutbox != nil {
			triggerOutbox.Membership = membership
		}
	}
	eventIDs := []string{event.ID}
	firstSeq := boundarySeq
	if finalHandoffEvent != nil {
		eventIDs = append([]string{finalHandoffEvent.ID}, eventIDs...)
		firstSeq = finalHandoffSeq
	}
	if triggerEvent != nil {
		eventIDs = append(eventIDs, triggerEvent.ID)
	}
	stored := V3SessionMutationStoredResult{SessionID: input.SessionID, RunID: epoch.EpochID, FirstSeq: firstSeq, LastSeq: epochLastSeq, EventIDs: eventIDs, PayloadHash: input.PayloadHash, ResponseVersion: V3SessionMutationResponseVersion, ResponseStatus: V3SessionMutationStatusCompleted, EventType: event.EventType, LastEventSeq: epochLastSeq, ProjectionHighWatermarkSeq: epochLastSeq, AppliedAt: now}
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
		boundaryKey := KeyExecutionEpochBoundary(input.SessionID, epoch.Boundary.PlanID, epoch.Boundary.CheckpointID, epoch.Boundary.AttemptID, epoch.Boundary.Reason, epoch.Boundary.RunID, epoch.Boundary.SourceMessageID)
		if err := batch.Set([]byte(boundaryKey), []byte(epoch.EpochID), nil); err != nil {
			return BeginExecutionEpochResult{}, err
		}
	}
	for key, value := range map[string]any{KeyV3SessionEvent(input.SessionID, boundarySeq): event, KeyV3SessionProjection(input.SessionID): projection, KeyV3RealtimeOutbox(outbox.EndpointSeq): outbox, idemKey: idem} {
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
	for _, key := range []string{KeyV3RealtimeOutboxBySessionEndpoint(input.SessionID, outbox.EndpointSeq), KeyV3RealtimeOutboxBySessionSeq(input.SessionID, boundarySeq), KeyV3RealtimeOutboxByAuthScope(input.AccountScopeID, input.UserID, outbox.EndpointSeq)} {
		if err := batch.Set([]byte(key), ref, nil); err != nil {
			return BeginExecutionEpochResult{}, err
		}
	}
	messageCountDelta := 0
	if finalHandoffProvided {
		messageCountDelta++
	}
	if triggerProvided {
		messageCountDelta++
	}
	if messageCountDelta > 0 {
		if session, ok, getErr := s.GetSession(input.SessionID); getErr != nil {
			return BeginExecutionEpochResult{}, getErr
		} else if !ok {
			return BeginExecutionEpochResult{}, fmt.Errorf("session %q not found", input.SessionID)
		} else {
			session.MessageCount += messageCountDelta
			session.UpdatedAt = now
			session.LastMessageAt = now
			if err := s.setSessionInBatch(batch, session); err != nil {
				return BeginExecutionEpochResult{}, err
			}
		}
	}
	if finalHandoffEvent != nil && finalHandoffOutbox != nil {
		handoffMessageRaw, marshalErr := json.Marshal(finalHandoffMessage)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		handoffEventRaw, marshalErr := json.Marshal(*finalHandoffEvent)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		handoffOutboxRaw, marshalErr := json.Marshal(*finalHandoffOutbox)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		handoffRef, marshalErr := marshalV3RealtimeOutboxReference(*finalHandoffOutbox)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		for key, value := range map[string][]byte{
			KeyV3SessionMessage(input.SessionID, finalHandoffSeq):                                              handoffMessageRaw,
			KeyV3SessionEvent(input.SessionID, finalHandoffSeq):                                                handoffEventRaw,
			KeyV3RealtimeOutbox(finalHandoffOutbox.EndpointSeq):                                                handoffOutboxRaw,
			KeyV3RealtimeOutboxBySessionEndpoint(input.SessionID, finalHandoffOutbox.EndpointSeq):              handoffRef,
			KeyV3RealtimeOutboxBySessionSeq(input.SessionID, finalHandoffSeq):                                  handoffRef,
			KeyV3RealtimeOutboxByAuthScope(input.AccountScopeID, input.UserID, finalHandoffOutbox.EndpointSeq): handoffRef,
		} {
			if err := batch.Set([]byte(key), value, nil); err != nil {
				return BeginExecutionEpochResult{}, err
			}
		}
	}
	if triggerEvent != nil && triggerOutbox != nil {
		triggerMessageRaw, marshalErr := json.Marshal(triggerMessage)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		triggerEventRaw, marshalErr := json.Marshal(*triggerEvent)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		triggerOutboxRaw, marshalErr := json.Marshal(*triggerOutbox)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		triggerRef, marshalErr := marshalV3RealtimeOutboxReference(*triggerOutbox)
		if marshalErr != nil {
			return BeginExecutionEpochResult{}, marshalErr
		}
		for key, value := range map[string][]byte{
			KeyV3SessionMessage(input.SessionID, triggerSeq):                                              triggerMessageRaw,
			KeyV3SessionEvent(input.SessionID, triggerSeq):                                                triggerEventRaw,
			KeyV3RealtimeOutbox(triggerOutbox.EndpointSeq):                                                triggerOutboxRaw,
			KeyV3RealtimeOutboxBySessionEndpoint(input.SessionID, triggerOutbox.EndpointSeq):              triggerRef,
			KeyV3RealtimeOutboxBySessionSeq(input.SessionID, triggerSeq):                                  triggerRef,
			KeyV3RealtimeOutboxByAuthScope(input.AccountScopeID, input.UserID, triggerOutbox.EndpointSeq): triggerRef,
		} {
			if err := batch.Set([]byte(key), value, nil); err != nil {
				return BeginExecutionEpochResult{}, err
			}
		}
	}
	if err := batch.Set([]byte(KeyV3SessionSequence(input.SessionID)), uint64ToBytes(epochLastSeq), nil); err != nil {
		return BeginExecutionEpochResult{}, err
	}
	var committedRunIntent *V3SessionRunIntent
	if input.RunID != "" && !input.SkipRunIntent {
		run, ok, getErr := s.GetV3SessionRunIntent(input.SessionID, input.RunID)
		if getErr != nil {
			return BeginExecutionEpochResult{}, getErr
		}
		if !ok {
			run = V3SessionRunIntent{SessionID: input.SessionID, UserID: strings.TrimSpace(input.UserID), AccountScopeID: input.AccountScopeID, RunID: strings.TrimSpace(input.RunID), Status: V3RunIntentPendingExecutor, CreatedAt: now, ResumeContext: input.ResumeContext}
		}
		if ok {
			previousStatusKey := KeyV3SessionRunIntentStatus(run.Status, run.UpdatedAt, run.AccountScopeID, run.SessionID, run.RunID)
			if err := batch.Delete([]byte(previousStatusKey), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
				return BeginExecutionEpochResult{}, err
			}
		}
		run.EpochID = epoch.EpochID
		run.PlanID = epoch.Boundary.PlanID
		run.CheckpointID = epoch.Boundary.CheckpointID
		run.AttemptID = epoch.Boundary.AttemptID
		run.RunSessionID = epoch.Boundary.RunSessionID
		run.ParentSessionID = epoch.Boundary.ParentSessionID
		run.ResumeContext = input.ResumeContext
		run.UpdatedAt = now
		run.EventSeq = epochLastSeq
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
		committedRunIntent = &run
	}
	if hook := s.store.sessionMutations.beforeExecutionEpochCommit; hook != nil {
		if err := hook(input.SessionID); err != nil {
			return BeginExecutionEpochResult{}, err
		}
	}
	commitStart := time.Now()
	err = batch.Commit(pebble.Sync)
	observeExecutionEpochBatchCommit(commitStart)
	if err != nil {
		return BeginExecutionEpochResult{}, err
	}
	committed = true
	if err := s.store.sessionMutations.commitOutbox(s.store, reserved); err != nil {
		return BeginExecutionEpochResult{}, err
	}
	var committedTriggerMessage *MessageSnapshot
	if triggerProvided {
		committedTriggerMessage = &triggerMessage
	}
	var committedFinalHandoffMessage *MessageSnapshot
	if finalHandoffProvided {
		committedFinalHandoffMessage = &finalHandoffMessage
	}
	return BeginExecutionEpochResult{Epoch: epoch, Predecessor: predecessor, Event: event, Projection: projection, Outbox: outbox, FinalHandoffMessage: committedFinalHandoffMessage, FinalHandoffEvent: finalHandoffEvent, FinalHandoffOutbox: finalHandoffOutbox, TriggerMessage: committedTriggerMessage, TriggerEvent: triggerEvent, TriggerOutbox: triggerOutbox, RunIntent: committedRunIntent}, nil
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
