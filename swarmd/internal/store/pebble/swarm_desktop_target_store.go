package pebblestore

import (
	"errors"
	"strings"
	"time"
)

type SwarmDesktopTargetSelectionRecord struct {
	SwarmID        string `json:"swarm_id"`
	UserID         string `json:"user_id,omitempty"`
	AccountScopeID string `json:"account_scope_id,omitempty"`
	SelectedAt     int64  `json:"selected_at"`
}

type SwarmDesktopTargetSelectionStore struct {
	store *Store
}

func NewSwarmDesktopTargetSelectionStore(store *Store) *SwarmDesktopTargetSelectionStore {
	return &SwarmDesktopTargetSelectionStore{store: store}
}

// Get reads the legacy global desktop target selection. Product runtime paths
// must use GetForAccount so an account miss never falls back to this mutable key.
func (s *SwarmDesktopTargetSelectionStore) Get() (SwarmDesktopTargetSelectionRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmDesktopTargetSelectionRecord{}, false, nil
	}
	var record SwarmDesktopTargetSelectionRecord
	ok, err := s.store.GetJSON(KeySwarmDesktopTargetCurrent, &record)
	if err != nil || !ok {
		return SwarmDesktopTargetSelectionRecord{}, ok, err
	}
	return normalizeSwarmDesktopTargetSelectionRecord(record), normalizeSwarmDesktopTargetSelectionRecord(record).SwarmID != "", nil
}

// Put writes the legacy global desktop target selection. Product runtime paths
// must use PutForAccount; this method is retained only for explicit migration or
// system-internal uses that intentionally operate outside account scope.
func (s *SwarmDesktopTargetSelectionStore) Put(swarmID string) (SwarmDesktopTargetSelectionRecord, error) {
	if s == nil || s.store == nil {
		return SwarmDesktopTargetSelectionRecord{}, errors.New("swarm desktop target selection store is not configured")
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return SwarmDesktopTargetSelectionRecord{}, errors.New("swarm id is required")
	}
	record := SwarmDesktopTargetSelectionRecord{
		SwarmID:    swarmID,
		SelectedAt: time.Now().UnixMilli(),
	}
	if err := s.store.PutJSON(KeySwarmDesktopTargetCurrent, record); err != nil {
		return SwarmDesktopTargetSelectionRecord{}, err
	}
	return record, nil
}

func (s *SwarmDesktopTargetSelectionStore) GetForAccount(accountScopeID string) (SwarmDesktopTargetSelectionRecord, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return SwarmDesktopTargetSelectionRecord{}, false, errors.New("account scope id is required")
	}
	if s == nil || s.store == nil {
		return SwarmDesktopTargetSelectionRecord{}, false, nil
	}
	var record SwarmDesktopTargetSelectionRecord
	ok, err := s.store.GetJSON(KeySwarmDesktopTargetCurrentForAccount(accountScopeID), &record)
	if err != nil || !ok {
		return SwarmDesktopTargetSelectionRecord{}, ok, err
	}
	record = normalizeSwarmDesktopTargetSelectionRecord(record)
	if record.SwarmID == "" {
		return SwarmDesktopTargetSelectionRecord{}, false, nil
	}
	if strings.TrimSpace(record.AccountScopeID) != "" && record.AccountScopeID != accountScopeID {
		return SwarmDesktopTargetSelectionRecord{}, false, nil
	}
	record.AccountScopeID = accountScopeID
	return record, true, nil
}

func (s *SwarmDesktopTargetSelectionStore) PutForAccount(accountScopeID, userID, swarmID string) (SwarmDesktopTargetSelectionRecord, error) {
	if s == nil || s.store == nil {
		return SwarmDesktopTargetSelectionRecord{}, errors.New("swarm desktop target selection store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return SwarmDesktopTargetSelectionRecord{}, errors.New("account scope id is required")
	}
	swarmID = strings.TrimSpace(swarmID)
	if swarmID == "" {
		return SwarmDesktopTargetSelectionRecord{}, errors.New("swarm id is required")
	}
	record := SwarmDesktopTargetSelectionRecord{
		SwarmID:        swarmID,
		UserID:         strings.TrimSpace(userID),
		AccountScopeID: accountScopeID,
		SelectedAt:     time.Now().UnixMilli(),
	}
	if err := s.store.PutJSON(KeySwarmDesktopTargetCurrentForAccount(accountScopeID), record); err != nil {
		return SwarmDesktopTargetSelectionRecord{}, err
	}
	return record, nil
}

func normalizeSwarmDesktopTargetSelectionRecord(record SwarmDesktopTargetSelectionRecord) SwarmDesktopTargetSelectionRecord {
	record.SwarmID = strings.TrimSpace(record.SwarmID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	if record.SelectedAt < 0 {
		record.SelectedAt = 0
	}
	return record
}
