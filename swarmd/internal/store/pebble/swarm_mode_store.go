package pebblestore

import (
	"errors"
	"strings"
)

var (
	ErrSwarmModeStoreNotConfigured      = errors.New("swarm mode settings store is not configured")
	ErrSwarmModeAccountScopeIDRequired  = errors.New("swarm mode settings account scope id is required")
	ErrSwarmModeActionFavoriteIDRequired = errors.New("swarm mode settings action favorite id is required")
	ErrSwarmModePlanFavoriteIDRequired   = errors.New("swarm mode settings plan favorite id is required when plan mode is enabled")
	ErrSwarmModePlanFavoriteIDUnexpected = errors.New("swarm mode settings plan favorite id must be empty when plan mode is disabled")
	ErrSwarmModeAccountScopeIDMismatch   = errors.New("swarm mode settings account scope id does not match storage scope")
)

const swarmModeSettingsAccountPrefix = "swarm/mode_settings_by_account/"

type SwarmModeSettingsRecord struct {
	AccountScopeID   string `json:"account_scope_id"`
	ActionFavoriteID string `json:"action_favorite_id"`
	PlanEnabled      bool   `json:"plan_enabled"`
	PlanFavoriteID   string `json:"plan_favorite_id,omitempty"`
	UpdatedAt        int64  `json:"updated_at"`
}

type SwarmModeSettingsStore struct {
	store *Store
}

func NewSwarmModeSettingsStore(store *Store) *SwarmModeSettingsStore {
	return &SwarmModeSettingsStore{store: store}
}

func (s *SwarmModeSettingsStore) GetForAccount(accountScopeID string) (SwarmModeSettingsRecord, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return SwarmModeSettingsRecord{}, false, ErrSwarmModeAccountScopeIDRequired
	}
	if s == nil || s.store == nil {
		return SwarmModeSettingsRecord{}, false, ErrSwarmModeStoreNotConfigured
	}

	var record SwarmModeSettingsRecord
	ok, err := s.store.GetJSON(swarmModeSettingsKeyForAccount(accountScopeID), &record)
	if err != nil || !ok {
		return SwarmModeSettingsRecord{}, ok, err
	}
	record = normalizeSwarmModeSettingsRecord(record)
	if record.AccountScopeID != accountScopeID {
		return SwarmModeSettingsRecord{}, false, ErrSwarmModeAccountScopeIDMismatch
	}
	if err := validateSwarmModeSettingsRecord(record); err != nil {
		return SwarmModeSettingsRecord{}, false, err
	}
	return record, true, nil
}

func (s *SwarmModeSettingsStore) PutForAccount(record SwarmModeSettingsRecord) (SwarmModeSettingsRecord, error) {
	if s == nil || s.store == nil {
		return SwarmModeSettingsRecord{}, ErrSwarmModeStoreNotConfigured
	}
	record = normalizeSwarmModeSettingsRecord(record)
	if err := validateSwarmModeSettingsRecord(record); err != nil {
		return SwarmModeSettingsRecord{}, err
	}
	if err := s.store.PutJSON(swarmModeSettingsKeyForAccount(record.AccountScopeID), record); err != nil {
		return SwarmModeSettingsRecord{}, err
	}
	return record, nil
}

func normalizeSwarmModeSettingsRecord(record SwarmModeSettingsRecord) SwarmModeSettingsRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.ActionFavoriteID = strings.TrimSpace(record.ActionFavoriteID)
	record.PlanFavoriteID = strings.TrimSpace(record.PlanFavoriteID)
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func validateSwarmModeSettingsRecord(record SwarmModeSettingsRecord) error {
	if record.AccountScopeID == "" {
		return ErrSwarmModeAccountScopeIDRequired
	}
	if record.ActionFavoriteID == "" {
		return ErrSwarmModeActionFavoriteIDRequired
	}
	if record.PlanEnabled && record.PlanFavoriteID == "" {
		return ErrSwarmModePlanFavoriteIDRequired
	}
	if !record.PlanEnabled && record.PlanFavoriteID != "" {
		return ErrSwarmModePlanFavoriteIDUnexpected
	}
	return nil
}

func swarmModeSettingsKeyForAccount(accountScopeID string) string {
	return swarmModeSettingsAccountPrefix + keyPart(accountScopeID)
}
