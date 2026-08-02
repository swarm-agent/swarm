package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"
)

var (
	ErrSwarmModeStoreNotConfigured     = errors.New("swarm model settings store is not configured")
	ErrSwarmModeAccountScopeIDRequired = errors.New("swarm model settings account scope id is required")
	ErrSwarmModeActionRequired         = errors.New("swarm Action model selection is required")
	ErrSwarmModePlanRequired           = errors.New("swarm Plan model selection is required")
	ErrSwarmModeAccountScopeIDMismatch = errors.New("swarm model settings account scope id does not match storage scope")
)

const swarmModeSettingsAccountPrefix = "swarm/mode_settings_by_account/"

type SwarmModeSettingsRecord struct {
	AccountScopeID string                `json:"account_scope_id"`
	Action         ModelProfileSelection `json:"action"`
	Plan           ModelProfileSelection `json:"plan"`
	UpdatedAt      int64                 `json:"updated_at"`
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

	payload, ok, err := s.store.GetBytes(swarmModeSettingsKeyForAccount(accountScopeID))
	if err != nil || !ok {
		return SwarmModeSettingsRecord{}, ok, err
	}
	var record SwarmModeSettingsRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return SwarmModeSettingsRecord{}, false, err
	}
	record = normalizeSwarmModeSettingsRecord(record)
	if record.AccountScopeID != "" && record.AccountScopeID != accountScopeID {
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
	record.Action = normalizeSwarmModelSelection(record.Action)
	record.Plan = normalizeSwarmModelSelection(record.Plan)
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func validateSwarmModeSettingsRecord(record SwarmModeSettingsRecord) error {
	if record.AccountScopeID == "" {
		return ErrSwarmModeAccountScopeIDRequired
	}
	if !validSwarmModelSelection(record.Action) {
		return ErrSwarmModeActionRequired
	}
	if !validSwarmModelSelection(record.Plan) {
		return ErrSwarmModePlanRequired
	}
	return nil
}

func normalizeSwarmModelSelection(selection ModelProfileSelection) ModelProfileSelection {
	selection.Provider = strings.ToLower(strings.TrimSpace(selection.Provider))
	selection.Model = strings.TrimSpace(selection.Model)
	selection.Thinking = strings.TrimSpace(selection.Thinking)
	selection.ServiceTier = strings.TrimSpace(selection.ServiceTier)
	selection.ContextMode = strings.TrimSpace(selection.ContextMode)
	return selection
}

func validSwarmModelSelection(selection ModelProfileSelection) bool {
	return selection.Provider != "" && selection.Model != "" && selection.Thinking != ""
}

func swarmModeSettingsKeyForAccount(accountScopeID string) string {
	return swarmModeSettingsAccountPrefix + keyPart(accountScopeID)
}
