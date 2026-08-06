package pebblestore

import (
	"errors"
	"strings"
)

// Legacy Swarm mode records exist only as input/output between the ordered
// model-profile and agent-model-settings startup migrations. Runtime code must
// use AgentModelSettingsStore instead.
const swarmModeSettingsAccountPrefix = "swarm/mode_settings_by_account/"

var (
	errLegacySwarmModeAccountRequired = errors.New("legacy swarm model settings account scope id is required")
	errLegacySwarmModeActionRequired  = errors.New("legacy swarm Action model selection is required")
	errLegacySwarmModePlanRequired    = errors.New("legacy swarm Plan model selection is required")
)

type legacySwarmModeSettingsRecord struct {
	AccountScopeID string                `json:"account_scope_id"`
	Action         ModelProfileSelection `json:"action"`
	Plan           ModelProfileSelection `json:"plan"`
	UpdatedAt      int64                 `json:"updated_at"`
}

func normalizeLegacySwarmModeSettingsRecord(record legacySwarmModeSettingsRecord) legacySwarmModeSettingsRecord {
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.Action = normalizeLegacySwarmModelSelection(record.Action)
	record.Plan = normalizeLegacySwarmModelSelection(record.Plan)
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func validateLegacySwarmModeSettingsRecord(record legacySwarmModeSettingsRecord) error {
	if record.AccountScopeID == "" {
		return errLegacySwarmModeAccountRequired
	}
	if !validLegacySwarmModelSelection(record.Action) {
		return errLegacySwarmModeActionRequired
	}
	if !validLegacySwarmModelSelection(record.Plan) {
		return errLegacySwarmModePlanRequired
	}
	return nil
}

func normalizeLegacySwarmModelSelection(selection ModelProfileSelection) ModelProfileSelection {
	selection.Provider = strings.ToLower(strings.TrimSpace(selection.Provider))
	selection.Model = strings.TrimSpace(selection.Model)
	selection.Thinking = strings.TrimSpace(selection.Thinking)
	selection.ServiceTier = strings.TrimSpace(selection.ServiceTier)
	selection.ContextMode = strings.TrimSpace(selection.ContextMode)
	return selection
}

func validLegacySwarmModelSelection(selection ModelProfileSelection) bool {
	return selection.Provider != "" && selection.Model != "" && selection.Thinking != ""
}

func swarmModeSettingsKeyForAccount(accountScopeID string) string {
	return swarmModeSettingsAccountPrefix + keyPart(accountScopeID)
}
