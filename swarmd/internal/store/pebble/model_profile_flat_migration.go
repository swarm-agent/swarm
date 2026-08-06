package pebblestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

const (
	modelProfileFlatMigrationVersion = 1
	modelProfileFlatMigrationKey     = "meta/migrations/model_profile_flat/v1"
)

// ModelProfileFlatMigrationResult describes the single atomic legacy-profile rewrite.
type ModelProfileFlatMigrationResult struct {
	Version          int  `json:"version"`
	Applied          bool `json:"applied"`
	AlreadyApplied   bool `json:"already_applied"`
	AccountsMigrated int  `json:"accounts_migrated"`
	ProfilesMigrated int  `json:"profiles_migrated"`
	FavoritesCreated int  `json:"favorites_created"`
}

type legacyModelProfileSelection struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier"`
	ContextMode string `json:"context_mode"`
}

type legacyModelProfileRecord struct {
	ProfileID      string                       `json:"profile_id"`
	AccountScopeID string                       `json:"account_scope_id"`
	Name           string                       `json:"name"`
	ModelMode      string                       `json:"model_mode"`
	Single         *legacyModelProfileSelection `json:"single,omitempty"`
	Plan           *legacyModelProfileSelection `json:"plan,omitempty"`
	Auto           *legacyModelProfileSelection `json:"auto,omitempty"`
	CreatedAt      int64                        `json:"created_at"`
	UpdatedAt      int64                        `json:"updated_at"`
	SortOrder      int                          `json:"sort_order"`
}

type legacyModelProfileRow struct {
	key    string
	record legacyModelProfileRecord
}

type modelProfileFlatMigrationMarker struct {
	Version          int `json:"version"`
	AccountsMigrated int `json:"accounts_migrated"`
	ProfilesMigrated int `json:"profiles_migrated"`
	FavoritesCreated int `json:"favorites_created"`
}

// RunModelProfileFlatMigration converts the removed single/split profile schema
// to flat favorites and account mode settings. All rewrites, legacy cleanup, and
// the version marker are committed in one synced batch.
func RunModelProfileFlatMigration(store *Store) (ModelProfileFlatMigrationResult, error) {
	result := ModelProfileFlatMigrationResult{Version: modelProfileFlatMigrationVersion}
	if store == nil || store.db == nil {
		return result, errors.New("model profile flat migration store is not configured")
	}

	store.modelProfilesMu.Lock()
	defer store.modelProfilesMu.Unlock()

	if markerPayload, found, err := store.GetBytes(modelProfileFlatMigrationKey); err != nil {
		return result, fmt.Errorf("read model profile flat migration marker: %w", err)
	} else if found {
		var marker modelProfileFlatMigrationMarker
		if err := decodeStrictJSON(markerPayload, &marker); err != nil {
			return result, fmt.Errorf("decode model profile flat migration marker: %w", err)
		}
		if marker.Version != modelProfileFlatMigrationVersion {
			return result, fmt.Errorf("unsupported model profile flat migration marker version %d", marker.Version)
		}
		result.AlreadyApplied = true
		return result, nil
	}

	rows, profileKeys, err := scanLegacyModelProfileRows(store)
	if err != nil {
		return result, err
	}
	indexKeys, err := scanKeysWithPrefix(store, KeyModelProfileNameAccountPrefix)
	if err != nil {
		return result, fmt.Errorf("scan legacy model profile name indexes: %w", err)
	}
	defaultValues, defaultKeys, err := scanValuesWithPrefix(store, KeyModelProfileDefaultAccountPrefix)
	if err != nil {
		return result, fmt.Errorf("scan legacy model profile defaults: %w", err)
	}

	byAccount := make(map[string][]legacyModelProfileRow)
	for _, row := range rows {
		byAccount[row.record.AccountScopeID] = append(byAccount[row.record.AccountScopeID], row)
	}
	accounts := make([]string, 0, len(byAccount))
	for accountScopeID := range byAccount {
		accounts = append(accounts, accountScopeID)
	}
	sort.Strings(accounts)

	outputs := make(map[string][]ModelProfileRecord, len(accounts))
	defaults := make(map[string]string, len(accounts))
	modeSettings := make(map[string]legacySwarmModeSettingsRecord, len(accounts))
	consumedDefaults := make(map[string]struct{}, len(accounts))

	for _, accountScopeID := range accounts {
		if keyPart(accountScopeID) != accountScopeID {
			return result, fmt.Errorf("legacy account scope id %q is not in canonical storage form", accountScopeID)
		}
		accountRows := byAccount[accountScopeID]
		sort.SliceStable(accountRows, func(i, j int) bool {
			if accountRows[i].record.SortOrder != accountRows[j].record.SortOrder {
				return accountRows[i].record.SortOrder < accountRows[j].record.SortOrder
			}
			left, right := NormalizeModelProfileName(accountRows[i].record.Name), NormalizeModelProfileName(accountRows[j].record.Name)
			if left != right {
				return left < right
			}
			return accountRows[i].record.ProfileID < accountRows[j].record.ProfileID
		})

		defaultKey := KeyModelProfileDefaultForAccount(accountScopeID)
		defaultValue, found := defaultValues[defaultKey]
		if !found || strings.TrimSpace(string(defaultValue)) == "" {
			return result, fmt.Errorf("legacy model profile account %q has no default", accountScopeID)
		}
		consumedDefaults[defaultKey] = struct{}{}
		legacyDefaultID := strings.TrimSpace(string(defaultValue))
		defaultFound := false
		seenIDs, seenNames := map[string]struct{}{}, map[string]struct{}{}
		lastOrder, haveOrder := 0, false

		for _, row := range accountRows {
			legacy := row.record
			startOrder := legacy.SortOrder
			if haveOrder && startOrder <= lastOrder {
				startOrder = lastOrder + 1
			}
			favorites, actionID, _, err := flattenLegacyModelProfile(legacy, startOrder)
			if err != nil {
				return result, fmt.Errorf("migrate legacy model profile %q for account %q: %w", legacy.ProfileID, accountScopeID, err)
			}
			for _, favorite := range favorites {
				idKey := keyPart(favorite.ProfileID)
				nameKey := keyPart(NormalizeModelProfileName(favorite.Name))
				if _, exists := seenIDs[idKey]; exists {
					return result, fmt.Errorf("generated model profile id collision for account %q: %q", accountScopeID, favorite.ProfileID)
				}
				if _, exists := seenNames[nameKey]; exists {
					return result, fmt.Errorf("generated model profile name collision for account %q: %q", accountScopeID, favorite.Name)
				}
				seenIDs[idKey], seenNames[nameKey] = struct{}{}, struct{}{}
				outputs[accountScopeID] = append(outputs[accountScopeID], favorite)
				lastOrder, haveOrder = favorite.SortOrder, true
			}
			if keyPart(legacy.ProfileID) == keyPart(legacyDefaultID) {
				if defaultFound {
					return result, fmt.Errorf("ambiguous legacy default %q for account %q", legacyDefaultID, accountScopeID)
				}
				defaultFound = true
				defaults[accountScopeID] = actionID
				actionSelection := legacy.Auto
				if actionSelection == nil {
					actionSelection = legacy.Single
				}
				planSelection := legacy.Plan
				if planSelection == nil {
					planSelection = actionSelection
				}
				if actionSelection == nil || planSelection == nil {
					return result, fmt.Errorf("legacy default %q for account %q has no Action/Plan selections", legacyDefaultID, accountScopeID)
				}
				modeSettings[accountScopeID] = legacySwarmModeSettingsRecord{
					AccountScopeID: accountScopeID,
					Action:         modelProfileSelectionFromLegacy(*actionSelection),
					Plan:           modelProfileSelectionFromLegacy(*planSelection),
					UpdatedAt:      legacy.UpdatedAt,
				}
			}
		}
		if !defaultFound {
			return result, fmt.Errorf("legacy default %q for account %q is dangling", legacyDefaultID, accountScopeID)
		}
		if _, exists, err := store.GetBytes(swarmModeSettingsKeyForAccount(accountScopeID)); err != nil {
			return result, fmt.Errorf("read swarm mode settings for account %q: %w", accountScopeID, err)
		} else if exists {
			return result, fmt.Errorf("swarm mode settings collision for account %q", accountScopeID)
		}
	}
	if len(consumedDefaults) != len(defaultValues) {
		return result, errors.New("legacy model profile default exists without a matching account")
	}

	batch := store.NewBatch()
	defer batch.Close()
	for _, key := range append(append(profileKeys, indexKeys...), defaultKeys...) {
		if err := batch.Delete([]byte(key), nil); err != nil {
			return result, err
		}
	}
	for _, accountScopeID := range accounts {
		for _, favorite := range outputs[accountScopeID] {
			payload, err := json.Marshal(favorite)
			if err != nil {
				return result, fmt.Errorf("marshal migrated favorite %q: %w", favorite.ProfileID, err)
			}
			if err := batch.Set([]byte(KeyModelProfileForAccount(accountScopeID, favorite.ProfileID)), payload, nil); err != nil {
				return result, err
			}
			if err := batch.Set([]byte(KeyModelProfileNameForAccount(accountScopeID, NormalizeModelProfileName(favorite.Name))), []byte(favorite.ProfileID), nil); err != nil {
				return result, err
			}
		}
		if err := batch.Set([]byte(KeyModelProfileDefaultForAccount(accountScopeID)), []byte(defaults[accountScopeID]), nil); err != nil {
			return result, err
		}
		settingsPayload, err := json.Marshal(modeSettings[accountScopeID])
		if err != nil {
			return result, fmt.Errorf("marshal migrated swarm mode settings: %w", err)
		}
		if err := batch.Set([]byte(swarmModeSettingsKeyForAccount(accountScopeID)), settingsPayload, nil); err != nil {
			return result, err
		}
	}

	result.AccountsMigrated = len(accounts)
	result.ProfilesMigrated = len(rows)
	for _, favorites := range outputs {
		result.FavoritesCreated += len(favorites)
	}
	markerPayload, err := json.Marshal(modelProfileFlatMigrationMarker{
		Version: modelProfileFlatMigrationVersion, AccountsMigrated: result.AccountsMigrated,
		ProfilesMigrated: result.ProfilesMigrated, FavoritesCreated: result.FavoritesCreated,
	})
	if err != nil {
		return result, err
	}
	if err := batch.Set([]byte(modelProfileFlatMigrationKey), markerPayload, nil); err != nil {
		return result, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return ModelProfileFlatMigrationResult{Version: modelProfileFlatMigrationVersion}, fmt.Errorf("commit model profile flat migration: %w", err)
	}
	result.Applied = true
	return result, nil
}

func modelProfileSelectionFromLegacy(selection legacyModelProfileSelection) ModelProfileSelection {
	return ModelProfileSelection{
		Provider: selection.Provider, Model: selection.Model, Thinking: selection.Thinking,
		ServiceTier: selection.ServiceTier, ContextMode: selection.ContextMode,
	}
}

func scanLegacyModelProfileRows(store *Store) ([]legacyModelProfileRow, []string, error) {
	values, keys, err := scanValuesWithPrefix(store, KeyModelProfileAccountPrefix)
	if err != nil {
		return nil, nil, fmt.Errorf("scan legacy model profiles: %w", err)
	}
	rows := make([]legacyModelProfileRow, 0, len(keys))
	for _, key := range keys {
		payload := values[key]
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(payload, &shape); err != nil {
			return nil, nil, fmt.Errorf("decode legacy model profile at %q: %w", key, err)
		}
		_, hasMode := shape["model_mode"]
		_, hasProvider := shape["provider"]
		_, hasModel := shape["model"]
		if !hasMode || hasProvider || hasModel {
			return nil, nil, fmt.Errorf("model profile at %q is not an unambiguous legacy row", key)
		}
		var record legacyModelProfileRecord
		if err := decodeStrictJSON(payload, &record); err != nil {
			return nil, nil, fmt.Errorf("decode legacy model profile at %q: %w", key, err)
		}
		record.ProfileID = strings.TrimSpace(record.ProfileID)
		record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
		record.Name = strings.TrimSpace(record.Name)
		record.ModelMode = strings.ToLower(strings.TrimSpace(record.ModelMode))
		if record.ProfileID == "" || record.AccountScopeID == "" || NormalizeModelProfileName(record.Name) == "" {
			return nil, nil, fmt.Errorf("legacy model profile at %q is missing identity fields", key)
		}
		if keyPart(record.ProfileID) != record.ProfileID {
			return nil, nil, fmt.Errorf("legacy model profile id %q is not in canonical storage form", record.ProfileID)
		}
		if KeyModelProfileForAccount(record.AccountScopeID, record.ProfileID) != key {
			return nil, nil, fmt.Errorf("legacy model profile at %q does not match its storage key", key)
		}
		rows = append(rows, legacyModelProfileRow{key: key, record: record})
	}
	return rows, keys, nil
}

func flattenLegacyModelProfile(legacy legacyModelProfileRecord, startOrder int) ([]ModelProfileRecord, string, string, error) {
	base := func(id, name string, selection *legacyModelProfileSelection, order int) (ModelProfileRecord, error) {
		if selection == nil {
			return ModelProfileRecord{}, errors.New("required model selection is missing")
		}
		provider := strings.TrimSpace(selection.Provider)
		model := strings.TrimSpace(selection.Model)
		thinking := strings.TrimSpace(selection.Thinking)
		if provider == "" || model == "" || thinking == "" {
			return ModelProfileRecord{}, errors.New("model selection provider, model, and thinking are required")
		}
		return ModelProfileRecord{
			ProfileID: id, AccountScopeID: legacy.AccountScopeID, Name: name,
			Provider: provider, Model: model, Thinking: thinking,
			ServiceTier: strings.TrimSpace(selection.ServiceTier), ContextMode: strings.TrimSpace(selection.ContextMode),
			CreatedAt: legacy.CreatedAt, UpdatedAt: legacy.UpdatedAt, SortOrder: order,
		}, nil
	}

	switch legacy.ModelMode {
	case "single":
		if legacy.Single == nil || legacy.Plan != nil || legacy.Auto != nil {
			return nil, "", "", errors.New("single profile must contain only a single selection")
		}
		favorite, err := base(legacy.ProfileID, legacy.Name, legacy.Single, startOrder)
		return []ModelProfileRecord{favorite}, legacy.ProfileID, "", err
	case "split":
		if legacy.Single != nil || legacy.Plan == nil || legacy.Auto == nil {
			return nil, "", "", errors.New("split profile must contain only plan and auto selections")
		}
		actionID, planID := legacy.ProfileID+"_action", legacy.ProfileID+"_plan"
		action, err := base(actionID, legacy.Name+" Action", legacy.Auto, startOrder)
		if err != nil {
			return nil, "", "", err
		}
		plan, err := base(planID, legacy.Name+" Plan", legacy.Plan, startOrder+1)
		if err != nil {
			return nil, "", "", err
		}
		return []ModelProfileRecord{action, plan}, actionID, planID, nil
	default:
		return nil, "", "", fmt.Errorf("unsupported model_mode %q", legacy.ModelMode)
	}
}

func scanKeysWithPrefix(store *Store, prefix string) ([]string, error) {
	_, keys, err := scanValuesWithPrefix(store, prefix)
	return keys, err
}

func scanValuesWithPrefix(store *Store, prefix string) (map[string][]byte, []string, error) {
	iter, err := store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()
	values := make(map[string][]byte)
	keys := make([]string, 0)
	for valid := iter.First(); valid; valid = iter.Next() {
		key := string(append([]byte(nil), iter.Key()...))
		values[key] = append([]byte(nil), iter.Value()...)
		keys = append(keys, key)
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	return values, keys, nil
}

func decodeStrictJSON(payload []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
