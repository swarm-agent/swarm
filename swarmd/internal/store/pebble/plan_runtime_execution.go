package pebblestore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	PlanRuntimeSchemaVersion           = 1
	PlanExecutionRealtimeProtocol      = "v3.plan_execution"
	PlanExecutionRealtimeKindDelta     = "plan.execution.delta"
	PlanRuntimeProjectionSchemaVersion = 1
	PlanRuntimeMaxSubtaskChanges       = 64
	PlanRuntimeMaxEventBytes           = 48 << 10
	PlanRuntimeMaxOutboxBytes          = 64 << 10
	PlanRuntimeMaxResultBytes          = 32 << 10
	PlanRuntimeMaxBatchBytes           = 128 << 10
	PlanRuntimeMaxKeysPerCommit        = 144
	PlanRuntimeMaxEventPage            = 256
	PlanRuntimeMaxEventPageBytes       = 1 << 20
)

var (
	ErrPlanRuntimeIdempotencyConflict = errors.New("plan runtime idempotency key was reused with a different payload")
	ErrPlanRuntimeExecutionConflict   = errors.New("plan runtime execution sequence conflict")
)

// PlanRuntimeExecutionConflictError deliberately carries only bounded current state.
type PlanRuntimeExecutionConflictError struct {
	Expected uint64
	Current  uint64
	Status   string
}

func (e *PlanRuntimeExecutionConflictError) Error() string {
	return fmt.Sprintf("%v: expected %d, current %d (%s)", ErrPlanRuntimeExecutionConflict, e.Expected, e.Current, e.Status)
}
func (e *PlanRuntimeExecutionConflictError) Unwrap() error { return ErrPlanRuntimeExecutionConflict }

// PlanExecutionSummary is the fixed-size high-water record for the new runtime.
type PlanExecutionSummary struct {
	SchemaVersion            int    `json:"schema_version"`
	SessionID                string `json:"session_id"`
	PlanID                   string `json:"plan_id"`
	DefinitionRevision       uint64 `json:"definition_revision"`
	ExecutionSeq             uint64 `json:"execution_seq"`
	Status                   string `json:"status"`
	ActiveCheckpointID       string `json:"active_checkpoint_id,omitempty"`
	NextCheckpointID         string `json:"next_checkpoint_id,omitempty"`
	ActiveAttemptID          string `json:"active_attempt_id,omitempty"`
	ContinuationMode         string `json:"continuation_mode,omitempty"`
	PauseAfterCurrent        bool   `json:"pause_after_current,omitempty"`
	CompletedCheckpointCount uint64 `json:"completed_checkpoint_count,omitempty"`
	BlockedReasonCode        string `json:"blocked_reason_code,omitempty"`
	UpdatedAt                int64  `json:"updated_at"`
}

type CheckpointExecution struct {
	SchemaVersion   int    `json:"schema_version"`
	SessionID       string `json:"session_id"`
	PlanID          string `json:"plan_id"`
	CheckpointID    string `json:"checkpoint_id"`
	ExecutionSeq    uint64 `json:"execution_seq"`
	Status          string `json:"status"`
	AttemptNumber   uint64 `json:"attempt_number,omitempty"`
	ActiveAttemptID string `json:"active_attempt_id,omitempty"`
	ActiveSubtaskID string `json:"active_subtask_id,omitempty"`
	NextSubtaskID   string `json:"next_subtask_id,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	EpochID         string `json:"epoch_id,omitempty"`
	RunSessionID    string `json:"run_session_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	StartedAt       int64  `json:"started_at,omitempty"`
	TerminalAt      int64  `json:"terminal_at,omitempty"`
	OutcomeCode     string `json:"outcome_code,omitempty"`
	EvidenceRef     string `json:"evidence_ref,omitempty"`
	ReviewState     string `json:"review_state,omitempty"`
}

type SubtaskExecution struct {
	SchemaVersion int    `json:"schema_version"`
	SessionID     string `json:"session_id"`
	PlanID        string `json:"plan_id"`
	CheckpointID  string `json:"checkpoint_id"`
	SubtaskID     string `json:"subtask_id"`
	ExecutionSeq  uint64 `json:"execution_seq"`
	Status        string `json:"status"`
	AttemptID     string `json:"attempt_id,omitempty"`
	StartedAt     int64  `json:"started_at,omitempty"`
	CompletedAt   int64  `json:"completed_at,omitempty"`
}

type PlanExecutionRunEpochLink struct {
	SessionID       string `json:"session_id"`
	PlanID          string `json:"plan_id"`
	ExecutionSeq    uint64 `json:"execution_seq"`
	CheckpointID    string `json:"checkpoint_id"`
	AttemptID       string `json:"attempt_id,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	EpochID         string `json:"epoch_id,omitempty"`
	RunSessionID    string `json:"run_session_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
}

type PlanExecutionDelta struct {
	SummaryChange    PlanExecutionSummary       `json:"summary_change"`
	CheckpointChange *CheckpointExecution       `json:"checkpoint_change,omitempty"`
	SubtaskChanges   []SubtaskExecution         `json:"subtask_changes,omitempty"`
	RunEpochLink     *PlanExecutionRunEpochLink `json:"run_epoch_link,omitempty"`
	NextAction       string                     `json:"next_action,omitempty"`
}

type PlanExecutionEvent struct {
	SchemaVersion      int                `json:"schema_version"`
	SessionID          string             `json:"session_id"`
	PlanID             string             `json:"plan_id"`
	ExecutionSeq       uint64             `json:"execution_seq"`
	EventID            string             `json:"event_id"`
	EventType          string             `json:"event_type"`
	DefinitionRevision uint64             `json:"definition_revision"`
	ClientRequestID    string             `json:"client_request_id"`
	PayloadHash        string             `json:"payload_hash"`
	CheckpointID       string             `json:"checkpoint_id,omitempty"`
	SubtaskIDs         []string           `json:"subtask_ids,omitempty"`
	ResultDelta        PlanExecutionDelta `json:"result_delta"`
	ActorID            string             `json:"actor_id,omitempty"`
	OccurredAt         int64              `json:"occurred_at"`
}

type PlanExecutionMutationResult struct {
	SessionID        string                     `json:"session_id"`
	PlanID           string                     `json:"plan_id"`
	ExecutionSeq     uint64                     `json:"execution_seq"`
	EventID          string                     `json:"event_id"`
	EventType        string                     `json:"event_type"`
	Replayed         bool                       `json:"replayed,omitempty"`
	CheckpointChange *CheckpointExecution       `json:"checkpoint_change,omitempty"`
	SubtaskChanges   []SubtaskExecution         `json:"subtask_changes,omitempty"`
	SummaryChange    PlanExecutionSummary       `json:"summary_change"`
	RunEpochLink     *PlanExecutionRunEpochLink `json:"run_epoch_link,omitempty"`
	NextAction       string                     `json:"next_action,omitempty"`
}

type PlanExecutionCommand struct {
	SessionID            string
	PlanID               string
	AccountScopeID       string
	ExpectedExecutionSeq uint64
	ClientRequestID      string
	PayloadHash          string
	ActorID              string
	DefinitionRevision   uint64
	EventType            string
	CheckpointID         string
	NextSummary          PlanExecutionSummary
	CheckpointChange     *CheckpointExecution
	SubtaskChanges       []SubtaskExecution
	RunEpochLink         *PlanExecutionRunEpochLink
	NextAction           string
	NowUnixMs            int64
}

type planExecutionIdempotencyRecord struct {
	PayloadHash string                      `json:"payload_hash"`
	Result      PlanExecutionMutationResult `json:"result"`
	CreatedAt   int64                       `json:"created_at"`
}

type PlanExecutionRealtimeOutboxRecord struct {
	Protocol           string               `json:"protocol"`
	ProtocolVersion    int                  `json:"protocol_version"`
	Kind               string               `json:"kind"`
	SchemaVersion      int                  `json:"schema_version"`
	SessionID          string               `json:"session_id"`
	PlanID             string               `json:"plan_id"`
	DefinitionRevision uint64               `json:"definition_revision"`
	ExecutionSeq       uint64               `json:"execution_seq"`
	EventID            string               `json:"event_id"`
	EventType          string               `json:"event_type"`
	CheckpointID       string               `json:"checkpoint_id,omitempty"`
	SubtaskIDs         []string             `json:"subtask_ids,omitempty"`
	CheckpointChange   *CheckpointExecution `json:"checkpoint_change,omitempty"`
	SubtaskChanges     []SubtaskExecution   `json:"subtask_changes,omitempty"`
	SummaryChange      PlanExecutionSummary `json:"summary_change"`
	NextAction         string               `json:"next_action,omitempty"`
	CreatedAt          int64                `json:"created_at"`
}

type PlanExecutionEventPage struct {
	Events       []PlanExecutionEvent `json:"events"`
	NextAfterSeq uint64               `json:"next_after_seq"`
	HasMore      bool                 `json:"has_more"`
	EncodedBytes int                  `json:"encoded_bytes"`
}

// PlanExecutionOutboxPage is the compact durable replay surface consumed by
// V3 clients. Records contain only the changed projection rows and summary.
type PlanExecutionOutboxPage struct {
	Records      []PlanExecutionRealtimeOutboxRecord `json:"records"`
	NextAfterSeq uint64                              `json:"next_after_seq"`
	HasMore      bool                                `json:"has_more"`
	EncodedBytes int                                 `json:"encoded_bytes"`
}

// PlanRuntimeHydration is a current materialized view. It deliberately keeps
// immutable definition rows separate from execution projections and is built
// from point/prefix projection reads, never by replaying execution events.
type PlanRuntimeHydration struct {
	SchemaVersion         int                             `json:"schema_version"`
	Definition            PlanDefinition                  `json:"definition"`
	CheckpointDefinitions map[string]CheckpointDefinition `json:"checkpoint_definitions"`
	SubtaskDefinitions    map[string]SubtaskDefinition    `json:"subtask_definitions"`
	Summary               PlanExecutionSummary            `json:"summary"`
	CheckpointExecutions  map[string]CheckpointExecution  `json:"checkpoint_executions"`
	SubtaskExecutions     map[string]SubtaskExecution     `json:"subtask_executions"`
}

type PlanExecutionSnapshot struct {
	SchemaVersion           int                   `json:"schema_version"`
	ProjectionSchemaVersion int                   `json:"projection_schema_version"`
	SessionID               string                `json:"session_id"`
	PlanID                  string                `json:"plan_id"`
	DefinitionRevision      uint64                `json:"definition_revision"`
	ExecutionSeq            uint64                `json:"execution_seq"`
	Summary                 PlanExecutionSummary  `json:"summary"`
	CheckpointExecutions    []CheckpointExecution `json:"checkpoint_executions,omitempty"`
	SubtaskExecutions       []SubtaskExecution    `json:"subtask_executions,omitempty"`
	CreatedAt               int64                 `json:"created_at"`
	ContentHash             string                `json:"content_hash"`
}

type planExecutionSnapshotPointer struct {
	ProjectionSchemaVersion int    `json:"projection_schema_version"`
	ExecutionSeq            uint64 `json:"execution_seq"`
	SnapshotKey             string `json:"snapshot_key"`
	CreatedAt               int64  `json:"created_at"`
	ContentHash             string `json:"content_hash"`
}

type PlanExecutionRecovery struct {
	SnapshotSeq uint64
	SnapshotAge time.Duration
	TailEvents  int
	TailBytes   int
	Summary     PlanExecutionSummary
	Checkpoints map[string]CheckpointExecution
	Subtasks    map[string]SubtaskExecution
}

const planRuntimeKeyspace = "v3/plan_runtime/v1"

// planRuntimeKeyPart preserves opaque ID case and makes every component prefix-safe.
func planRuntimeKeyPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(value)))
}
func planRuntimeBase(sessionID, planID string) string {
	return fmt.Sprintf("%s/%s/%s", planRuntimeKeyspace, planRuntimeKeyPart(sessionID), planRuntimeKeyPart(planID))
}
func KeyPlanExecutionSummary(sessionID, planID string) string {
	return planRuntimeBase(sessionID, planID) + "/execution/summary"
}
func KeyPlanExecutionEvent(sessionID, planID string, seq uint64) string {
	return fmt.Sprintf("%s/execution/event/%020d", planRuntimeBase(sessionID, planID), seq)
}
func PlanExecutionEventPrefix(sessionID, planID string) string {
	return planRuntimeBase(sessionID, planID) + "/execution/event/"
}
func KeyPlanCheckpointExecution(sessionID, planID, checkpointID string) string {
	return planRuntimeBase(sessionID, planID) + "/projection/checkpoint/" + planRuntimeKeyPart(checkpointID)
}
func PlanCheckpointExecutionPrefix(sessionID, planID string) string {
	return planRuntimeBase(sessionID, planID) + "/projection/checkpoint/"
}
func KeyPlanSubtaskExecution(sessionID, planID, checkpointID, subtaskID string) string {
	return planRuntimeBase(sessionID, planID) + "/projection/subtask/" + planRuntimeKeyPart(checkpointID) + "/" + planRuntimeKeyPart(subtaskID)
}
func PlanSubtaskExecutionPrefix(sessionID, planID string) string {
	return planRuntimeBase(sessionID, planID) + "/projection/subtask/"
}
func KeyPlanExecutionIdempotency(accountScopeID, sessionID, planID, clientRequestID string) string {
	return fmt.Sprintf("%s/idempotency/%s/%s/%s/%s", planRuntimeKeyspace, planRuntimeKeyPart(accountScopeID), planRuntimeKeyPart(sessionID), planRuntimeKeyPart(planID), planRuntimeKeyPart(clientRequestID))
}
func KeyPlanExecutionOutbox(sessionID, planID string, seq uint64) string {
	return fmt.Sprintf("%s/realtime/outbox/%020d", planRuntimeBase(sessionID, planID), seq)
}
func PlanExecutionOutboxPrefix(sessionID, planID string) string {
	return planRuntimeBase(sessionID, planID) + "/realtime/outbox/"
}
func KeyPlanExecutionRunEpochLink(sessionID, planID string, seq uint64) string {
	return fmt.Sprintf("%s/execution/run_epoch_link/%020d", planRuntimeBase(sessionID, planID), seq)
}
func KeyPlanExecutionSnapshot(sessionID, planID string, seq uint64) string {
	return fmt.Sprintf("%s/snapshot/%020d", planRuntimeBase(sessionID, planID), seq)
}
func KeyPlanExecutionCompatibleSnapshot(sessionID, planID string, projectionVersion int) string {
	return fmt.Sprintf("%s/snapshot_compatible/%020d", planRuntimeBase(sessionID, planID), projectionVersion)
}

func validatePlanRuntimeID(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > 128 {
		return fmt.Errorf("%s exceeds 128 bytes", name)
	}
	return nil
}

func getPlanRuntimeJSON(reader pebble.Reader, key string, out any) (bool, error) {
	ok, err := getJSONFromReader(reader, key, out)
	if err != nil {
		return false, fmt.Errorf("read plan runtime key %q: %w", key, err)
	}
	return ok, nil
}

func (s *SessionStore) GetPlanExecutionSummary(sessionID, planID string) (PlanExecutionSummary, bool, error) {
	var value PlanExecutionSummary
	if s == nil || s.store == nil {
		return value, false, errors.New("session store is not configured")
	}
	ok, err := getPlanRuntimeJSON(s.store.db, KeyPlanExecutionSummary(sessionID, planID), &value)
	return value, ok, err
}
func (s *SessionStore) GetPlanCheckpointExecution(sessionID, planID, checkpointID string) (CheckpointExecution, bool, error) {
	var value CheckpointExecution
	ok, err := getPlanRuntimeJSON(s.store.db, KeyPlanCheckpointExecution(sessionID, planID, checkpointID), &value)
	return value, ok, err
}
func (s *SessionStore) GetPlanSubtaskExecution(sessionID, planID, checkpointID, subtaskID string) (SubtaskExecution, bool, error) {
	var value SubtaskExecution
	ok, err := getPlanRuntimeJSON(s.store.db, KeyPlanSubtaskExecution(sessionID, planID, checkpointID, subtaskID), &value)
	return value, ok, err
}
func (s *SessionStore) GetPlanExecutionEvent(sessionID, planID string, seq uint64) (PlanExecutionEvent, bool, error) {
	var value PlanExecutionEvent
	ok, err := getPlanRuntimeJSON(s.store.db, KeyPlanExecutionEvent(sessionID, planID, seq), &value)
	return value, ok, err
}

// ListPlanExecutionEventsAfter is bounded by both records and encoded bytes.
func (s *SessionStore) ListPlanExecutionEventsAfter(sessionID, planID string, afterSeq uint64, limit int) (PlanExecutionEventPage, error) {
	if limit <= 0 || limit > PlanRuntimeMaxEventPage {
		limit = PlanRuntimeMaxEventPage
	}
	page := PlanExecutionEventPage{NextAfterSeq: afterSeq}
	prefix := PlanExecutionEventPrefix(sessionID, planID)
	start := KeyPlanExecutionEvent(sessionID, planID, afterSeq+1)
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: prefix, StartKey: start, Limit: limit + 1}, func(_ string, raw []byte) (bool, error) {
		if len(page.Events) >= limit || (len(page.Events) > 0 && page.EncodedBytes+len(raw) > PlanRuntimeMaxEventPageBytes) {
			page.HasMore = true
			return false, nil
		}
		var event PlanExecutionEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return false, fmt.Errorf("decode plan execution event: %w", err)
		}
		if event.ExecutionSeq <= page.NextAfterSeq {
			return false, fmt.Errorf("plan execution event sequence is not monotonic")
		}
		page.Events = append(page.Events, event)
		page.NextAfterSeq = event.ExecutionSeq
		page.EncodedBytes += len(raw)
		return true, nil
	})
	return page, err
}

// ListPlanExecutionOutboxAfter replays compact committed deltas in execution
// sequence order. It has the same explicit record/byte bounds as event reads.
func (s *SessionStore) ListPlanExecutionOutboxAfter(sessionID, planID string, afterSeq uint64, limit int) (PlanExecutionOutboxPage, error) {
	if limit <= 0 || limit > PlanRuntimeMaxEventPage {
		limit = PlanRuntimeMaxEventPage
	}
	page := PlanExecutionOutboxPage{NextAfterSeq: afterSeq}
	prefix := PlanExecutionOutboxPrefix(sessionID, planID)
	start := KeyPlanExecutionOutbox(sessionID, planID, afterSeq+1)
	err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: prefix, StartKey: start, Limit: limit + 1}, func(_ string, raw []byte) (bool, error) {
		if len(page.Records) >= limit || (len(page.Records) > 0 && page.EncodedBytes+len(raw) > PlanRuntimeMaxEventPageBytes) {
			page.HasMore = true
			return false, nil
		}
		var record PlanExecutionRealtimeOutboxRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return false, fmt.Errorf("decode plan execution outbox: %w", err)
		}
		if record.Protocol != PlanExecutionRealtimeProtocol || record.ProtocolVersion != PlanRuntimeSchemaVersion || record.Kind != PlanExecutionRealtimeKindDelta {
			return false, errors.New("plan execution outbox uses an unsupported realtime schema")
		}
		if record.ExecutionSeq != page.NextAfterSeq+1 {
			return false, fmt.Errorf("plan execution outbox gap after sequence %d", page.NextAfterSeq)
		}
		page.Records = append(page.Records, record)
		page.NextAfterSeq = record.ExecutionSeq
		page.EncodedBytes += len(raw)
		return true, nil
	})
	return page, err
}

// HydratePlanRuntime reads the immutable definition and current materialized
// projection. It never reads the execution event stream or legacy plan state.
func (s *SessionStore) HydratePlanRuntime(sessionID, planID string, definitionRevision uint64) (PlanRuntimeHydration, bool, error) {
	definition, ok, err := s.GetPlanDefinition(sessionID, planID, definitionRevision)
	if err != nil || !ok {
		return PlanRuntimeHydration{}, ok, err
	}
	out := PlanRuntimeHydration{
		SchemaVersion: PlanRuntimeSchemaVersion, Definition: definition,
		CheckpointDefinitions: make(map[string]CheckpointDefinition, len(definition.CheckpointOrder)),
		SubtaskDefinitions:    make(map[string]SubtaskDefinition),
		CheckpointExecutions:  make(map[string]CheckpointExecution),
		SubtaskExecutions:     make(map[string]SubtaskExecution),
	}
	for _, checkpointID := range definition.CheckpointOrder {
		checkpoint, found, getErr := s.GetPlanCheckpointDefinition(sessionID, planID, definitionRevision, checkpointID)
		if getErr != nil {
			return PlanRuntimeHydration{}, false, getErr
		}
		if !found {
			return PlanRuntimeHydration{}, false, fmt.Errorf("plan runtime definition references missing checkpoint %q", checkpointID)
		}
		out.CheckpointDefinitions[checkpointID] = checkpoint
		for _, subtaskID := range checkpoint.SubtaskOrder {
			subtask, subtaskFound, subtaskErr := s.GetPlanSubtaskDefinition(sessionID, planID, definitionRevision, checkpointID, subtaskID)
			if subtaskErr != nil {
				return PlanRuntimeHydration{}, false, subtaskErr
			}
			if !subtaskFound {
				return PlanRuntimeHydration{}, false, fmt.Errorf("checkpoint %q references missing subtask %q", checkpointID, subtaskID)
			}
			out.SubtaskDefinitions[checkpointID+"\x00"+subtaskID] = subtask
		}
	}
	if summary, found, getErr := s.GetPlanExecutionSummary(sessionID, planID); getErr != nil {
		return PlanRuntimeHydration{}, false, getErr
	} else if found {
		out.Summary = summary
	}
	if err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: PlanCheckpointExecutionPrefix(sessionID, planID)}, func(_ string, raw []byte) (bool, error) {
		var value CheckpointExecution
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, err
		}
		out.CheckpointExecutions[value.CheckpointID] = value
		return true, nil
	}); err != nil {
		return PlanRuntimeHydration{}, false, fmt.Errorf("read checkpoint execution projection: %w", err)
	}
	if err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: PlanSubtaskExecutionPrefix(sessionID, planID)}, func(_ string, raw []byte) (bool, error) {
		var value SubtaskExecution
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, err
		}
		out.SubtaskExecutions[value.CheckpointID+"\x00"+value.SubtaskID] = value
		return true, nil
	}); err != nil {
		return PlanRuntimeHydration{}, false, fmt.Errorf("read subtask execution projection: %w", err)
	}
	return out, true, nil
}

func (s *SessionStore) AppendPlanExecution(input PlanExecutionCommand) (PlanExecutionMutationResult, error) {
	started := time.Now()
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.PlanID = strings.TrimSpace(input.PlanID)
	input.AccountScopeID = strings.TrimSpace(input.AccountScopeID)
	input.ClientRequestID = strings.TrimSpace(input.ClientRequestID)
	input.PayloadHash = strings.TrimSpace(input.PayloadHash)
	input.EventType = strings.TrimSpace(input.EventType)
	input.CheckpointID = strings.TrimSpace(input.CheckpointID)
	if s == nil || s.store == nil {
		return PlanExecutionMutationResult{}, errors.New("session store is not configured")
	}
	for name, value := range map[string]string{"session id": input.SessionID, "plan id": input.PlanID, "client request id": input.ClientRequestID, "payload hash": input.PayloadHash, "event type": input.EventType} {
		if err := validatePlanRuntimeID(name, value); err != nil {
			return PlanExecutionMutationResult{}, err
		}
	}
	if input.DefinitionRevision == 0 {
		return PlanExecutionMutationResult{}, errors.New("definition revision is required")
	}
	if len(input.SubtaskChanges) > PlanRuntimeMaxSubtaskChanges {
		return PlanExecutionMutationResult{}, fmt.Errorf("subtask changes exceed %d", PlanRuntimeMaxSubtaskChanges)
	}

	unlock := s.store.sessionMutations.lockSessions(input.SessionID)
	defer unlock()
	idemKey := KeyPlanExecutionIdempotency(input.AccountScopeID, input.SessionID, input.PlanID, input.ClientRequestID)
	var idem planExecutionIdempotencyRecord
	if ok, err := getPlanRuntimeJSON(s.store.db, idemKey, &idem); err != nil {
		return PlanExecutionMutationResult{}, err
	} else if ok {
		if idem.PayloadHash != input.PayloadHash {
			observePlanRuntimeConflict()
			return PlanExecutionMutationResult{}, ErrPlanRuntimeIdempotencyConflict
		}
		observePlanRuntimeReplay()
		idem.Result.Replayed = true
		return idem.Result, nil
	}

	current, exists, err := s.GetPlanExecutionSummary(input.SessionID, input.PlanID)
	if err != nil {
		return PlanExecutionMutationResult{}, err
	}
	currentSeq := uint64(0)
	currentStatus := "absent"
	if exists {
		currentSeq, currentStatus = current.ExecutionSeq, current.Status
	}
	if currentSeq != input.ExpectedExecutionSeq {
		observePlanRuntimeConflict()
		return PlanExecutionMutationResult{}, &PlanRuntimeExecutionConflictError{Expected: input.ExpectedExecutionSeq, Current: currentSeq, Status: currentStatus}
	}
	seq := currentSeq + 1
	reservedOutbox, err := s.store.sessionMutations.reserveOutbox(s.store, 1)
	if err != nil {
		return PlanExecutionMutationResult{}, err
	}
	outboxReservationCommitted := false
	defer func() {
		if !outboxReservationCommitted {
			s.store.sessionMutations.abandonOutbox(reservedOutbox)
		}
	}()
	rootSeq, err := s.readV3SessionSequence(input.SessionID)
	if err != nil {
		return PlanExecutionMutationResult{}, err
	}
	session, sessionFound, err := s.GetSession(input.SessionID)
	if err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if !sessionFound {
		return PlanExecutionMutationResult{}, fmt.Errorf("session %q not found", input.SessionID)
	}
	if input.AccountScopeID != "" && strings.TrimSpace(session.AccountScopeID) != input.AccountScopeID {
		return PlanExecutionMutationResult{}, errors.New("plan runtime account scope does not match session authority")
	}
	now := input.NowUnixMs
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	summary := input.NextSummary
	summary.SchemaVersion = PlanRuntimeSchemaVersion
	summary.SessionID, summary.PlanID = input.SessionID, input.PlanID
	summary.DefinitionRevision, summary.ExecutionSeq, summary.UpdatedAt = input.DefinitionRevision, seq, now
	if strings.TrimSpace(summary.Status) == "" {
		return PlanExecutionMutationResult{}, errors.New("next summary status is required")
	}
	var checkpoint *CheckpointExecution
	if input.CheckpointChange != nil {
		value := *input.CheckpointChange
		value.CheckpointID = strings.TrimSpace(value.CheckpointID)
		if err := validatePlanRuntimeID("checkpoint id", value.CheckpointID); err != nil {
			return PlanExecutionMutationResult{}, err
		}
		if input.CheckpointID != "" && input.CheckpointID != value.CheckpointID {
			return PlanExecutionMutationResult{}, errors.New("checkpoint target does not match checkpoint projection")
		}
		value.SchemaVersion, value.SessionID, value.PlanID, value.ExecutionSeq = PlanRuntimeSchemaVersion, input.SessionID, input.PlanID, seq
		checkpoint = &value
	}
	seenSubtasks := make(map[string]struct{}, len(input.SubtaskChanges))
	subtasks := make([]SubtaskExecution, len(input.SubtaskChanges))
	subtaskIDs := make([]string, len(input.SubtaskChanges))
	for i, change := range input.SubtaskChanges {
		change.CheckpointID, change.SubtaskID = strings.TrimSpace(change.CheckpointID), strings.TrimSpace(change.SubtaskID)
		if err := validatePlanRuntimeID("subtask checkpoint id", change.CheckpointID); err != nil {
			return PlanExecutionMutationResult{}, err
		}
		if err := validatePlanRuntimeID("subtask id", change.SubtaskID); err != nil {
			return PlanExecutionMutationResult{}, err
		}
		if input.CheckpointID != "" && input.CheckpointID != change.CheckpointID {
			return PlanExecutionMutationResult{}, errors.New("subtask target belongs to a different checkpoint")
		}
		identity := change.CheckpointID + "\x00" + change.SubtaskID
		if _, duplicate := seenSubtasks[identity]; duplicate {
			return PlanExecutionMutationResult{}, fmt.Errorf("duplicate subtask target %q", change.SubtaskID)
		}
		seenSubtasks[identity] = struct{}{}
		change.SchemaVersion, change.SessionID, change.PlanID, change.ExecutionSeq = PlanRuntimeSchemaVersion, input.SessionID, input.PlanID, seq
		subtasks[i], subtaskIDs[i] = change, change.SubtaskID
	}
	var link *PlanExecutionRunEpochLink
	if input.RunEpochLink != nil {
		value := *input.RunEpochLink
		value.SessionID, value.PlanID, value.ExecutionSeq = input.SessionID, input.PlanID, seq
		if strings.TrimSpace(value.RunID) == "" && strings.TrimSpace(value.EpochID) == "" {
			return PlanExecutionMutationResult{}, errors.New("run/epoch link requires a run id or epoch id")
		}
		if value.EpochID != "" {
			if epoch, ok, getErr := s.GetExecutionEpoch(input.SessionID, value.EpochID); getErr != nil {
				return PlanExecutionMutationResult{}, getErr
			} else if !ok || epoch.SessionID != input.SessionID {
				return PlanExecutionMutationResult{}, fmt.Errorf("execution epoch %q not found", value.EpochID)
			}
		}
		link = &value
	}

	delta := PlanExecutionDelta{SummaryChange: summary, CheckpointChange: checkpoint, SubtaskChanges: subtasks, RunEpochLink: link, NextAction: strings.TrimSpace(input.NextAction)}
	event := PlanExecutionEvent{SchemaVersion: PlanRuntimeSchemaVersion, SessionID: input.SessionID, PlanID: input.PlanID, ExecutionSeq: seq, EventID: fmt.Sprintf("planexec_%s_%020d", shortPlanRuntimeID(input.PlanID), seq), EventType: input.EventType, DefinitionRevision: input.DefinitionRevision, ClientRequestID: input.ClientRequestID, PayloadHash: input.PayloadHash, CheckpointID: input.CheckpointID, SubtaskIDs: subtaskIDs, ResultDelta: delta, ActorID: strings.TrimSpace(input.ActorID), OccurredAt: now}
	result := PlanExecutionMutationResult{SessionID: input.SessionID, PlanID: input.PlanID, ExecutionSeq: seq, EventID: event.EventID, EventType: event.EventType, CheckpointChange: checkpoint, SubtaskChanges: subtasks, SummaryChange: summary, RunEpochLink: link, NextAction: delta.NextAction}
	outbox := PlanExecutionRealtimeOutboxRecord{Protocol: PlanExecutionRealtimeProtocol, ProtocolVersion: PlanRuntimeSchemaVersion, Kind: PlanExecutionRealtimeKindDelta, SchemaVersion: PlanRuntimeSchemaVersion, SessionID: input.SessionID, PlanID: input.PlanID, DefinitionRevision: input.DefinitionRevision, ExecutionSeq: seq, EventID: event.EventID, EventType: event.EventType, CheckpointID: input.CheckpointID, SubtaskIDs: subtaskIDs, CheckpointChange: checkpoint, SubtaskChanges: subtasks, SummaryChange: summary, NextAction: delta.NextAction, CreatedAt: now}
	idem = planExecutionIdempotencyRecord{PayloadHash: input.PayloadHash, Result: result, CreatedAt: now}
	rootEventPayload, err := json.Marshal(map[string]any{"plan_execution": outbox})
	if err != nil {
		return PlanExecutionMutationResult{}, fmt.Errorf("marshal plan runtime realtime payload: %w", err)
	}
	rootEventSeq := rootSeq + 1
	rootEvent := V3SessionEvent{ID: fmt.Sprintf("v3evt_%s_%020d", input.SessionID, rootEventSeq), SessionID: input.SessionID, Seq: rootEventSeq, EventType: PlanExecutionRealtimeKindDelta, Payload: rootEventPayload, TsUnixMs: now}
	var activeEpoch *ExecutionEpoch
	if epoch, ok, getErr := s.GetActiveExecutionEpoch(input.SessionID); getErr != nil {
		return PlanExecutionMutationResult{}, getErr
	} else if ok {
		rootEvent.EpochID = epoch.EpochID
		epoch.LastRootSeq, epoch.UpdatedAt = rootEventSeq, now
		activeEpoch = &epoch
	}
	rootProjection := V3SessionProjection{SessionID: input.SessionID, LastEventSeq: rootEventSeq, ProjectionHighWatermarkSeq: rootEventSeq, UpdatedAt: now}
	globalOutbox := V3RealtimeOutboxRecord{EndpointSeq: reservedOutbox[0], EndpointCursor: V3RealtimeOutboxCursor(reservedOutbox[0]), SessionID: input.SessionID, UserID: strings.TrimSpace(session.UserID), AccountScopeID: strings.TrimSpace(session.AccountScopeID), Membership: newV3RealtimeOutboxMembershipFromSession(session, now), Event: rootEvent, Projection: rootProjection, CreatedAt: now}
	globalOutboxRaw, err := json.Marshal(globalOutbox)
	if err != nil {
		return PlanExecutionMutationResult{}, fmt.Errorf("marshal global plan runtime outbox: %w", err)
	}
	globalOutboxRef, err := marshalV3RealtimeOutboxReference(globalOutbox)
	if err != nil {
		return PlanExecutionMutationResult{}, fmt.Errorf("marshal global plan runtime outbox reference: %w", err)
	}
	rootEventRaw, err := json.Marshal(rootEvent)
	if err != nil {
		return PlanExecutionMutationResult{}, fmt.Errorf("marshal root plan runtime event: %w", err)
	}
	rootProjectionRaw, err := json.Marshal(rootProjection)
	if err != nil {
		return PlanExecutionMutationResult{}, fmt.Errorf("marshal root plan runtime projection: %w", err)
	}

	eventRaw, err := json.Marshal(event)
	if err != nil {
		return PlanExecutionMutationResult{}, err
	}
	summaryRaw, err := json.Marshal(summary)
	if err != nil {
		return PlanExecutionMutationResult{}, err
	}
	outboxRaw, err := json.Marshal(outbox)
	if err != nil {
		return PlanExecutionMutationResult{}, err
	}
	resultRaw, err := json.Marshal(idem)
	if err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if len(eventRaw) > PlanRuntimeMaxEventBytes {
		return PlanExecutionMutationResult{}, fmt.Errorf("plan execution event exceeds %d bytes", PlanRuntimeMaxEventBytes)
	}
	if len(outboxRaw) > PlanRuntimeMaxOutboxBytes {
		return PlanExecutionMutationResult{}, fmt.Errorf("plan execution outbox exceeds %d bytes", PlanRuntimeMaxOutboxBytes)
	}
	if len(resultRaw) > PlanRuntimeMaxResultBytes {
		return PlanExecutionMutationResult{}, fmt.Errorf("plan execution result exceeds %d bytes", PlanRuntimeMaxResultBytes)
	}

	batch := s.store.NewBatch() // intentionally unindexed; all validation reads precede writes
	defer batch.Close()
	keys, logicalBytes, projectionBytes := 0, 0, len(summaryRaw)
	set := func(key string, raw []byte) error {
		keys++
		logicalBytes += len(key) + len(raw)
		if keys > PlanRuntimeMaxKeysPerCommit || logicalBytes > PlanRuntimeMaxBatchBytes {
			return fmt.Errorf("plan runtime mutation exceeds batch budget (%d keys, %d bytes)", keys, logicalBytes)
		}
		return batch.Set([]byte(key), raw, nil)
	}
	if err := set(KeyPlanExecutionEvent(input.SessionID, input.PlanID, seq), eventRaw); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if err := set(KeyPlanExecutionSummary(input.SessionID, input.PlanID), summaryRaw); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if checkpoint != nil {
		raw, marshalErr := json.Marshal(checkpoint)
		if marshalErr != nil {
			return PlanExecutionMutationResult{}, marshalErr
		}
		projectionBytes += len(raw)
		if err := set(KeyPlanCheckpointExecution(input.SessionID, input.PlanID, checkpoint.CheckpointID), raw); err != nil {
			return PlanExecutionMutationResult{}, err
		}
	}
	for _, subtask := range subtasks {
		raw, marshalErr := json.Marshal(subtask)
		if marshalErr != nil {
			return PlanExecutionMutationResult{}, marshalErr
		}
		projectionBytes += len(raw)
		if err := set(KeyPlanSubtaskExecution(input.SessionID, input.PlanID, subtask.CheckpointID, subtask.SubtaskID), raw); err != nil {
			return PlanExecutionMutationResult{}, err
		}
	}
	if link != nil {
		raw, marshalErr := json.Marshal(link)
		if marshalErr != nil {
			return PlanExecutionMutationResult{}, marshalErr
		}
		if err := set(KeyPlanExecutionRunEpochLink(input.SessionID, input.PlanID, seq), raw); err != nil {
			return PlanExecutionMutationResult{}, err
		}
	}
	if err := set(idemKey, resultRaw); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if err := set(KeyPlanExecutionOutbox(input.SessionID, input.PlanID, seq), outboxRaw); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if err := set(KeyV3SessionSequence(input.SessionID), uint64ToBytes(rootEventSeq)); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if err := set(KeyV3SessionEvent(input.SessionID, rootEventSeq), rootEventRaw); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if err := set(KeyV3SessionProjection(input.SessionID), rootProjectionRaw); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	if err := set(KeyV3RealtimeOutbox(globalOutbox.EndpointSeq), globalOutboxRaw); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	for _, key := range []string{
		KeyV3RealtimeOutboxBySessionEndpoint(input.SessionID, globalOutbox.EndpointSeq),
		KeyV3RealtimeOutboxBySessionSeq(input.SessionID, rootEventSeq),
		KeyV3RealtimeOutboxByAuthScope(globalOutbox.AccountScopeID, globalOutbox.UserID, globalOutbox.EndpointSeq),
	} {
		if err := set(key, globalOutboxRef); err != nil {
			return PlanExecutionMutationResult{}, err
		}
	}
	if activeEpoch != nil {
		if err := setExecutionEpochInBatch(batch, *activeEpoch, true); err != nil {
			return PlanExecutionMutationResult{}, err
		}
		// setExecutionEpochInBatch writes four bounded keys outside the local
		// accounting helper; account for them in the mutation budget.
		keys += 4
		if keys > PlanRuntimeMaxKeysPerCommit {
			return PlanExecutionMutationResult{}, fmt.Errorf("plan runtime mutation exceeds batch budget (%d keys)", keys)
		}
	}
	if hook := s.store.sessionMutations.beforePlanRuntimeCommit; hook != nil {
		if err := hook(input.SessionID, input.PlanID); err != nil {
			return PlanExecutionMutationResult{}, err
		}
	}
	commitStarted := time.Now()
	if err := batch.Commit(pebble.Sync); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	outboxReservationCommitted = true
	if err := s.store.sessionMutations.commitOutbox(s.store, reservedOutbox); err != nil {
		return PlanExecutionMutationResult{}, err
	}
	observePlanRuntimeMutation(planRuntimeMutationObservation{eventBytes: len(eventRaw), projectionBytes: projectionBytes, outboxBytes: len(outboxRaw) + len(globalOutboxRaw), resultBytes: len(resultRaw), logicalBytes: logicalBytes, keys: keys, targets: len(subtasks) + boolInt(checkpoint != nil), commitDuration: time.Since(commitStarted), totalDuration: time.Since(started)})
	return result, nil
}

func shortPlanRuntimeID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *SessionStore) RecoverPlanExecution(sessionID, planID string) (PlanExecutionRecovery, error) {
	started := time.Now()
	recovery := PlanExecutionRecovery{Checkpoints: make(map[string]CheckpointExecution), Subtasks: make(map[string]SubtaskExecution)}
	var pointer planExecutionSnapshotPointer
	pointerKey := KeyPlanExecutionCompatibleSnapshot(sessionID, planID, PlanRuntimeProjectionSchemaVersion)
	if ok, err := getPlanRuntimeJSON(s.store.db, pointerKey, &pointer); err != nil {
		return recovery, err
	} else if ok && pointer.ProjectionSchemaVersion == PlanRuntimeProjectionSchemaVersion {
		var snapshot PlanExecutionSnapshot
		if found, getErr := getPlanRuntimeJSON(s.store.db, pointer.SnapshotKey, &snapshot); getErr != nil {
			return recovery, getErr
		} else if !found {
			return recovery, errors.New("compatible plan execution snapshot pointer is dangling")
		}
		if snapshot.ProjectionSchemaVersion != PlanRuntimeProjectionSchemaVersion || snapshot.ContentHash != pointer.ContentHash {
			return recovery, errors.New("compatible plan execution snapshot is inconsistent")
		}
		contentHash, hashErr := planRuntimeSnapshotContentHash(snapshot)
		if hashErr != nil {
			return recovery, hashErr
		}
		if contentHash != snapshot.ContentHash {
			return recovery, errors.New("compatible plan execution snapshot content hash does not match")
		}
		recovery.SnapshotSeq, recovery.Summary = snapshot.ExecutionSeq, snapshot.Summary
		if snapshot.CreatedAt > 0 {
			recovery.SnapshotAge = time.Since(time.UnixMilli(snapshot.CreatedAt))
		}
		for _, checkpoint := range snapshot.CheckpointExecutions {
			recovery.Checkpoints[checkpoint.CheckpointID] = checkpoint
		}
		for _, subtask := range snapshot.SubtaskExecutions {
			recovery.Subtasks[subtask.CheckpointID+"\x00"+subtask.SubtaskID] = subtask
		}
	}
	after := recovery.SnapshotSeq
	for {
		page, err := s.ListPlanExecutionEventsAfter(sessionID, planID, after, PlanRuntimeMaxEventPage)
		if err != nil {
			return recovery, err
		}
		for _, event := range page.Events {
			if event.ExecutionSeq != after+1 {
				return recovery, fmt.Errorf("plan execution event gap after sequence %d", after)
			}
			recovery.Summary = event.ResultDelta.SummaryChange
			if event.ResultDelta.CheckpointChange != nil {
				recovery.Checkpoints[event.ResultDelta.CheckpointChange.CheckpointID] = *event.ResultDelta.CheckpointChange
			}
			for _, subtask := range event.ResultDelta.SubtaskChanges {
				recovery.Subtasks[subtask.CheckpointID+"\x00"+subtask.SubtaskID] = subtask
			}
			after = event.ExecutionSeq
		}
		recovery.TailEvents += len(page.Events)
		recovery.TailBytes += page.EncodedBytes
		if !page.HasMore {
			break
		}
	}
	observePlanRuntimeRecovery(recovery.SnapshotSeq > 0, recovery.TailEvents, recovery.TailBytes, recovery.SnapshotAge, time.Since(started))
	return recovery, nil
}

// MaterializePlanExecutionSnapshot runs outside routine mutation commits. The
// pointer and immutable snapshot become visible atomically in their own Sync batch.
func (s *SessionStore) MaterializePlanExecutionSnapshot(sessionID, planID string, expectedExecutionSeq uint64) (PlanExecutionSnapshot, error) {
	unlock := s.store.sessionMutations.lockSessions(sessionID)
	defer unlock()
	recovery, err := s.RecoverPlanExecution(sessionID, planID)
	if err != nil {
		return PlanExecutionSnapshot{}, err
	}
	if recovery.Summary.ExecutionSeq != expectedExecutionSeq {
		return PlanExecutionSnapshot{}, &PlanRuntimeExecutionConflictError{Expected: expectedExecutionSeq, Current: recovery.Summary.ExecutionSeq, Status: recovery.Summary.Status}
	}
	now := time.Now().UnixMilli()
	snapshot := PlanExecutionSnapshot{SchemaVersion: PlanRuntimeSchemaVersion, ProjectionSchemaVersion: PlanRuntimeProjectionSchemaVersion, SessionID: sessionID, PlanID: planID, DefinitionRevision: recovery.Summary.DefinitionRevision, ExecutionSeq: recovery.Summary.ExecutionSeq, Summary: recovery.Summary, CreatedAt: now}
	for _, checkpoint := range recovery.Checkpoints {
		snapshot.CheckpointExecutions = append(snapshot.CheckpointExecutions, checkpoint)
	}
	for _, subtask := range recovery.Subtasks {
		snapshot.SubtaskExecutions = append(snapshot.SubtaskExecutions, subtask)
	}
	// Map iteration does not define content identity; sort by stable composite IDs.
	sortPlanRuntimeSnapshot(&snapshot)
	snapshot.ContentHash, err = planRuntimeSnapshotContentHash(snapshot)
	if err != nil {
		return PlanExecutionSnapshot{}, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return PlanExecutionSnapshot{}, err
	}
	key := KeyPlanExecutionSnapshot(sessionID, planID, snapshot.ExecutionSeq)
	pointer := planExecutionSnapshotPointer{ProjectionSchemaVersion: PlanRuntimeProjectionSchemaVersion, ExecutionSeq: snapshot.ExecutionSeq, SnapshotKey: key, CreatedAt: now, ContentHash: snapshot.ContentHash}
	pointerRaw, err := json.Marshal(pointer)
	if err != nil {
		return PlanExecutionSnapshot{}, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(key), raw, nil); err != nil {
		return PlanExecutionSnapshot{}, err
	}
	if err := batch.Set([]byte(KeyPlanExecutionCompatibleSnapshot(sessionID, planID, PlanRuntimeProjectionSchemaVersion)), pointerRaw, nil); err != nil {
		return PlanExecutionSnapshot{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return PlanExecutionSnapshot{}, err
	}
	return snapshot, nil
}

func planRuntimeSnapshotContentHash(snapshot PlanExecutionSnapshot) (string, error) {
	snapshot.ContentHash = ""
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("marshal plan execution snapshot for hashing: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sortPlanRuntimeSnapshot(snapshot *PlanExecutionSnapshot) {
	for i := 1; i < len(snapshot.CheckpointExecutions); i++ {
		for j := i; j > 0 && snapshot.CheckpointExecutions[j].CheckpointID < snapshot.CheckpointExecutions[j-1].CheckpointID; j-- {
			snapshot.CheckpointExecutions[j], snapshot.CheckpointExecutions[j-1] = snapshot.CheckpointExecutions[j-1], snapshot.CheckpointExecutions[j]
		}
	}
	for i := 1; i < len(snapshot.SubtaskExecutions); i++ {
		for j := i; j > 0; j-- {
			left := snapshot.SubtaskExecutions[j].CheckpointID + "\x00" + snapshot.SubtaskExecutions[j].SubtaskID
			right := snapshot.SubtaskExecutions[j-1].CheckpointID + "\x00" + snapshot.SubtaskExecutions[j-1].SubtaskID
			if left >= right {
				break
			}
			snapshot.SubtaskExecutions[j], snapshot.SubtaskExecutions[j-1] = snapshot.SubtaskExecutions[j-1], snapshot.SubtaskExecutions[j]
		}
	}
}

func parsePlanRuntimeSequence(key, prefix string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(key, prefix), 10, 64)
}
