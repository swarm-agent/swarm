package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

var ErrModelProfileNameConflict = errors.New("model profile name already exists")

const (
	ModelProfileModeSingle = "single"
	ModelProfileModeSplit  = "split"
)

type ModelProfileSelection struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier"`
	ContextMode string `json:"context_mode"`
}

type ModelProfileRecord struct {
	ProfileID      string                 `json:"profile_id"`
	AccountScopeID string                 `json:"account_scope_id"`
	Name           string                 `json:"name"`
	ModelMode      string                 `json:"model_mode"`
	Single         *ModelProfileSelection `json:"single,omitempty"`
	Plan           *ModelProfileSelection `json:"plan,omitempty"`
	Auto           *ModelProfileSelection `json:"auto,omitempty"`
	CreatedAt      int64                  `json:"created_at"`
	UpdatedAt      int64                  `json:"updated_at"`
}

type ModelProfileBulkDeleteResult struct {
	DeletedIDs []string `json:"deleted_ids"`
	MissingIDs []string `json:"missing_ids"`
}

type ModelProfileStore struct {
	store *Store
}

func NewModelProfileStore(store *Store) *ModelProfileStore {
	return &ModelProfileStore{store: store}
}

func NormalizeModelProfileName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (s *ModelProfileStore) GetForAccount(accountScopeID, profileID string) (ModelProfileRecord, bool, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	profileID = strings.TrimSpace(profileID)
	if accountScopeID == "" || profileID == "" {
		return ModelProfileRecord{}, false, nil
	}
	var record ModelProfileRecord
	ok, err := s.store.GetJSON(KeyModelProfileForAccount(accountScopeID, profileID), &record)
	if err != nil || !ok {
		return ModelProfileRecord{}, ok, err
	}
	if record.AccountScopeID != accountScopeID || record.ProfileID != profileID {
		return ModelProfileRecord{}, false, nil
	}
	return record, true, nil
}

func (s *ModelProfileStore) ListForAccount(accountScopeID string, limit int) ([]ModelProfileRecord, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]ModelProfileRecord, 0)
	err := s.store.IteratePrefix(ModelProfilePrefixForAccount(accountScopeID), limit, func(_ string, value []byte) error {
		var record ModelProfileRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		if record.AccountScopeID == accountScopeID {
			out = append(out, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := NormalizeModelProfileName(out[i].Name), NormalizeModelProfileName(out[j].Name)
		if left != right {
			return left < right
		}
		return out[i].ProfileID < out[j].ProfileID
	})
	return out, nil
}

func (s *ModelProfileStore) PutForAccount(record ModelProfileRecord) (ModelProfileRecord, error) {
	if s == nil || s.store == nil {
		return ModelProfileRecord{}, errors.New("model profile store is not configured")
	}
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.ProfileID = strings.TrimSpace(record.ProfileID)
	record.Name = strings.TrimSpace(record.Name)
	if record.AccountScopeID == "" || record.ProfileID == "" || NormalizeModelProfileName(record.Name) == "" {
		return ModelProfileRecord{}, errors.New("account scope id, profile id, and name are required")
	}

	s.store.modelProfilesMu.Lock()
	defer s.store.modelProfilesMu.Unlock()

	existing, exists, err := s.GetForAccount(record.AccountScopeID, record.ProfileID)
	if err != nil {
		return ModelProfileRecord{}, err
	}
	nameKey := KeyModelProfileNameForAccount(record.AccountScopeID, NormalizeModelProfileName(record.Name))
	indexedID, indexed, err := s.store.GetBytes(nameKey)
	if err != nil {
		return ModelProfileRecord{}, err
	}
	if indexed && string(indexedID) != record.ProfileID {
		return ModelProfileRecord{}, ErrModelProfileNameConflict
	}

	payload, err := json.Marshal(record)
	if err != nil {
		return ModelProfileRecord{}, fmt.Errorf("marshal model profile: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyModelProfileForAccount(record.AccountScopeID, record.ProfileID)), payload, nil); err != nil {
		return ModelProfileRecord{}, err
	}
	if err := batch.Set([]byte(nameKey), []byte(record.ProfileID), nil); err != nil {
		return ModelProfileRecord{}, err
	}
	if exists && NormalizeModelProfileName(existing.Name) != NormalizeModelProfileName(record.Name) {
		if err := batch.Delete([]byte(KeyModelProfileNameForAccount(record.AccountScopeID, NormalizeModelProfileName(existing.Name))), nil); err != nil {
			return ModelProfileRecord{}, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return ModelProfileRecord{}, err
	}
	return record, nil
}

func (s *ModelProfileStore) DeleteForAccount(accountScopeID, profileID string) (bool, error) {
	result, err := s.BulkDeleteForAccount(accountScopeID, []string{profileID})
	return len(result.DeletedIDs) == 1, err
}

func (s *ModelProfileStore) BulkDeleteForAccount(accountScopeID string, profileIDs []string) (ModelProfileBulkDeleteResult, error) {
	if s == nil || s.store == nil {
		return ModelProfileBulkDeleteResult{}, errors.New("model profile store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return ModelProfileBulkDeleteResult{}, errors.New("account scope id is required")
	}

	s.store.modelProfilesMu.Lock()
	defer s.store.modelProfilesMu.Unlock()

	result := ModelProfileBulkDeleteResult{DeletedIDs: []string{}, MissingIDs: []string{}}
	records := make([]ModelProfileRecord, 0, len(profileIDs))
	seen := make(map[string]struct{}, len(profileIDs))
	for _, rawID := range profileIDs {
		profileID := strings.TrimSpace(rawID)
		if profileID == "" {
			continue
		}
		if _, duplicate := seen[profileID]; duplicate {
			continue
		}
		seen[profileID] = struct{}{}
		record, ok, err := s.GetForAccount(accountScopeID, profileID)
		if err != nil {
			return ModelProfileBulkDeleteResult{}, err
		}
		if !ok {
			result.MissingIDs = append(result.MissingIDs, profileID)
			continue
		}
		records = append(records, record)
		result.DeletedIDs = append(result.DeletedIDs, profileID)
	}

	batch := s.store.NewBatch()
	defer batch.Close()
	for _, record := range records {
		if err := batch.Delete([]byte(KeyModelProfileForAccount(accountScopeID, record.ProfileID)), nil); err != nil {
			return ModelProfileBulkDeleteResult{}, err
		}
		if err := batch.Delete([]byte(KeyModelProfileNameForAccount(accountScopeID, NormalizeModelProfileName(record.Name))), nil); err != nil {
			return ModelProfileBulkDeleteResult{}, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return ModelProfileBulkDeleteResult{}, err
	}
	return result, nil
}
