package pebblestore

import (
	"errors"
	"time"
)

type SandboxStateRecord struct {
	Enabled   bool  `json:"enabled"`
	UpdatedAt int64 `json:"updated_at"`
}

type SandboxStore struct {
	store *Store
}

func NewSandboxStore(store *Store) *SandboxStore {
	return &SandboxStore{store: store}
}

func (s *SandboxStore) GetGlobalState() (SandboxStateRecord, bool, error) {
	if s == nil || s.store == nil {
		return SandboxStateRecord{}, false, errors.New("sandbox store is not configured")
	}
	var record SandboxStateRecord
	ok, err := s.store.GetJSON(KeySandboxGlobalState, &record)
	if err != nil {
		return SandboxStateRecord{}, false, err
	}
	if !ok {
		return SandboxStateRecord{Enabled: false, UpdatedAt: 0}, false, nil
	}
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record, true, nil
}

func (s *SandboxStore) SetGlobalState(enabled bool) (SandboxStateRecord, error) {
	if s == nil || s.store == nil {
		return SandboxStateRecord{}, errors.New("sandbox store is not configured")
	}
	record := SandboxStateRecord{
		Enabled:   enabled,
		UpdatedAt: time.Now().UnixMilli(),
	}
	if err := s.store.PutJSON(KeySandboxGlobalState, record); err != nil {
		return SandboxStateRecord{}, err
	}
	return record, nil
}
