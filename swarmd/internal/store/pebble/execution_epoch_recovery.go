package pebblestore

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ExecutionEpochRecoveryStatusInProgress = "in_progress"
	ExecutionEpochRecoveryStatusCompleted  = "completed"
	ExecutionEpochRecoveryStatusSkipped    = "skipped"
	ExecutionEpochRecoveryStatusFailed     = "failed"
)

// ExecutionEpochRecovery is the durable, payload-free recovery projection for
// one execution epoch. The first claim permanently consumes the epoch's single
// recovery allowance, including after a daemon restart or a terminal failure.
type ExecutionEpochRecovery struct {
	SessionID     string `json:"session_id"`
	EpochID       string `json:"epoch_id"`
	OwnerRunID    string `json:"owner_run_id,omitempty"`
	Status        string `json:"status"`
	Phase         string `json:"phase,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Attempts      int    `json:"attempts"`
	Outcome       string `json:"outcome,omitempty"`
	StartedAt     int64  `json:"started_at,omitempty"`
	UpdatedAt     int64  `json:"updated_at"`
	CooldownUntil int64  `json:"cooldown_until,omitempty"`
}

func KeyExecutionEpochRecovery(sessionID, epochID string) string {
	return fmt.Sprintf("v3/execution_epoch_recovery/%s/%s", keyPart(sessionID), keyPart(epochID))
}

func (s *SessionStore) GetExecutionEpochRecovery(sessionID, epochID string) (ExecutionEpochRecovery, bool, error) {
	var record ExecutionEpochRecovery
	sessionID = strings.TrimSpace(sessionID)
	epochID = strings.TrimSpace(epochID)
	if sessionID == "" || epochID == "" {
		return record, false, errors.New("session id and epoch id are required")
	}
	ok, err := s.store.GetJSON(KeyExecutionEpochRecovery(sessionID, epochID), &record)
	return record, ok, err
}

// ClaimExecutionEpochRecovery atomically grants the epoch's only recovery
// attempt. Every existing record is a permanent no-op fence.
func (s *SessionStore) ClaimExecutionEpochRecovery(sessionID, epochID, ownerRunID string, now int64) (ExecutionEpochRecovery, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	epochID = strings.TrimSpace(epochID)
	ownerRunID = strings.TrimSpace(ownerRunID)
	if s == nil || s.store == nil {
		return ExecutionEpochRecovery{}, false, errors.New("session store is not configured")
	}
	if sessionID == "" || epochID == "" || ownerRunID == "" {
		return ExecutionEpochRecovery{}, false, errors.New("session id, epoch id, and recovery owner run id are required")
	}
	unlock := s.store.sessionMutations.lockSessions(sessionID)
	defer unlock()

	record, ok, err := s.GetExecutionEpochRecovery(sessionID, epochID)
	if err != nil {
		return ExecutionEpochRecovery{}, false, err
	}
	if ok {
		return record, false, nil
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	record.SessionID = sessionID
	record.EpochID = epochID
	record.OwnerRunID = ownerRunID
	record.Status = ExecutionEpochRecoveryStatusInProgress
	record.Phase = "detected"
	record.Attempts = 1
	record.Outcome = ""
	record.StartedAt = now
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyExecutionEpochRecovery(sessionID, epochID), record); err != nil {
		return ExecutionEpochRecovery{}, false, err
	}
	return record, true, nil
}

func (s *SessionStore) UpdateExecutionEpochRecoveryPhase(sessionID, epochID, ownerRunID, phase, reason string, now int64) (ExecutionEpochRecovery, error) {
	sessionID = strings.TrimSpace(sessionID)
	epochID = strings.TrimSpace(epochID)
	ownerRunID = strings.TrimSpace(ownerRunID)
	phase = strings.TrimSpace(phase)
	if s == nil || s.store == nil {
		return ExecutionEpochRecovery{}, errors.New("session store is not configured")
	}
	if phase == "" {
		return ExecutionEpochRecovery{}, errors.New("execution epoch recovery phase is required")
	}
	unlock := s.store.sessionMutations.lockSessions(sessionID)
	defer unlock()
	record, ok, err := s.GetExecutionEpochRecovery(sessionID, epochID)
	if err != nil {
		return ExecutionEpochRecovery{}, err
	}
	if !ok || record.Status != ExecutionEpochRecoveryStatusInProgress || record.OwnerRunID != ownerRunID {
		return ExecutionEpochRecovery{}, errors.New("execution epoch recovery is not owned by this run")
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	record.Phase = phase
	record.Reason = strings.TrimSpace(reason)
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyExecutionEpochRecovery(sessionID, epochID), record); err != nil {
		return ExecutionEpochRecovery{}, err
	}
	return record, nil
}

func (s *SessionStore) FinishExecutionEpochRecovery(sessionID, epochID, ownerRunID, status, outcome string, now int64) (ExecutionEpochRecovery, error) {
	sessionID = strings.TrimSpace(sessionID)
	epochID = strings.TrimSpace(epochID)
	ownerRunID = strings.TrimSpace(ownerRunID)
	status = strings.ToLower(strings.TrimSpace(status))
	if s == nil || s.store == nil {
		return ExecutionEpochRecovery{}, errors.New("session store is not configured")
	}
	if status != ExecutionEpochRecoveryStatusCompleted && status != ExecutionEpochRecoveryStatusSkipped && status != ExecutionEpochRecoveryStatusFailed {
		return ExecutionEpochRecovery{}, fmt.Errorf("unsupported execution epoch recovery status %q", status)
	}
	unlock := s.store.sessionMutations.lockSessions(sessionID)
	defer unlock()
	record, ok, err := s.GetExecutionEpochRecovery(sessionID, epochID)
	if err != nil {
		return ExecutionEpochRecovery{}, err
	}
	if !ok || record.Status != ExecutionEpochRecoveryStatusInProgress || record.OwnerRunID != ownerRunID {
		return ExecutionEpochRecovery{}, errors.New("execution epoch recovery is not owned by this run")
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	record.Status = status
	record.Phase = status
	record.Outcome = strings.TrimSpace(outcome)
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyExecutionEpochRecovery(sessionID, epochID), record); err != nil {
		return ExecutionEpochRecovery{}, err
	}
	return record, nil
}
