package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SessionDeployReservation is the durable, idempotent accounting record for one
// manage-sessions deploy call. Approved deployments remain counted for the full
// parent run, including after their child sessions complete.
type SessionDeployReservation struct {
	SessionID    string `json:"session_id"`
	RunID        string `json:"run_id"`
	CallID       string `json:"call_id"`
	ManifestHash string `json:"manifest_hash"`
	DeployCount  int    `json:"deploy_count"`
	Status       string `json:"status"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

func (s *PermissionStore) GetSessionDeployReservation(sessionID, runID, callID string) (SessionDeployReservation, bool, error) {
	var record SessionDeployReservation
	ok, err := s.store.GetJSON(KeySessionDeployReservation(sessionID, runID, callID), &record)
	return record, ok, err
}

func (s *PermissionStore) PutSessionDeployReservation(record SessionDeployReservation) error {
	if strings.TrimSpace(record.SessionID) == "" || strings.TrimSpace(record.RunID) == "" || strings.TrimSpace(record.CallID) == "" {
		return errors.New("session deployment reservation requires session, run, and call IDs")
	}
	if record.DeployCount < 1 {
		return errors.New("session deployment reservation count is invalid")
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt == 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeySessionDeployReservation(record.SessionID, record.RunID, record.CallID), record); err != nil {
		return fmt.Errorf("persist session deployment reservation: %w", err)
	}
	return nil
}

func (s *PermissionStore) ListSessionDeployReservations(sessionID, runID string) ([]SessionDeployReservation, error) {
	out := make([]SessionDeployReservation, 0)
	err := s.store.IteratePrefix(SessionDeployReservationRunPrefix(sessionID, runID), 1000, func(_ string, value []byte) error {
		var record SessionDeployReservation
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		out = append(out, record)
		return nil
	})
	return out, err
}
