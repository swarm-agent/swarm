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
var ErrModelProfileNotFound = errors.New("model profile not found")

type ModelProfileRecord struct {
	ProfileID      string `json:"profile_id"`
	AccountScopeID string `json:"account_scope_id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Thinking       string `json:"thinking"`
	ServiceTier    string `json:"service_tier"`
	ContextMode    string `json:"context_mode"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	SortOrder      int    `json:"sort_order"`
	IsDefault      bool   `json:"-"`
}

type ModelProfileListState struct {
	Profiles         []ModelProfileRecord
	DefaultProfileID string
}

type ModelProfileBulkDeleteResult struct {
	DeletedIDs []string `json:"deleted_ids"`
	MissingIDs []string `json:"missing_ids"`
}

type ModelProfileStore struct{ store *Store }

func NewModelProfileStore(store *Store) *ModelProfileStore { return &ModelProfileStore{store: store} }

func NormalizeModelProfileName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func (s *ModelProfileStore) GetForAccount(accountScopeID, profileID string) (ModelProfileRecord, bool, error) {
	accountScopeID, profileID = strings.TrimSpace(accountScopeID), strings.TrimSpace(profileID)
	if accountScopeID == "" || profileID == "" {
		return ModelProfileRecord{}, false, nil
	}
	state, err := s.ListStateForAccount(accountScopeID, 500)
	if err != nil {
		return ModelProfileRecord{}, false, err
	}
	record, ok := findModelProfile(state.Profiles, profileID)
	return record, ok, nil
}

func (s *ModelProfileStore) ListForAccount(accountScopeID string, limit int) ([]ModelProfileRecord, error) {
	state, err := s.ListStateForAccount(accountScopeID, limit)
	return state.Profiles, err
}

// ListStateForAccount returns profiles and repairs legacy missing/dangling defaults atomically.
func (s *ModelProfileStore) ListStateForAccount(accountScopeID string, limit int) (ModelProfileListState, error) {
	if s == nil || s.store == nil {
		return ModelProfileListState{}, errors.New("model profile store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return ModelProfileListState{}, errors.New("account scope id is required")
	}
	s.store.modelProfilesMu.Lock()
	defer s.store.modelProfilesMu.Unlock()
	profiles, err := s.listUnlocked(accountScopeID, limit)
	if err != nil {
		return ModelProfileListState{}, err
	}
	defaultID, _, err := s.defaultIDUnlocked(accountScopeID)
	if err != nil {
		return ModelProfileListState{}, err
	}
	if !containsModelProfile(profiles, defaultID) {
		if len(profiles) == 0 {
			defaultID = ""
			if err := s.store.Delete(KeyModelProfileDefaultForAccount(accountScopeID)); err != nil {
				return ModelProfileListState{}, err
			}
		} else {
			defaultID = profiles[0].ProfileID
			if err := s.store.PutBytes(KeyModelProfileDefaultForAccount(accountScopeID), []byte(defaultID)); err != nil {
				return ModelProfileListState{}, err
			}
		}
	}
	for i := range profiles {
		profiles[i].IsDefault = profiles[i].ProfileID == defaultID
	}
	return ModelProfileListState{Profiles: profiles, DefaultProfileID: defaultID}, nil
}

func (s *ModelProfileStore) listUnlocked(accountScopeID string, limit int) ([]ModelProfileRecord, error) {
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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		left, right := NormalizeModelProfileName(out[i].Name), NormalizeModelProfileName(out[j].Name)
		if left != right {
			return left < right
		}
		return out[i].ProfileID < out[j].ProfileID
	})
	return out, nil
}

func (s *ModelProfileStore) PutForAccount(record ModelProfileRecord) (ModelProfileRecord, error) {
	return s.putForAccount(record, false)
}

// PutForAccountIfEmpty creates the onboarding profile only while the account has no records.
func (s *ModelProfileStore) PutForAccountIfEmpty(record ModelProfileRecord) (ModelProfileRecord, bool, error) {
	stored, err := s.putForAccount(record, true)
	if errors.Is(err, errModelProfileAccountNotEmpty) {
		return ModelProfileRecord{}, false, nil
	}
	return stored, err == nil, err
}

var errModelProfileAccountNotEmpty = errors.New("model profile account is not empty")

func (s *ModelProfileStore) putForAccount(record ModelProfileRecord, onlyIfEmpty bool) (ModelProfileRecord, error) {
	if s == nil || s.store == nil {
		return ModelProfileRecord{}, errors.New("model profile store is not configured")
	}
	record.AccountScopeID, record.ProfileID, record.Name = strings.TrimSpace(record.AccountScopeID), strings.TrimSpace(record.ProfileID), strings.TrimSpace(record.Name)
	record.IsDefault = false
	if record.AccountScopeID == "" || record.ProfileID == "" || NormalizeModelProfileName(record.Name) == "" {
		return ModelProfileRecord{}, errors.New("account scope id, profile id, and name are required")
	}
	s.store.modelProfilesMu.Lock()
	defer s.store.modelProfilesMu.Unlock()
	profiles, err := s.listUnlocked(record.AccountScopeID, 500)
	if err != nil {
		return ModelProfileRecord{}, err
	}
	if onlyIfEmpty && len(profiles) != 0 {
		return ModelProfileRecord{}, errModelProfileAccountNotEmpty
	}
	existing, exists := findModelProfile(profiles, record.ProfileID)
	if !exists && len(profiles) > 0 {
		maxOrder := profiles[0].SortOrder
		for _, profile := range profiles[1:] {
			if profile.SortOrder > maxOrder {
				maxOrder = profile.SortOrder
			}
		}
		record.SortOrder = maxOrder + 1
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
	defaultID, _, err := s.defaultIDUnlocked(record.AccountScopeID)
	if err != nil {
		return ModelProfileRecord{}, err
	}
	if !containsModelProfile(profiles, defaultID) {
		defaultID = ""
	}
	if defaultID == "" {
		defaultID = record.ProfileID
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeyModelProfileForAccount(record.AccountScopeID, record.ProfileID)), payload, nil); err != nil {
		return ModelProfileRecord{}, err
	}
	if err := batch.Set([]byte(nameKey), []byte(record.ProfileID), nil); err != nil {
		return ModelProfileRecord{}, err
	}
	if err := batch.Set([]byte(KeyModelProfileDefaultForAccount(record.AccountScopeID)), []byte(defaultID), nil); err != nil {
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
	record.IsDefault = record.ProfileID == defaultID
	return record, nil
}

func (s *ModelProfileStore) SetDefaultForAccount(accountScopeID, profileID string) (ModelProfileRecord, error) {
	if s == nil || s.store == nil {
		return ModelProfileRecord{}, errors.New("model profile store is not configured")
	}
	accountScopeID, profileID = strings.TrimSpace(accountScopeID), strings.TrimSpace(profileID)
	if accountScopeID == "" || profileID == "" {
		return ModelProfileRecord{}, errors.New("account scope id and profile id are required")
	}
	s.store.modelProfilesMu.Lock()
	defer s.store.modelProfilesMu.Unlock()
	profiles, err := s.listUnlocked(accountScopeID, 500)
	if err != nil {
		return ModelProfileRecord{}, err
	}
	record, ok := findModelProfile(profiles, profileID)
	if !ok {
		return ModelProfileRecord{}, ErrModelProfileNotFound
	}
	if err := s.store.PutBytes(KeyModelProfileDefaultForAccount(accountScopeID), []byte(profileID)); err != nil {
		return ModelProfileRecord{}, err
	}
	record.IsDefault = true
	return record, nil
}

func (s *ModelProfileStore) ReorderForAccount(accountScopeID string, profileIDs []string) ([]ModelProfileRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("model profile store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	s.store.modelProfilesMu.Lock()
	defer s.store.modelProfilesMu.Unlock()
	profiles, err := s.listUnlocked(accountScopeID, 500)
	if err != nil {
		return nil, err
	}
	if len(profileIDs) != len(profiles) {
		return nil, errors.New("profile_ids must include every model profile exactly once")
	}
	byID := make(map[string]ModelProfileRecord, len(profiles))
	for _, profile := range profiles {
		byID[profile.ProfileID] = profile
	}
	ordered := make([]ModelProfileRecord, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for index, rawID := range profileIDs {
		profileID := strings.TrimSpace(rawID)
		profile, ok := byID[profileID]
		if !ok {
			return nil, ErrModelProfileNotFound
		}
		if _, duplicate := seen[profileID]; duplicate {
			return nil, errors.New("profile_ids must include every model profile exactly once")
		}
		seen[profileID] = struct{}{}
		profile.SortOrder = index
		ordered = append(ordered, profile)
	}
	defaultID, _, err := s.defaultIDUnlocked(accountScopeID)
	if err != nil {
		return nil, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for i := range ordered {
		ordered[i].IsDefault = ordered[i].ProfileID == defaultID
		payload, err := json.Marshal(ordered[i])
		if err != nil {
			return nil, fmt.Errorf("marshal model profile: %w", err)
		}
		if err := batch.Set([]byte(KeyModelProfileForAccount(accountScopeID, ordered[i].ProfileID)), payload, nil); err != nil {
			return nil, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return ordered, nil
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
	all, err := s.listUnlocked(accountScopeID, 500)
	if err != nil {
		return ModelProfileBulkDeleteResult{}, err
	}
	byID := make(map[string]ModelProfileRecord, len(all))
	for _, record := range all {
		byID[record.ProfileID] = record
	}
	result := ModelProfileBulkDeleteResult{DeletedIDs: []string{}, MissingIDs: []string{}}
	seen, deleting := map[string]struct{}{}, map[string]struct{}{}
	for _, rawID := range profileIDs {
		profileID := strings.TrimSpace(rawID)
		if profileID == "" {
			continue
		}
		if _, duplicate := seen[profileID]; duplicate {
			continue
		}
		seen[profileID] = struct{}{}
		if _, ok := byID[profileID]; !ok {
			result.MissingIDs = append(result.MissingIDs, profileID)
			continue
		}
		deleting[profileID] = struct{}{}
		result.DeletedIDs = append(result.DeletedIDs, profileID)
	}
	remaining := make([]ModelProfileRecord, 0, len(all)-len(deleting))
	for _, record := range all {
		if _, drop := deleting[record.ProfileID]; !drop {
			remaining = append(remaining, record)
		}
	}
	defaultID, _, err := s.defaultIDUnlocked(accountScopeID)
	if err != nil {
		return ModelProfileBulkDeleteResult{}, err
	}
	if !containsModelProfile(remaining, defaultID) {
		defaultID = ""
		if len(remaining) > 0 {
			defaultID = remaining[0].ProfileID
		}
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for profileID := range deleting {
		record := byID[profileID]
		if err := batch.Delete([]byte(KeyModelProfileForAccount(accountScopeID, profileID)), nil); err != nil {
			return ModelProfileBulkDeleteResult{}, err
		}
		if err := batch.Delete([]byte(KeyModelProfileNameForAccount(accountScopeID, NormalizeModelProfileName(record.Name))), nil); err != nil {
			return ModelProfileBulkDeleteResult{}, err
		}
	}
	if defaultID == "" {
		if err := batch.Delete([]byte(KeyModelProfileDefaultForAccount(accountScopeID)), nil); err != nil {
			return ModelProfileBulkDeleteResult{}, err
		}
	} else if err := batch.Set([]byte(KeyModelProfileDefaultForAccount(accountScopeID)), []byte(defaultID), nil); err != nil {
		return ModelProfileBulkDeleteResult{}, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return ModelProfileBulkDeleteResult{}, err
	}
	return result, nil
}

func (s *ModelProfileStore) defaultIDUnlocked(accountScopeID string) (string, bool, error) {
	value, ok, err := s.store.GetBytes(KeyModelProfileDefaultForAccount(accountScopeID))
	return strings.TrimSpace(string(value)), ok, err
}

func containsModelProfile(profiles []ModelProfileRecord, profileID string) bool {
	_, ok := findModelProfile(profiles, profileID)
	return ok
}
func findModelProfile(profiles []ModelProfileRecord, profileID string) (ModelProfileRecord, bool) {
	for _, profile := range profiles {
		if profile.ProfileID == profileID {
			return profile, true
		}
	}
	return ModelProfileRecord{}, false
}
