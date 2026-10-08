package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// SubagentWaveReservation is the durable, idempotent accounting record for one
// task call. LaunchCount records the complete declared wave/program while
// ActiveCount records only the currently reserved ready cohort. SwarmMode records
// which configured child ceiling authorized the call.
type SubagentWaveReservation struct {
	AccountScopeID string `json:"account_scope_id,omitempty"`
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
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySubagentWaveReservation(record.SessionID, record.RunID, record.CallID)), payload, nil); err != nil {
		return err
	}
	if record.AccountScopeID != "" && record.SwarmMode {
		key := []byte("subagent_swarm_active/" + keyPart(record.AccountScopeID) + "/" + keyPart(record.SessionID) + "/" + keyPart(record.RunID) + "/" + keyPart(record.CallID))
		if record.ActiveCount > 0 {
			err = batch.Set(key, payload, nil)
		} else {
			err = batch.Delete(key, nil)
		}
		if err != nil {
			return err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
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

// CountAccountSwarmChildren reads only active reservations, never task history.
func (s *PermissionStore) CountAccountSwarmChildren(accountID string) (int, error) {
	count := 0
	err := s.iterateSwarmReservations("subagent_swarm_active/"+keyPart(accountID)+"/", func(_ string, value []byte) error {
		var record SubagentWaveReservation
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		count += record.ActiveCount
		return nil
	})
	return count, err
}

// BackfillAccountSwarmReservations upgrades pre-account reservation records once
// before the first account admission. Admission must fail closed on errors.
func (s *PermissionStore) BackfillAccountSwarmReservations() error {
	return s.iterateSwarmReservations("subagent_reservation/", func(_ string, value []byte) error {
		var record SubagentWaveReservation
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if !record.SwarmMode || record.ActiveCount == 0 || record.AccountScopeID != "" {
			return nil
		}
		session, found, err := NewSessionStore(s.store).GetSession(record.SessionID)
		if err != nil {
			return err
		}
		if !found || session.AccountScopeID == "" {
			return errors.New("active legacy Swarm reservation has no account owner")
		}
		record.AccountScopeID = session.AccountScopeID
		return s.PutSubagentWaveReservation(record)
	})
}

// Page without truncating accounting at the generic iterator's default limit.
func (s *PermissionStore) iterateSwarmReservations(prefix string, visit func(string, []byte) error) error {
	start := ""
	for {
		count := 0
		err := scanRangeFromReader(s.store.db, scanRangeOptions{Prefix: prefix, StartKey: start, Limit: 256}, func(key string, value []byte) (bool, error) {
			count++
			start = key + "\x00"
			return true, visit(key, value)
		})
		if err != nil || count < 256 {
			return err
		}
	}
}
