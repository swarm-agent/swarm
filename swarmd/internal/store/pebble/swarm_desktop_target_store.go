package pebblestore

import (
	"errors"
	"strings"
	"time"
)

type SwarmDesktopTargetSelectionRecord struct {
	SwarmID    string `json:"swarm_id"`
	SelectedAt int64  `json:"selected_at"`
}

type SwarmDesktopTargetSelectionStore struct {
	store *Store
}

func NewSwarmDesktopTargetSelectionStore(store *Store) *SwarmDesktopTargetSelectionStore {
	return &SwarmDesktopTargetSelectionStore{store: store}
}

func (s *SwarmDesktopTargetSelectionStore) Get() (SwarmDesktopTargetSelectionRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmDesktopTargetSelectionRecord{}, false, nil
	}
	var record SwarmDesktopTargetSelectionRecord
	ok, err := s.store.GetJSON(KeySwarmDesktopTargetCurrent, &record)
	if err != nil {
		return SwarmDesktopTargetSelectionRecord{}, false, err
	}
	if !ok {
		return SwarmDesktopTargetSelectionRecord{}, false, nil
	}
	record.SwarmID = strings.TrimSpace(record.SwarmID)
	if record.SelectedAt < 0 {
		record.SelectedAt = 0
	}
	if record.SwarmID == "" {
		return SwarmDesktopTargetSelectionRecord{}, false, nil
	}
	return record, true, nil
}

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
