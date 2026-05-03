package pebblestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"

	"swarm/packages/swarmd/internal/flow"
)

const (
	FlowOutboxStatusPending     = "pending"
	FlowOutboxStatusDelivered   = "delivered"
	FlowOutboxStatusRejected    = "rejected"
	FlowOutboxStatusUnreachable = "unreachable"

	FlowRunStatusClaimed = "claimed"
	FlowRunStatusRunning = "running"
	FlowRunStatusSuccess = "success"
	FlowRunStatusSkipped = "skipped"
	FlowRunStatusReview  = "review"
	FlowRunStatusFailed  = "failed"
)

type FlowDefinitionRecord struct {
	FlowID     string          `json:"flow_id"`
	Revision   int64           `json:"revision"`
	Assignment flow.Assignment `json:"assignment"`
	NextDueAt  time.Time       `json:"next_due_at,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	DeletedAt  time.Time       `json:"deleted_at,omitempty"`
}

type FlowAssignmentStatusRecord struct {
	FlowID           string                `json:"flow_id"`
	TargetSwarmID    string                `json:"target_swarm_id"`
	Target           flow.TargetSelection  `json:"target"`
	CommandID        string                `json:"command_id,omitempty"`
	DesiredRevision  int64                 `json:"desired_revision"`
	AcceptedRevision int64                 `json:"accepted_revision,omitempty"`
	Status           flow.AssignmentStatus `json:"status"`
	Reason           string                `json:"reason,omitempty"`
	PendingSync      bool                  `json:"pending_sync"`
	TargetClock      time.Time             `json:"target_clock,omitempty"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

type FlowOutboxCommandRecord struct {
	CommandID     string                 `json:"command_id"`
	FlowID        string                 `json:"flow_id"`
	Revision      int64                  `json:"revision"`
	TargetSwarmID string                 `json:"target_swarm_id"`
	Target        flow.TargetSelection   `json:"target"`
	Command       flow.AssignmentCommand `json:"command"`
	Status        string                 `json:"status"`
	AttemptCount  int                    `json:"attempt_count,omitempty"`
	NextAttemptAt time.Time              `json:"next_attempt_at,omitempty"`
	LastAttemptAt time.Time              `json:"last_attempt_at,omitempty"`
	LastError     string                 `json:"last_error,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

type FlowRunSummaryRecord struct {
	RunID              string    `json:"run_id"`
	FlowID             string    `json:"flow_id"`
	Revision           int64     `json:"revision"`
	ScheduledAt        time.Time `json:"scheduled_at"`
	StartedAt          time.Time `json:"started_at"`
	FinishedAt         time.Time `json:"finished_at,omitempty"`
	DurationMS         int64     `json:"duration_ms,omitempty"`
	Status             string    `json:"status"`
	Summary            string    `json:"summary,omitempty"`
	SessionID          string    `json:"session_id,omitempty"`
	TargetSwarmID      string    `json:"target_swarm_id,omitempty"`
	ReportedAt         time.Time `json:"reported_at,omitempty"`
	ReportAttemptCount int       `json:"report_attempt_count,omitempty"`
	NextReportAt       time.Time `json:"next_report_at,omitempty"`
	ReportError        string    `json:"report_error,omitempty"`
}

type FlowCommandLedgerRecord struct {
	CommandID string                `json:"command_id"`
	FlowID    string                `json:"flow_id"`
	Revision  int64                 `json:"revision"`
	Action    flow.CommandAction    `json:"action"`
	Status    flow.AssignmentStatus `json:"status"`
	Ack       flow.AssignmentAck    `json:"ack"`
	AppliedAt time.Time             `json:"applied_at"`
}

type FlowDueRecord struct {
	FlowID      string    `json:"flow_id"`
	Revision    int64     `json:"revision"`
	DueAt       time.Time `json:"due_at"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

type FlowRunClaimRecord struct {
	FlowID      string    `json:"flow_id"`
	Revision    int64     `json:"revision"`
	ScheduledAt time.Time `json:"scheduled_at"`
	RunID       string    `json:"run_id"`
	ClaimedAt   time.Time `json:"claimed_at"`
	LeaseUntil  time.Time `json:"lease_until,omitempty"`
}

type FlowStore struct {
	store *Store
}

func NewFlowStore(store *Store) *FlowStore {
	return &FlowStore{store: store}
}

func (s *FlowStore) PutDefinition(record FlowDefinitionRecord) (FlowDefinitionRecord, error) {
	if s == nil || s.store == nil {
		return FlowDefinitionRecord{}, errors.New("flow store is not configured")
	}
	record = normalizeFlowDefinitionRecord(record)
	if record.FlowID == "" {
		return FlowDefinitionRecord{}, errors.New("flow_id is required")
	}
	if record.Revision <= 0 {
		return FlowDefinitionRecord{}, errors.New("flow revision is required")
	}
	if err := flow.ValidateAssignment(record.Assignment); err != nil {
		return FlowDefinitionRecord{}, err
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyFlowDefinition(record.FlowID), record); err != nil {
		return FlowDefinitionRecord{}, err
	}
	return record, nil
}

func (s *FlowStore) GetDefinition(flowID string) (FlowDefinitionRecord, bool, error) {
	if s == nil || s.store == nil {
		return FlowDefinitionRecord{}, false, errors.New("flow store is not configured")
	}
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return FlowDefinitionRecord{}, false, errors.New("flow_id is required")
	}
	payload, ok, err := s.store.GetBytes(KeyFlowDefinition(flowID))
	if err != nil || !ok {
		return FlowDefinitionRecord{}, ok, err
	}
	record, repaired, err := decodeFlowDefinitionRecordPayload(payload)
	if err != nil {
		return FlowDefinitionRecord{}, false, fmt.Errorf("decode flow definition: %w", err)
	}
	if repaired {
		if err := s.store.PutJSON(KeyFlowDefinition(flowID), record); err != nil {
			return FlowDefinitionRecord{}, false, err
		}
	}
	return normalizeFlowDefinitionRecord(record), true, nil
}

func (s *FlowStore) ListDefinitions(limit int) ([]FlowDefinitionRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("flow store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]FlowDefinitionRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(FlowDefinitionPrefix(), limit, func(key string, value []byte) error {
		record, repaired, err := decodeFlowDefinitionRecordPayload(value)
		if err != nil {
			return fmt.Errorf("decode flow definition: %w", err)
		}
		record = normalizeFlowDefinitionRecord(record)
		if repaired {
			if err := s.store.PutJSON(key, record); err != nil {
				return err
			}
		}
		if record.FlowID != "" {
			out = append(out, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].FlowID < out[j].FlowID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *FlowStore) DeleteDefinition(flowID string) error {
	if s == nil || s.store == nil {
		return errors.New("flow store is not configured")
	}
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return errors.New("flow_id is required")
	}
	return s.store.Delete(KeyFlowDefinition(flowID))
}

func (s *FlowStore) PutAssignmentStatus(record FlowAssignmentStatusRecord) (FlowAssignmentStatusRecord, error) {
	if s == nil || s.store == nil {
		return FlowAssignmentStatusRecord{}, errors.New("flow store is not configured")
	}
	record = normalizeFlowAssignmentStatusRecord(record)
	if record.FlowID == "" || record.TargetSwarmID == "" {
		return FlowAssignmentStatusRecord{}, errors.New("flow_id and target_swarm_id are required")
	}
	record.UpdatedAt = time.Now().UTC()
	if err := s.store.PutJSON(KeyFlowAssignmentStatus(record.FlowID, record.TargetSwarmID), record); err != nil {
		return FlowAssignmentStatusRecord{}, err
	}
	return record, nil
}

func (s *FlowStore) GetAssignmentStatus(flowID, targetSwarmID string) (FlowAssignmentStatusRecord, bool, error) {
	if s == nil || s.store == nil {
		return FlowAssignmentStatusRecord{}, false, errors.New("flow store is not configured")
	}
	var record FlowAssignmentStatusRecord
	ok, err := s.store.GetJSON(KeyFlowAssignmentStatus(flowID, targetSwarmID), &record)
	if err != nil || !ok {
		return FlowAssignmentStatusRecord{}, ok, err
	}
	return normalizeFlowAssignmentStatusRecord(record), true, nil
}

func (s *FlowStore) ListAssignmentStatuses(flowID string, limit int) ([]FlowAssignmentStatusRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("flow store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]FlowAssignmentStatusRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(FlowAssignmentStatusPrefix(flowID), limit, func(_ string, value []byte) error {
		var record FlowAssignmentStatusRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode flow assignment status: %w", err)
		}
		record = normalizeFlowAssignmentStatusRecord(record)
		if record.FlowID != "" && record.TargetSwarmID != "" {
			out = append(out, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TargetSwarmID < out[j].TargetSwarmID })
	return out, nil
}

func (s *FlowStore) PutOutboxCommand(record FlowOutboxCommandRecord, previous *FlowOutboxCommandRecord) (FlowOutboxCommandRecord, error) {
	if s == nil || s.store == nil {
		return FlowOutboxCommandRecord{}, errors.New("flow store is not configured")
	}
	record = normalizeFlowOutboxCommandRecord(record)
	if record.CommandID == "" || record.FlowID == "" || record.Revision <= 0 {
		return FlowOutboxCommandRecord{}, errors.New("command_id, flow_id, and revision are required")
	}
	if err := record.Command.ValidateIdempotencyKey(); err != nil {
		return FlowOutboxCommandRecord{}, err
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	payload, err := json.Marshal(record)
	if err != nil {
		return FlowOutboxCommandRecord{}, fmt.Errorf("marshal flow outbox command: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	recordKey := KeyFlowOutbox(record.CommandID)
	if err := batch.Set([]byte(recordKey), payload, nil); err != nil {
		return FlowOutboxCommandRecord{}, fmt.Errorf("set flow outbox command: %w", err)
	}
	if previous != nil {
		prev := normalizeFlowOutboxCommandRecord(*previous)
		if prev.CommandID != "" {
			prevIndex := KeyFlowOutboxStatus(prev.Status, prev.NextAttemptAt.UTC().UnixMilli(), prev.CommandID)
			nextIndex := KeyFlowOutboxStatus(record.Status, record.NextAttemptAt.UTC().UnixMilli(), record.CommandID)
			if prevIndex != nextIndex {
				if err := batch.Delete([]byte(prevIndex), nil); err != nil {
					return FlowOutboxCommandRecord{}, fmt.Errorf("delete stale flow outbox status index: %w", err)
				}
			}
		}
	}
	if err := batch.Set([]byte(KeyFlowOutboxStatus(record.Status, record.NextAttemptAt.UTC().UnixMilli(), record.CommandID)), []byte(recordKey), nil); err != nil {
		return FlowOutboxCommandRecord{}, fmt.Errorf("set flow outbox status index: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return FlowOutboxCommandRecord{}, fmt.Errorf("commit flow outbox command: %w", err)
	}
	return record, nil
}

func (s *FlowStore) GetOutboxCommand(commandID string) (FlowOutboxCommandRecord, bool, error) {
	if s == nil || s.store == nil {
		return FlowOutboxCommandRecord{}, false, errors.New("flow store is not configured")
	}
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return FlowOutboxCommandRecord{}, false, errors.New("command_id is required")
	}
	key := KeyFlowOutbox(commandID)
	payload, ok, err := s.store.GetBytes(key)
	if err != nil || !ok {
		return FlowOutboxCommandRecord{}, ok, err
	}
	record, repaired, err := decodeFlowOutboxCommandRecordPayload(payload)
	if err != nil {
		return FlowOutboxCommandRecord{}, false, fmt.Errorf("decode flow outbox command: %w", err)
	}
	if repaired {
		if err := s.store.PutJSON(key, record); err != nil {
			return FlowOutboxCommandRecord{}, false, err
		}
	}
	return normalizeFlowOutboxCommandRecord(record), true, nil
}

func (s *FlowStore) CountOutboxCommands(status string) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("flow store is not configured")
	}
	count := 0
	err := s.store.IteratePrefix(FlowOutboxStatusPrefix(status), 100000, func(_ string, _ []byte) error {
		count++
		return nil
	})
	return count, err
}

func (s *FlowStore) ListOutboxCommands(status string, limit int) ([]FlowOutboxCommandRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("flow store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]FlowOutboxCommandRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(FlowOutboxStatusPrefix(status), 100000, func(_ string, value []byte) error {
		if len(out) >= limit {
			return nil
		}
		recordKey := strings.TrimSpace(string(value))
		if recordKey == "" {
			return nil
		}
		payload, ok, err := s.store.GetBytes(recordKey)
		if err != nil || !ok {
			return err
		}
		record, repaired, err := decodeFlowOutboxCommandRecordPayload(payload)
		if err != nil {
			return fmt.Errorf("decode flow outbox command: %w", err)
		}
		if repaired {
			if err := s.store.PutJSON(recordKey, record); err != nil {
				return err
			}
		}
		out = append(out, normalizeFlowOutboxCommandRecord(record))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NextAttemptAt.Equal(out[j].NextAttemptAt) {
			return out[i].CommandID < out[j].CommandID
		}
		return out[i].NextAttemptAt.Before(out[j].NextAttemptAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *FlowStore) DeleteOutboxCommand(commandID string, previous *FlowOutboxCommandRecord) error {
	if s == nil || s.store == nil {
		return errors.New("flow store is not configured")
	}
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return errors.New("command_id is required")
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeyFlowOutbox(commandID)), nil); err != nil {
		return fmt.Errorf("delete flow outbox command: %w", err)
	}
	if previous != nil {
		prev := normalizeFlowOutboxCommandRecord(*previous)
		if prev.CommandID != "" {
			if err := batch.Delete([]byte(KeyFlowOutboxStatus(prev.Status, prev.NextAttemptAt.UTC().UnixMilli(), prev.CommandID)), nil); err != nil {
				return fmt.Errorf("delete flow outbox status index: %w", err)
			}
		}
	}
	return batch.Commit(pebble.Sync)
}

func (s *FlowStore) PutMirroredRunSummary(record FlowRunSummaryRecord) (FlowRunSummaryRecord, error) {
	if s == nil || s.store == nil {
		return FlowRunSummaryRecord{}, errors.New("flow store is not configured")
	}
	record = normalizeFlowRunSummaryRecord(record)
	if record.RunID == "" || record.FlowID == "" {
		return FlowRunSummaryRecord{}, errors.New("run_id and flow_id are required")
	}
	return s.putRunSummary(record, KeyFlowMirroredRun(record.FlowID, record.StartedAt.UTC().UnixMilli(), record.RunID))
}

func (s *FlowStore) ListMirroredRunSummaries(flowID string, limit int) ([]FlowRunSummaryRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("flow store is not configured")
	}
	return s.listRunSummaries(FlowMirroredRunPrefix(flowID), limit)
}

func (s *FlowStore) PutAcceptedAssignment(record flow.AcceptedAssignment) (flow.AcceptedAssignment, error) {
	if s == nil || s.store == nil {
		return flow.AcceptedAssignment{}, errors.New("flow store is not configured")
	}
	record = normalizeAcceptedAssignment(record)
	if record.FlowID == "" || record.Revision <= 0 {
		return flow.AcceptedAssignment{}, errors.New("flow_id and revision are required")
	}
	if err := s.store.PutJSON(KeyFlowTargetAccepted(record.FlowID), record); err != nil {
		return flow.AcceptedAssignment{}, err
	}
	return record, nil
}

func (s *FlowStore) ApplyTargetAssignmentCommand(command flow.AssignmentCommand, targetSwarmID string, now time.Time) (flow.AssignmentAck, bool, error) {
	if s == nil || s.store == nil {
		return flow.AssignmentAck{}, false, errors.New("flow store is not configured")
	}
	command = normalizeFlowAssignmentCommand(command)
	if err := command.ValidateIdempotencyKey(); err != nil {
		return flow.AssignmentAck{}, false, err
	}
	key := command.IdempotencyKey()
	if existing, ok, err := s.GetCommandLedger(key.FlowID, key.Revision, key.CommandID); err != nil || ok {
		if err != nil {
			return flow.AssignmentAck{}, false, err
		}
		return normalizeFlowAssignmentAck(existing.Ack), false, nil
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	targetSwarmID = strings.TrimSpace(targetSwarmID)
	baseAck := flow.AssignmentAck{
		CommandID:     key.CommandID,
		FlowID:        key.FlowID,
		TargetSwarmID: targetSwarmID,
		TargetClock:   now,
	}
	current, hasCurrent, err := s.GetAcceptedAssignment(key.FlowID)
	if err != nil {
		return flow.AssignmentAck{}, false, err
	}
	maxAppliedRevision, err := s.maxAppliedTargetAssignmentRevision(key.FlowID)
	if err != nil {
		return flow.AssignmentAck{}, false, err
	}
	if hasCurrent && current.Revision > maxAppliedRevision {
		maxAppliedRevision = current.Revision
	}
	reject := func(status flow.AssignmentStatus, reason string) (flow.AssignmentAck, bool, error) {
		ack := baseAck
		ack.Status = status
		ack.Reason = strings.TrimSpace(reason)
		if hasCurrent {
			ack.AcceptedRevision = current.Revision
		} else if maxAppliedRevision > 0 {
			ack.AcceptedRevision = maxAppliedRevision
		}
		_, inserted, err := s.PutCommandLedger(FlowCommandLedgerRecord{
			CommandID: key.CommandID,
			FlowID:    key.FlowID,
			Revision:  key.Revision,
			Action:    command.Action,
			Status:    status,
			Ack:       ack,
			AppliedAt: now,
		})
		return ack, inserted, err
	}

	switch command.Action {
	case flow.CommandInstall, flow.CommandUpdate:
		assignment := normalizeFlowAssignment(command.Assignment)
		if assignment.FlowID != key.FlowID || assignment.Revision != key.Revision {
			return reject(flow.AssignmentRejected, "assignment identity must match command flow_id and revision")
		}
		if maxAppliedRevision >= key.Revision {
			return reject(flow.AssignmentOutOfOrder, fmt.Sprintf("target already applied revision %d", maxAppliedRevision))
		}
		if err := flow.ValidateAssignment(assignment); err != nil {
			return reject(flow.AssignmentRejected, err.Error())
		}
		return s.acceptTargetAssignmentCommand(command, baseAck, assignment, now)
	case flow.CommandDelete:
		if hasCurrent && current.Revision > key.Revision {
			return reject(flow.AssignmentOutOfOrder, fmt.Sprintf("target already accepted revision %d", current.Revision))
		}
		if !hasCurrent && maxAppliedRevision > key.Revision {
			return reject(flow.AssignmentOutOfOrder, fmt.Sprintf("target already applied revision %d", maxAppliedRevision))
		}
		return s.acceptTargetDeleteCommand(command, baseAck, now)
	case flow.CommandRunNow:
		if !hasCurrent {
			return reject(flow.AssignmentRejected, "accepted assignment is not installed on target")
		}
		if current.Revision > key.Revision {
			return reject(flow.AssignmentOutOfOrder, fmt.Sprintf("target already accepted revision %d", current.Revision))
		}
		if current.Revision != key.Revision {
			return reject(flow.AssignmentRejected, fmt.Sprintf("accepted revision %d does not match run_now revision %d", current.Revision, key.Revision))
		}
		return reject(flow.AssignmentRejected, "run_now requires the target execution service")
	default:
		return reject(flow.AssignmentRejected, fmt.Sprintf("unsupported flow command action %q", command.Action))
	}
}

func (s *FlowStore) GetAcceptedAssignment(flowID string) (flow.AcceptedAssignment, bool, error) {
	if s == nil || s.store == nil {
		return flow.AcceptedAssignment{}, false, errors.New("flow store is not configured")
	}
	var record flow.AcceptedAssignment
	ok, err := s.store.GetJSON(KeyFlowTargetAccepted(flowID), &record)
	if err != nil || !ok {
		return flow.AcceptedAssignment{}, ok, err
	}
	return normalizeAcceptedAssignment(record), true, nil
}

func (s *FlowStore) ListAcceptedAssignments(limit int) ([]flow.AcceptedAssignment, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("flow store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]flow.AcceptedAssignment, 0, min(limit, 16))
	err := s.store.IteratePrefix(FlowTargetAcceptedPrefix(), limit, func(_ string, value []byte) error {
		var record flow.AcceptedAssignment
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode accepted flow assignment: %w", err)
		}
		record = normalizeAcceptedAssignment(record)
		if record.FlowID != "" {
			out = append(out, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AcceptedAt.Equal(out[j].AcceptedAt) {
			return out[i].FlowID < out[j].FlowID
		}
		return out[i].AcceptedAt.After(out[j].AcceptedAt)
	})
	return out, nil
}

func (s *FlowStore) DeleteAcceptedAssignment(flowID string) error {
	if s == nil || s.store == nil {
		return errors.New("flow store is not configured")
	}
	flowID = strings.TrimSpace(flowID)
	if flowID == "" {
		return errors.New("flow_id is required")
	}
	return s.store.Delete(KeyFlowTargetAccepted(flowID))
}

func (s *FlowStore) PutCommandLedger(record FlowCommandLedgerRecord) (FlowCommandLedgerRecord, bool, error) {
	if s == nil || s.store == nil {
		return FlowCommandLedgerRecord{}, false, errors.New("flow store is not configured")
	}
	record = normalizeFlowCommandLedgerRecord(record)
	if record.CommandID == "" || record.FlowID == "" || record.Revision <= 0 {
		return FlowCommandLedgerRecord{}, false, errors.New("command_id, flow_id, and revision are required")
	}
	key := KeyFlowTargetCommandLedger(record.FlowID, record.Revision, record.CommandID)
	var existing FlowCommandLedgerRecord
	ok, err := s.store.GetJSON(key, &existing)
	if err != nil {
		return FlowCommandLedgerRecord{}, false, err
	}
	if ok {
		return normalizeFlowCommandLedgerRecord(existing), false, nil
	}
	if record.AppliedAt.IsZero() {
		record.AppliedAt = time.Now().UTC()
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return FlowCommandLedgerRecord{}, false, err
	}
	return record, true, nil
}

func (s *FlowStore) GetCommandLedger(flowID string, revision int64, commandID string) (FlowCommandLedgerRecord, bool, error) {
	if s == nil || s.store == nil {
		return FlowCommandLedgerRecord{}, false, errors.New("flow store is not configured")
	}
	var record FlowCommandLedgerRecord
	ok, err := s.store.GetJSON(KeyFlowTargetCommandLedger(flowID, revision, commandID), &record)
	if err != nil || !ok {
		return FlowCommandLedgerRecord{}, ok, err
	}
	return normalizeFlowCommandLedgerRecord(record), true, nil
}

func (s *FlowStore) ListCommandLedger(flowID string, limit int) ([]FlowCommandLedgerRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("flow store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]FlowCommandLedgerRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(FlowTargetCommandLedgerPrefix(flowID), limit, func(_ string, value []byte) error {
		var record FlowCommandLedgerRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode flow command ledger: %w", err)
		}
		out = append(out, normalizeFlowCommandLedgerRecord(record))
		return nil
	})
	return out, err
}

func (s *FlowStore) PutDue(record FlowDueRecord) (FlowDueRecord, error) {
	if s == nil || s.store == nil {
		return FlowDueRecord{}, errors.New("flow store is not configured")
	}
	record = normalizeFlowDueRecord(record)
	if record.FlowID == "" || record.Revision <= 0 || record.DueAt.IsZero() {
		return FlowDueRecord{}, errors.New("flow_id, revision, and due_at are required")
	}
	if err := s.store.PutJSON(KeyFlowTargetDue(record.DueAt.UTC().UnixMilli(), record.FlowID, record.Revision), record); err != nil {
		return FlowDueRecord{}, err
	}
	return record, nil
}

func (s *FlowStore) ListDue(now time.Time, limit int) ([]FlowDueRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("flow store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	now = now.UTC()
	out := make([]FlowDueRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(FlowTargetDuePrefix(), 100000, func(_ string, value []byte) error {
		if len(out) >= limit {
			return nil
		}
		var record FlowDueRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode flow due record: %w", err)
		}
		record = normalizeFlowDueRecord(record)
		if record.DueAt.IsZero() || record.DueAt.After(now) {
			return nil
		}
		out = append(out, record)
		return nil
	})
	return out, err
}

func (s *FlowStore) DeleteDue(record FlowDueRecord) error {
	if s == nil || s.store == nil {
		return errors.New("flow store is not configured")
	}
	record = normalizeFlowDueRecord(record)
	if record.FlowID == "" || record.Revision <= 0 || record.DueAt.IsZero() {
		return errors.New("flow_id, revision, and due_at are required")
	}
	return s.store.Delete(KeyFlowTargetDue(record.DueAt.UTC().UnixMilli(), record.FlowID, record.Revision))
}

func (s *FlowStore) ClaimRun(record FlowRunClaimRecord) (FlowRunClaimRecord, bool, error) {
	if s == nil || s.store == nil {
		return FlowRunClaimRecord{}, false, errors.New("flow store is not configured")
	}
	record = normalizeFlowRunClaimRecord(record)
	if record.FlowID == "" || record.Revision <= 0 || record.ScheduledAt.IsZero() || record.RunID == "" {
		return FlowRunClaimRecord{}, false, errors.New("flow_id, revision, scheduled_at, and run_id are required")
	}
	key := KeyFlowTargetRunClaim(record.FlowID, record.Revision, record.ScheduledAt.UTC().UnixMilli())
	var existing FlowRunClaimRecord
	ok, err := s.store.GetJSON(key, &existing)
	if err != nil {
		return FlowRunClaimRecord{}, false, err
	}
	if ok {
		return normalizeFlowRunClaimRecord(existing), false, nil
	}
	if record.ClaimedAt.IsZero() {
		record.ClaimedAt = time.Now().UTC()
	}
	if err := s.store.PutJSON(key, record); err != nil {
		return FlowRunClaimRecord{}, false, err
	}
	return record, true, nil
}

func (s *FlowStore) GetRunClaim(flowID string, revision int64, scheduledAt time.Time) (FlowRunClaimRecord, bool, error) {
	if s == nil || s.store == nil {
		return FlowRunClaimRecord{}, false, errors.New("flow store is not configured")
	}
	var record FlowRunClaimRecord
	ok, err := s.store.GetJSON(KeyFlowTargetRunClaim(flowID, revision, scheduledAt.UTC().UnixMilli()), &record)
	if err != nil || !ok {
		return FlowRunClaimRecord{}, ok, err
	}
	return normalizeFlowRunClaimRecord(record), true, nil
}

func (s *FlowStore) PutTargetRun(record FlowRunSummaryRecord) (FlowRunSummaryRecord, error) {
	if s == nil || s.store == nil {
		return FlowRunSummaryRecord{}, errors.New("flow store is not configured")
	}
	record = normalizeFlowRunSummaryRecord(record)
	if record.RunID == "" || record.FlowID == "" {
		return FlowRunSummaryRecord{}, errors.New("run_id and flow_id are required")
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return FlowRunSummaryRecord{}, fmt.Errorf("marshal target flow run: %w", err)
	}
	recordKey := KeyFlowTargetRun(record.RunID)
	var existing FlowRunSummaryRecord
	existingOK, err := s.store.GetJSON(recordKey, &existing)
	if err != nil {
		return FlowRunSummaryRecord{}, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if existingOK {
		existing = normalizeFlowRunSummaryRecord(existing)
		oldIndex := KeyFlowTargetRunByFlow(existing.FlowID, existing.StartedAt.UTC().UnixMilli(), existing.RunID)
		newIndex := KeyFlowTargetRunByFlow(record.FlowID, record.StartedAt.UTC().UnixMilli(), record.RunID)
		if oldIndex != newIndex {
			if err := batch.Delete([]byte(oldIndex), nil); err != nil {
				return FlowRunSummaryRecord{}, fmt.Errorf("delete stale target flow run index: %w", err)
			}
		}
	}
	if err := batch.Set([]byte(recordKey), payload, nil); err != nil {
		return FlowRunSummaryRecord{}, fmt.Errorf("set target flow run: %w", err)
	}
	if err := batch.Set([]byte(KeyFlowTargetRunByFlow(record.FlowID, record.StartedAt.UTC().UnixMilli(), record.RunID)), []byte(recordKey), nil); err != nil {
		return FlowRunSummaryRecord{}, fmt.Errorf("set target flow run by flow: %w", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return FlowRunSummaryRecord{}, fmt.Errorf("commit target flow run: %w", err)
	}
	return record, nil
}

func (s *FlowStore) GetTargetRun(runID string) (FlowRunSummaryRecord, bool, error) {
	if s == nil || s.store == nil {
		return FlowRunSummaryRecord{}, false, errors.New("flow store is not configured")
	}
	var record FlowRunSummaryRecord
	ok, err := s.store.GetJSON(KeyFlowTargetRun(runID), &record)
	if err != nil || !ok {
		return FlowRunSummaryRecord{}, ok, err
	}
	return normalizeFlowRunSummaryRecord(record), true, nil
}

func (s *FlowStore) ListTargetRuns(flowID string, limit int) ([]FlowRunSummaryRecord, error) {
	return s.listTargetRuns(flowID, limit, false, func(FlowRunSummaryRecord) bool { return true })
}

func (s *FlowStore) ListPendingTargetRunReports(now time.Time, limit int) ([]FlowRunSummaryRecord, error) {
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.listTargetRuns("", limit, true, func(record FlowRunSummaryRecord) bool {
		if !record.ReportedAt.IsZero() {
			return false
		}
		if record.FinishedAt.IsZero() {
			return record.ReportAttemptCount == 0
		}
		return record.NextReportAt.IsZero() || !record.NextReportAt.After(now)
	})
}

func (s *FlowStore) listTargetRuns(flowID string, limit int, scanAll bool, include func(FlowRunSummaryRecord) bool) ([]FlowRunSummaryRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("flow store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]FlowRunSummaryRecord, 0, min(limit, 16))
	prefix := FlowTargetRunByFlowPrefix(flowID)
	if scanAll {
		prefix = KeyFlowTargetRunByFlowPrefix
	}
	err := s.store.IteratePrefix(prefix, 100000, func(_ string, value []byte) error {
		if len(out) >= limit {
			return nil
		}
		recordKey := strings.TrimSpace(string(value))
		if recordKey == "" {
			return nil
		}
		var record FlowRunSummaryRecord
		ok, err := s.store.GetJSON(recordKey, &record)
		if err != nil || !ok {
			return err
		}
		record = normalizeFlowRunSummaryRecord(record)
		if include == nil || include(record) {
			out = append(out, record)
		}
		return nil
	})
	return out, err
}

func (s *FlowStore) putRunSummary(record FlowRunSummaryRecord, key string) (FlowRunSummaryRecord, error) {
	if err := s.store.PutJSON(key, record); err != nil {
		return FlowRunSummaryRecord{}, err
	}
	return record, nil
}

func (s *FlowStore) listRunSummaries(prefix string, limit int) ([]FlowRunSummaryRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	out := make([]FlowRunSummaryRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var record FlowRunSummaryRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode flow run summary: %w", err)
		}
		out = append(out, normalizeFlowRunSummaryRecord(record))
		return nil
	})
	return out, err
}

func decodeFlowDefinitionRecordPayload(payload []byte) (FlowDefinitionRecord, bool, error) {
	var record FlowDefinitionRecord
	if err := json.Unmarshal(payload, &record); err == nil {
		return normalizeFlowDefinitionRecord(record), false, nil
	}
	repaired, repairedOK, repairErr := repairLegacyFlowDefinitionPayload(payload)
	if repairErr != nil {
		return FlowDefinitionRecord{}, false, repairErr
	}
	if !repairedOK {
		var zero FlowDefinitionRecord
		if err := json.Unmarshal(payload, &zero); err != nil {
			return FlowDefinitionRecord{}, false, err
		}
		return normalizeFlowDefinitionRecord(zero), false, nil
	}
	if err := json.Unmarshal(repaired, &record); err != nil {
		return FlowDefinitionRecord{}, false, err
	}
	return normalizeFlowDefinitionRecord(record), true, nil
}

func decodeFlowOutboxCommandRecordPayload(payload []byte) (FlowOutboxCommandRecord, bool, error) {
	var record FlowOutboxCommandRecord
	if err := json.Unmarshal(payload, &record); err == nil {
		return normalizeFlowOutboxCommandRecord(record), false, nil
	}
	repaired, repairedOK, repairErr := repairLegacyFlowOutboxCommandPayload(payload)
	if repairErr != nil {
		return FlowOutboxCommandRecord{}, false, repairErr
	}
	if !repairedOK {
		var zero FlowOutboxCommandRecord
		if err := json.Unmarshal(payload, &zero); err != nil {
			return FlowOutboxCommandRecord{}, false, err
		}
		return normalizeFlowOutboxCommandRecord(zero), false, nil
	}
	if err := json.Unmarshal(repaired, &record); err != nil {
		return FlowOutboxCommandRecord{}, false, err
	}
	return normalizeFlowOutboxCommandRecord(record), true, nil
}

func repairLegacyFlowOutboxCommandPayload(payload []byte) ([]byte, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, false, err
	}
	commandRaw, ok := raw["command"]
	if !ok {
		return nil, false, nil
	}
	repairedCommand, repaired, err := repairLegacyFlowAssignmentCommandPayload(commandRaw)
	if err != nil {
		return nil, false, err
	}
	if !repaired {
		return nil, false, nil
	}
	raw["command"] = repairedCommand
	repairedPayload, err := json.Marshal(raw)
	if err != nil {
		return nil, false, err
	}
	return repairedPayload, true, nil
}

func repairLegacyFlowAssignmentCommandPayload(payload []byte) ([]byte, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, false, err
	}
	assignmentRaw, ok := raw["assignment"]
	if !ok {
		return nil, false, nil
	}
	repairedAssignment, repaired, err := repairLegacyFlowAssignmentPayload(assignmentRaw)
	if err != nil {
		return nil, false, err
	}
	if !repaired {
		return nil, false, nil
	}
	raw["assignment"] = repairedAssignment
	repairedPayload, err := json.Marshal(raw)
	if err != nil {
		return nil, false, err
	}
	return repairedPayload, true, nil
}

func repairLegacyFlowDefinitionPayload(payload []byte) ([]byte, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, false, err
	}
	assignmentRaw, ok := raw["assignment"]
	if !ok {
		return nil, false, nil
	}
	repairedAssignment, repaired, err := repairLegacyFlowAssignmentPayload(assignmentRaw)
	if err != nil {
		return nil, false, err
	}
	if !repaired {
		return nil, false, nil
	}
	raw["assignment"] = repairedAssignment
	updatedPayload, err := json.Marshal(raw)
	if err != nil {
		return nil, false, err
	}
	return updatedPayload, true, nil
}

func repairLegacyFlowAssignmentPayload(payload []byte) ([]byte, bool, error) {
	var assignment map[string]json.RawMessage
	if err := json.Unmarshal(payload, &assignment); err != nil {
		return nil, false, err
	}
	agentRaw, ok := assignment["agent"]
	if !ok {
		return nil, false, nil
	}
	repairedAgent, repaired, err := repairLegacyFlowAgentPayload(agentRaw)
	if err != nil {
		return nil, false, err
	}
	if !repaired {
		return nil, false, nil
	}
	assignment["agent"] = repairedAgent
	updatedAssignment, err := json.Marshal(assignment)
	if err != nil {
		return nil, false, err
	}
	return updatedAssignment, true, nil
}

func repairLegacyFlowAgentPayload(payload []byte) ([]byte, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, false, err
	}
	var profileName string
	if value, ok := raw["profile_name"]; ok {
		_ = json.Unmarshal(value, &profileName)
		profileName = strings.TrimSpace(profileName)
	}
	var profileMode string
	if value, ok := raw["profile_mode"]; ok {
		_ = json.Unmarshal(value, &profileMode)
		profileMode = flow.NormalizeAgentProfileMode(profileMode)
	}
	var targetName string
	if value, ok := raw["target_name"]; ok {
		_ = json.Unmarshal(value, &targetName)
		delete(raw, "target_name")
	}
	var targetKind string
	if value, ok := raw["target_kind"]; ok {
		_ = json.Unmarshal(value, &targetKind)
		delete(raw, "target_kind")
	}
	for _, key := range []string{"model", "service_tier", "provider", "thinking", "prompt", "tool_scope", "tool_contract", "execution_setting", "exit_plan_mode_enabled", "enabled", "protected", "updated_at"} {
		delete(raw, key)
	}
	if profileName == "" || profileMode == "" {
		inferredName, inferredMode, ok := inferLegacyFlowAgentProfile(targetName, targetKind)
		if !ok {
			return nil, false, fmt.Errorf("legacy flow agent payload is missing recoverable durable profile identity")
		}
		if profileName == "" {
			profileName = inferredName
		}
		if profileMode == "" {
			profileMode = inferredMode
		}
	}
	encodedName, err := json.Marshal(profileName)
	if err != nil {
		return nil, false, err
	}
	raw["profile_name"] = encodedName
	encodedMode, err := json.Marshal(profileMode)
	if err != nil {
		return nil, false, err
	}
	raw["profile_mode"] = encodedMode
	repaired, err := json.Marshal(raw)
	if err != nil {
		return nil, false, err
	}
	return repaired, !bytes.Equal(bytes.TrimSpace(payload), bytes.TrimSpace(repaired)), nil
}

func inferLegacyFlowAgentProfile(targetName, targetKind string) (string, string, bool) {
	name := strings.TrimSpace(targetName)
	mode := flow.NormalizeAgentProfileMode(targetKind)
	if name == "" || mode == "" {
		return "", "", false
	}
	return name, mode, true
}

func normalizeFlowDefinitionRecord(record FlowDefinitionRecord) FlowDefinitionRecord {
	record.FlowID = strings.TrimSpace(firstNonEmptyString(record.FlowID, record.Assignment.FlowID))
	record.Assignment = normalizeFlowAssignment(record.Assignment)
	if record.Assignment.FlowID == "" {
		record.Assignment.FlowID = record.FlowID
	}
	if record.Revision == 0 {
		record.Revision = record.Assignment.Revision
	}
	if record.Assignment.Revision == 0 {
		record.Assignment.Revision = record.Revision
	}
	record.NextDueAt = record.NextDueAt.UTC()
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	record.DeletedAt = record.DeletedAt.UTC()
	return record
}

func normalizeFlowAssignmentStatusRecord(record FlowAssignmentStatusRecord) FlowAssignmentStatusRecord {
	record.FlowID = strings.TrimSpace(record.FlowID)
	record.TargetSwarmID = strings.TrimSpace(record.TargetSwarmID)
	record.CommandID = strings.TrimSpace(record.CommandID)
	record.Reason = strings.TrimSpace(record.Reason)
	if record.Status == "" {
		record.Status = flow.AssignmentPendingSync
	}
	record.PendingSync = record.Status == flow.AssignmentPendingSync || record.Status == flow.AssignmentTargetOffline || record.Status == flow.AssignmentTargetUnusable
	record.TargetClock = record.TargetClock.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeFlowOutboxCommandRecord(record FlowOutboxCommandRecord) FlowOutboxCommandRecord {
	record.CommandID = strings.TrimSpace(record.CommandID)
	record.FlowID = strings.TrimSpace(firstNonEmptyString(record.FlowID, record.Command.FlowID, record.Command.Assignment.FlowID))
	if record.Revision == 0 {
		record.Revision = firstNonZeroInt64(record.Command.Revision, record.Command.Assignment.Revision)
	}
	record.TargetSwarmID = strings.TrimSpace(record.TargetSwarmID)
	record.Status = strings.TrimSpace(strings.ToLower(record.Status))
	if record.Status == "" {
		record.Status = FlowOutboxStatusPending
	}
	record.LastError = strings.TrimSpace(record.LastError)
	record.Command = normalizeFlowAssignmentCommand(record.Command)
	record.Command.CommandID = strings.TrimSpace(firstNonEmptyString(record.Command.CommandID, record.CommandID))
	record.Command.FlowID = strings.TrimSpace(firstNonEmptyString(record.Command.FlowID, record.FlowID))
	record.Command.Revision = firstNonZeroInt64(record.Command.Revision, record.Revision)
	record.NextAttemptAt = record.NextAttemptAt.UTC()
	record.LastAttemptAt = record.LastAttemptAt.UTC()
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record
}

func normalizeFlowRunSummaryRecord(record FlowRunSummaryRecord) FlowRunSummaryRecord {
	record.RunID = strings.TrimSpace(record.RunID)
	record.FlowID = strings.TrimSpace(record.FlowID)
	record.Status = strings.TrimSpace(strings.ToLower(record.Status))
	if record.Status == "" {
		record.Status = FlowRunStatusRunning
	}
	record.Summary = strings.TrimSpace(record.Summary)
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.TargetSwarmID = strings.TrimSpace(record.TargetSwarmID)
	record.ReportError = strings.TrimSpace(record.ReportError)
	record.ScheduledAt = record.ScheduledAt.UTC()
	record.StartedAt = record.StartedAt.UTC()
	if record.StartedAt.IsZero() {
		record.StartedAt = record.ScheduledAt
	}
	record.FinishedAt = record.FinishedAt.UTC()
	record.ReportedAt = record.ReportedAt.UTC()
	record.NextReportAt = record.NextReportAt.UTC()
	if !record.FinishedAt.IsZero() && record.DurationMS == 0 && !record.StartedAt.IsZero() {
		durationMS := record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		if durationMS > 0 {
			record.DurationMS = durationMS
		}
	}
	return record
}

func normalizeFlowCommandLedgerRecord(record FlowCommandLedgerRecord) FlowCommandLedgerRecord {
	record.CommandID = strings.TrimSpace(record.CommandID)
	record.FlowID = strings.TrimSpace(record.FlowID)
	if record.Status == "" {
		record.Status = flow.AssignmentAccepted
	}
	record.Ack.CommandID = strings.TrimSpace(record.Ack.CommandID)
	record.Ack.FlowID = strings.TrimSpace(record.Ack.FlowID)
	record.Ack.Reason = strings.TrimSpace(record.Ack.Reason)
	record.Ack.TargetSwarmID = strings.TrimSpace(record.Ack.TargetSwarmID)
	record.Ack.TargetClock = record.Ack.TargetClock.UTC()
	record.AppliedAt = record.AppliedAt.UTC()
	return record
}

func normalizeFlowDueRecord(record FlowDueRecord) FlowDueRecord {
	record.FlowID = strings.TrimSpace(record.FlowID)
	record.DueAt = record.DueAt.UTC()
	record.ScheduledAt = record.ScheduledAt.UTC()
	if record.ScheduledAt.IsZero() {
		record.ScheduledAt = record.DueAt
	}
	return record
}

func normalizeFlowRunClaimRecord(record FlowRunClaimRecord) FlowRunClaimRecord {
	record.FlowID = strings.TrimSpace(record.FlowID)
	record.RunID = strings.TrimSpace(record.RunID)
	record.ScheduledAt = record.ScheduledAt.UTC()
	record.ClaimedAt = record.ClaimedAt.UTC()
	record.LeaseUntil = record.LeaseUntil.UTC()
	return record
}

func normalizeAcceptedAssignment(record flow.AcceptedAssignment) flow.AcceptedAssignment {
	record.Assignment = normalizeFlowAssignment(record.Assignment)
	record.AcceptedAt = record.AcceptedAt.UTC()
	if record.AcceptedAt.IsZero() {
		record.AcceptedAt = time.Now().UTC()
	}
	return record
}

func normalizeFlowAssignment(record flow.Assignment) flow.Assignment {
	record.FlowID = strings.TrimSpace(record.FlowID)
	record.Name = strings.TrimSpace(record.Name)
	record.Target.SwarmID = strings.TrimSpace(record.Target.SwarmID)
	record.Target.Kind = strings.TrimSpace(record.Target.Kind)
	record.Target.DeploymentID = strings.TrimSpace(record.Target.DeploymentID)
	record.Target.Name = strings.TrimSpace(record.Target.Name)
	record.Agent = flow.NormalizeAgentSelection(record.Agent)
	record.Workspace.WorkspacePath = strings.TrimSpace(record.Workspace.WorkspacePath)
	record.Workspace.HostWorkspacePath = strings.TrimSpace(record.Workspace.HostWorkspacePath)
	record.Workspace.RuntimeWorkspacePath = strings.TrimSpace(record.Workspace.RuntimeWorkspacePath)
	record.Workspace.CWD = strings.TrimSpace(record.Workspace.CWD)
	record.Workspace.WorktreeMode = strings.TrimSpace(record.Workspace.WorktreeMode)
	record.Schedule = flow.NormalizeScheduleSpec(record.Schedule)
	record.Schedule.Weekday = strings.TrimSpace(record.Schedule.Weekday)
	record.CatchUpPolicy.Mode = strings.TrimSpace(record.CatchUpPolicy.Mode)
	record.Intent.Prompt = strings.TrimSpace(record.Intent.Prompt)
	record.Intent.Mode = strings.TrimSpace(record.Intent.Mode)
	for i := range record.Intent.Tasks {
		record.Intent.Tasks[i].ID = strings.TrimSpace(record.Intent.Tasks[i].ID)
		record.Intent.Tasks[i].Title = strings.TrimSpace(record.Intent.Tasks[i].Title)
		record.Intent.Tasks[i].Detail = strings.TrimSpace(record.Intent.Tasks[i].Detail)
		record.Intent.Tasks[i].Action = strings.TrimSpace(record.Intent.Tasks[i].Action)
	}
	return record
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
