package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	agentModelSettingsMigrationVersion = 1
	agentModelSettingsMigrationKey     = "meta/migrations/agent_model_settings/v1"
)

type AgentModelSettingsMigrationResult struct {
	Version          int  `json:"version"`
	Applied          bool `json:"applied"`
	AlreadyApplied   bool `json:"already_applied"`
	AccountsMigrated int  `json:"accounts_migrated"`
	UIRecordsRewrote int  `json:"ui_records_rewritten"`
}

type agentModelSettingsMigrationMarker struct {
	Version          int `json:"version"`
	AccountsMigrated int `json:"accounts_migrated"`
	UIRecordsRewrote int `json:"ui_records_rewritten"`
}

type legacyUIAgentModelAssignments struct {
	Compact  AgentModelAssignment `json:"compact,omitempty"`
	Finder   AgentModelAssignment `json:"finder,omitempty"`
	Coder    AgentModelAssignment `json:"coder,omitempty"`
	Designer AgentModelAssignment `json:"designer,omitempty"`
	Router   AgentModelAssignment `json:"router,omitempty"`
}

type legacyUISettingsMigrationRow struct {
	key       string
	rewritten []byte
	agents    legacyUIAgentModelAssignments
	rewriteUI bool
	updatedAt int64
}

// RunAgentModelSettingsMigration joins the two superseded persistence sources
// into the canonical account-scoped record. The legacy unscoped UI key is not
// accepted: it carries no durable account identity and therefore cannot be
// mapped without risking cross-account data disclosure.
func RunAgentModelSettingsMigration(store *Store) (AgentModelSettingsMigrationResult, error) {
	result := AgentModelSettingsMigrationResult{Version: agentModelSettingsMigrationVersion}
	if store == nil || store.db == nil {
		return result, ErrAgentModelSettingsStoreNotConfigured
	}

	store.agentModelSettingsMu.Lock()
	defer store.agentModelSettingsMu.Unlock()

	if payload, found, err := store.GetBytes(agentModelSettingsMigrationKey); err != nil {
		return result, fmt.Errorf("read agent model settings migration marker: %w", err)
	} else if found {
		var marker agentModelSettingsMigrationMarker
		if err := decodeStrictJSON(payload, &marker); err != nil {
			return result, fmt.Errorf("decode agent model settings migration marker: %w", err)
		}
		if marker.Version != agentModelSettingsMigrationVersion {
			return result, fmt.Errorf("unsupported agent model settings migration marker version %d", marker.Version)
		}
		result.AlreadyApplied = true
		return result, nil
	}

	if _, found, err := store.GetBytes(KeyUISettingsDefault); err != nil {
		return result, fmt.Errorf("read unscoped UI settings: %w", err)
	} else if found {
		return result, errors.New("unscoped UI settings cannot be migrated to an authenticated account")
	}

	canonicalKeys, err := scanKeysWithPrefix(store, KeyAgentModelSettingsAccountPrefix)
	if err != nil {
		return result, fmt.Errorf("scan canonical agent model settings: %w", err)
	}

	modeRows, modeKeys, err := scanLegacySwarmModeSettings(store)
	if err != nil {
		return result, err
	}
	uiRows, err := scanLegacyUISettingsForAgentModels(store)
	if err != nil {
		return result, err
	}
	if len(modeRows) == 0 && len(uiRows) == 0 && len(canonicalKeys) != 0 {
		// A fresh database may initialize the canonical record before the first
		// daemon restart. Accept only well-formed canonical records, then mark the
		// migration complete without rewriting product data.
		for _, key := range canonicalKeys {
			payload, found, err := store.GetBytes(key)
			if err != nil || !found {
				return result, fmt.Errorf("read canonical agent model settings at %q: %w", key, err)
			}
			var record AgentModelSettingsRecord
			if err := decodeStrictJSON(payload, &record); err != nil {
				return result, fmt.Errorf("decode canonical agent model settings at %q: %w", key, err)
			}
			record = normalizeAgentModelSettingsRecord(record)
			if KeyAgentModelSettingsForAccount(record.AccountScopeID) != key {
				return result, fmt.Errorf("canonical agent model settings at %q do not match their storage key", key)
			}
			if err := validateAgentModelSettingsRecord(record); err != nil {
				return result, fmt.Errorf("validate canonical agent model settings at %q: %w", key, err)
			}
		}
	}

	accountsSet := make(map[string]struct{}, len(modeRows)+len(uiRows))
	for accountID := range modeRows {
		accountsSet[accountID] = struct{}{}
	}
	for accountID := range uiRows {
		accountsSet[accountID] = struct{}{}
	}
	accounts := make([]string, 0, len(accountsSet))
	for accountID := range accountsSet {
		accounts = append(accounts, accountID)
	}
	sort.Strings(accounts)

	outputs := make(map[string]AgentModelSettingsRecord)
	preexisting := make(map[string]bool)
	for _, accountID := range accounts {
		mode, hasMode := modeRows[accountID]
		ui, hasUI := uiRows[accountID]
		if !hasMode && hasUI && ui.rewriteUI {
			return result, fmt.Errorf("UI agent settings for account %q have no required Action/Plan settings", accountID)
		}

		if !hasMode {
			continue
		}
		record := AgentModelSettingsRecord{
			AccountScopeID: accountID,
			Swarm: SwarmAgentModelAssignments{
				Action: assignmentFromModelProfileSelection(mode.Action),
				Plan:   assignmentFromModelProfileSelection(mode.Plan),
			},
			UpdatedAt: mode.UpdatedAt,
		}
		if hasUI {
			record.SystemAgents = SystemAgentModelAssignments{
				Compact: ui.agents.Compact, Finder: ui.agents.Finder, Coder: ui.agents.Coder,
				Designer: ui.agents.Designer, Router: ui.agents.Router,
			}
			if ui.updatedAt > record.UpdatedAt {
				record.UpdatedAt = ui.updatedAt
			}
		}
		record = normalizeAgentModelSettingsRecord(record)
		if err := validateAgentModelSettingsRecord(record); err != nil {
			return result, fmt.Errorf("validate migrated agent model settings for account %q: %w", accountID, err)
		}
		key := KeyAgentModelSettingsForAccount(accountID)
		if payload, found, err := store.GetBytes(key); err != nil {
			return result, fmt.Errorf("read canonical agent model settings for account %q: %w", accountID, err)
		} else if found {
			var canonical AgentModelSettingsRecord
			if err := decodeStrictJSON(payload, &canonical); err != nil {
				return result, fmt.Errorf("decode canonical agent model settings for account %q: %w", accountID, err)
			}
			canonical = normalizeAgentModelSettingsRecord(canonical)
			if err := validateAgentModelSettingsRecord(canonical); err != nil {
				return result, fmt.Errorf("validate canonical agent model settings for account %q: %w", accountID, err)
			}
			if canonical.AccountScopeID != record.AccountScopeID || canonical.Swarm != record.Swarm || canonical.SystemAgents != record.SystemAgents {
				return result, fmt.Errorf("canonical agent model settings conflict for account %q", accountID)
			}
			outputs[accountID] = canonical
			preexisting[accountID] = true
			continue
		}
		outputs[accountID] = record
	}

	batch := store.NewBatch()
	defer batch.Close()
	for _, key := range modeKeys {
		if err := batch.Delete([]byte(key), nil); err != nil {
			return result, err
		}
	}
	for _, accountID := range accounts {
		if ui, ok := uiRows[accountID]; ok && ui.rewriteUI {
			if err := batch.Set([]byte(ui.key), ui.rewritten, nil); err != nil {
				return result, err
			}
			result.UIRecordsRewrote++
		}
		if record, ok := outputs[accountID]; ok {
			payload, err := json.Marshal(record)
			if err != nil {
				return result, fmt.Errorf("marshal migrated agent model settings for account %q: %w", accountID, err)
			}
			if err := batch.Set([]byte(KeyAgentModelSettingsForAccount(accountID)), payload, nil); err != nil {
				return result, err
			}
			if !preexisting[accountID] {
				result.AccountsMigrated++
			}
		}
	}
	markerPayload, err := json.Marshal(agentModelSettingsMigrationMarker{
		Version: agentModelSettingsMigrationVersion, AccountsMigrated: result.AccountsMigrated, UIRecordsRewrote: result.UIRecordsRewrote,
	})
	if err != nil {
		return result, err
	}
	if err := batch.Set([]byte(agentModelSettingsMigrationKey), markerPayload, nil); err != nil {
		return result, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return AgentModelSettingsMigrationResult{Version: agentModelSettingsMigrationVersion}, fmt.Errorf("commit agent model settings migration: %w", err)
	}
	result.Applied = true
	return result, nil
}

func scanLegacySwarmModeSettings(store *Store) (map[string]legacySwarmModeSettingsRecord, []string, error) {
	values, keys, err := scanValuesWithPrefix(store, swarmModeSettingsAccountPrefix)
	if err != nil {
		return nil, nil, fmt.Errorf("scan legacy swarm mode settings: %w", err)
	}
	rows := make(map[string]legacySwarmModeSettingsRecord, len(keys))
	for _, key := range keys {
		var record legacySwarmModeSettingsRecord
		if err := decodeStrictJSON(values[key], &record); err != nil {
			return nil, nil, fmt.Errorf("decode legacy swarm mode settings at %q: %w", key, err)
		}
		record = normalizeLegacySwarmModeSettingsRecord(record)
		accountID := NormalizeAgentModelAccountScopeID(record.AccountScopeID)
		if accountID == "" || accountID != record.AccountScopeID {
			return nil, nil, fmt.Errorf("legacy swarm mode settings at %q have a noncanonical account scope", key)
		}
		if swarmModeSettingsKeyForAccount(accountID) != key {
			return nil, nil, fmt.Errorf("legacy swarm mode settings at %q do not match their storage key", key)
		}
		if err := validateLegacySwarmModeSettingsRecord(record); err != nil {
			return nil, nil, fmt.Errorf("validate legacy swarm mode settings at %q: %w", key, err)
		}
		if _, exists := rows[accountID]; exists {
			return nil, nil, fmt.Errorf("duplicate legacy swarm mode settings for account %q", accountID)
		}
		rows[accountID] = record
	}
	return rows, keys, nil
}

func scanLegacyUISettingsForAgentModels(store *Store) (map[string]legacyUISettingsMigrationRow, error) {
	values, keys, err := scanValuesWithPrefix(store, KeyUISettingsAccountPrefix)
	if err != nil {
		return nil, fmt.Errorf("scan account UI settings: %w", err)
	}
	rows := make(map[string]legacyUISettingsMigrationRow, len(keys))
	for _, key := range keys {
		encodedAccount := strings.TrimPrefix(key, KeyUISettingsAccountPrefix)
		accountID, err := url.PathUnescape(encodedAccount)
		if err != nil {
			return nil, fmt.Errorf("decode UI settings account key %q: %w", key, err)
		}
		accountID = NormalizeAgentModelAccountScopeID(accountID)
		if accountID == "" || KeyUISettingsForAccount(accountID) != key {
			return nil, fmt.Errorf("UI settings at %q have a noncanonical account storage key", key)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(values[key], &object); err != nil || object == nil {
			if err == nil {
				err = errors.New("JSON object required")
			}
			return nil, fmt.Errorf("decode UI settings at %q: %w", key, err)
		}
		row := legacyUISettingsMigrationRow{key: key}
		if rawUpdatedAt, ok := object["updated_at"]; ok {
			if err := json.Unmarshal(rawUpdatedAt, &row.updatedAt); err != nil {
				return nil, fmt.Errorf("decode UI settings updated_at at %q: %w", key, err)
			}
		}
		if rawAgents, ok := object["agents"]; ok {
			row.rewriteUI = true
			if strings.TrimSpace(string(rawAgents)) != "null" {
				if err := decodeStrictJSON(rawAgents, &row.agents); err != nil {
					return nil, fmt.Errorf("decode legacy UI agents at %q: %w", key, err)
				}
			}
			row.agents.Compact = NormalizeAgentModelAssignment(row.agents.Compact)
			row.agents.Finder = NormalizeAgentModelAssignment(row.agents.Finder)
			row.agents.Coder = NormalizeAgentModelAssignment(row.agents.Coder)
			row.agents.Designer = NormalizeAgentModelAssignment(row.agents.Designer)
			row.agents.Router = NormalizeAgentModelAssignment(row.agents.Router)
			delete(object, "agents")
			row.rewritten, err = json.Marshal(object)
			if err != nil {
				return nil, fmt.Errorf("rewrite UI settings at %q: %w", key, err)
			}
		}
		if _, exists := rows[accountID]; exists {
			return nil, fmt.Errorf("duplicate UI settings for account %q", accountID)
		}
		rows[accountID] = row
	}
	return rows, nil
}

func assignmentFromModelProfileSelection(selection ModelProfileSelection) AgentModelAssignment {
	return AgentModelAssignment{
		Provider: selection.Provider, Model: selection.Model, Thinking: selection.Thinking,
		ServiceTier: selection.ServiceTier, ContextMode: selection.ContextMode,
	}
}
