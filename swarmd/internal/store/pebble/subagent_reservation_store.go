package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SubagentWaveReservation is the durable, idempotent accounting record for one
// task call. LaunchCount records the complete declared wave/program while
// ActiveCount records only the currently reserved ready cohort. SwarmMode records
// which configured child ceiling authorized the call.
type SubagentWaveReservation struct {
	SessionID      string `json:"session_id"`
	RunID          string `json:"run_id"`
	CallID         string `json:"call_id"`
	ManifestHash   string `json:"manifest_hash"`
	LaunchCount    int    `json:"launch_count"`
	SwarmMode      bool   `json:"swarm_mode,omitempty"`
	Program        bool   `json:"program,omitempty"`
	ReadyCount     int    `json:"ready_count,omitempty"`
	MaxConcurrency int    `json:"max_concurrency,omitempty"`
	ActiveCount    int    `json:"active_count"`
	Status         string `json:"status"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

func (s *PermissionStore) GetSubagentWaveReservation(sessionID, runID, callID string) (SubagentWaveReservation, bool, error) {
	var record SubagentWaveReservation
	ok, err := s.store.GetJSON(KeySubagentWaveReservation(sessionID, runID, callID), &record)
	return record, ok, err
}

func (s *PermissionStore) PutSubagentWaveReservation(record SubagentWaveReservation) error {
	if strings.TrimSpace(record.SessionID) == "" || strings.TrimSpace(record.RunID) == "" || strings.TrimSpace(record.CallID) == "" {
		return errors.New("subagent reservation requires session, run, and call IDs")
	}
	if record.LaunchCount < 1 || record.ActiveCount < 0 || record.ActiveCount > record.LaunchCount || record.ReadyCount < 0 || record.ReadyCount > record.LaunchCount || record.MaxConcurrency < 0 || record.MaxConcurrency > record.LaunchCount {
		return errors.New("subagent reservation counts are invalid")
	}
	if record.Program && record.ReadyCount < 1 {
		return errors.New("subagent program reservation requires at least one ready job")
	}
	if !record.Program && (record.ReadyCount != 0 || record.MaxConcurrency != 0) {
		return errors.New("legacy subagent reservation cannot carry program capacity fields")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt == 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeySubagentWaveReservation(record.SessionID, record.RunID, record.CallID), record); err != nil {
		return fmt.Errorf("persist subagent wave reservation: %w", err)
	}
	return nil
}

func (s *PermissionStore) UpdateSubagentProgramActiveCount(sessionID, runID, callID string, activeCount int, status string) (SubagentWaveReservation, error) {
	record, ok, err := s.GetSubagentWaveReservation(sessionID, runID, callID)
	if err != nil {
		return SubagentWaveReservation{}, err
	}
	if !ok || !record.Program {
		return SubagentWaveReservation{}, errors.New("subagent program reservation not found")
	}
	if activeCount < 0 || activeCount > record.LaunchCount {
		return SubagentWaveReservation{}, errors.New("subagent program active count is invalid")
	}
	record.ActiveCount = activeCount
	if strings.TrimSpace(status) != "" {
		record.Status = strings.TrimSpace(status)
	}
	if err := s.PutSubagentWaveReservation(record); err != nil {
		return SubagentWaveReservation{}, err
	}
	return record, nil
}

func (s *PermissionStore) ListSubagentWaveReservations(sessionID, runID string) ([]SubagentWaveReservation, error) {
	out := make([]SubagentWaveReservation, 0)
	err := s.store.IteratePrefix(SubagentWaveReservationRunPrefix(sessionID, runID), 1000, func(_ string, value []byte) error {
		var record SubagentWaveReservation
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, record)
		return nil
	})
	return out, err
}
