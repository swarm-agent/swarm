package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SubagentWaveReservation is the durable, idempotent accounting record for one
// task call. All delegated child purposes consume LaunchCount from the same run.
type SubagentWaveReservation struct {
	SessionID    string `json:"session_id"`
	RunID        string `json:"run_id"`
	CallID       string `json:"call_id"`
	ManifestHash string `json:"manifest_hash"`
	LaunchCount  int    `json:"launch_count"`
	ActiveCount  int    `json:"active_count"`
	Status       string `json:"status"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
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
	if record.LaunchCount < 1 || record.ActiveCount < 0 || record.ActiveCount > record.LaunchCount {
		return errors.New("subagent reservation counts are invalid")
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
