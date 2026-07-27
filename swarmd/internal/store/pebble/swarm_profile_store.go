package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
)

var ErrSwarmProfileNameConflict = errors.New("swarm profile name already exists")

type SwarmProfileMember struct {
	AgentID   string                 `json:"agent_id"`
	ModelMode string                 `json:"model_mode"`
	Single    *ModelProfileSelection `json:"single,omitempty"`
	Plan      *ModelProfileSelection `json:"plan,omitempty"`
	Auto      *ModelProfileSelection `json:"auto,omitempty"`
}

type SwarmProfileRecord struct {
	ProfileID      string               `json:"profile_id"`
	AccountScopeID string               `json:"account_scope_id"`
	Name           string               `json:"name"`
	Members        []SwarmProfileMember `json:"members"`
	CreatedAt      int64                `json:"created_at"`
	UpdatedAt      int64                `json:"updated_at"`
}

type SwarmProfileStore struct{ store *Store }

func NewSwarmProfileStore(store *Store) *SwarmProfileStore { return &SwarmProfileStore{store: store} }
func NormalizeSwarmProfileName(name string) string         { return strings.ToLower(strings.TrimSpace(name)) }

func (s *SwarmProfileStore) GetForAccount(accountScopeID, profileID string) (SwarmProfileRecord, bool, error) {
	accountScopeID, profileID = strings.TrimSpace(accountScopeID), strings.TrimSpace(profileID)
	if s == nil || s.store == nil || accountScopeID == "" || profileID == "" {
		return SwarmProfileRecord{}, false, nil
	}
	var record SwarmProfileRecord
	ok, err := s.store.GetJSON(KeySwarmProfileForAccount(accountScopeID, profileID), &record)
	if err != nil || !ok || record.AccountScopeID != accountScopeID || record.ProfileID != profileID {
		return SwarmProfileRecord{}, false, err
	}
	return record, true, nil
}

func (s *SwarmProfileStore) ListForAccount(accountScopeID string, limit int) ([]SwarmProfileRecord, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if s == nil || s.store == nil || accountScopeID == "" {
		return nil, errors.New("swarm profile store and account scope id are required")
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]SwarmProfileRecord, 0)
	err := s.store.IteratePrefix(SwarmProfilePrefixForAccount(accountScopeID), limit, func(_ string, value []byte) error {
		var record SwarmProfileRecord
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
		return NormalizeSwarmProfileName(out[i].Name) < NormalizeSwarmProfileName(out[j].Name)
	})
	return out, nil
}

func (s *SwarmProfileStore) PutForAccount(record SwarmProfileRecord) (SwarmProfileRecord, error) {
	if s == nil || s.store == nil {
		return SwarmProfileRecord{}, errors.New("swarm profile store is not configured")
	}
	record.AccountScopeID, record.ProfileID, record.Name = strings.TrimSpace(record.AccountScopeID), strings.TrimSpace(record.ProfileID), strings.TrimSpace(record.Name)
	if record.AccountScopeID == "" || record.ProfileID == "" || NormalizeSwarmProfileName(record.Name) == "" {
		return SwarmProfileRecord{}, errors.New("account scope id, profile id, and name are required")
	}
	s.store.swarmProfilesMu.Lock()
	defer s.store.swarmProfilesMu.Unlock()
	existing, exists, err := s.GetForAccount(record.AccountScopeID, record.ProfileID)
	if err != nil {
		return SwarmProfileRecord{}, err
	}
	nameKey := KeySwarmProfileNameForAccount(record.AccountScopeID, NormalizeSwarmProfileName(record.Name))
	indexedID, indexed, err := s.store.GetBytes(nameKey)
	if err != nil {
		return SwarmProfileRecord{}, err
	}
	if indexed && string(indexedID) != record.ProfileID {
		return SwarmProfileRecord{}, ErrSwarmProfileNameConflict
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return SwarmProfileRecord{}, fmt.Errorf("marshal swarm profile: %w", err)
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(KeySwarmProfileForAccount(record.AccountScopeID, record.ProfileID)), payload, nil); err != nil {
		return SwarmProfileRecord{}, err
	}
	if err := batch.Set([]byte(nameKey), []byte(record.ProfileID), nil); err != nil {
		return SwarmProfileRecord{}, err
	}
	if exists && NormalizeSwarmProfileName(existing.Name) != NormalizeSwarmProfileName(record.Name) {
		if err := batch.Delete([]byte(KeySwarmProfileNameForAccount(record.AccountScopeID, NormalizeSwarmProfileName(existing.Name))), nil); err != nil {
			return SwarmProfileRecord{}, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return SwarmProfileRecord{}, err
	}
	return record, nil
}

func (s *SwarmProfileStore) DeleteForAccount(accountScopeID, profileID string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("swarm profile store is not configured")
	}
	s.store.swarmProfilesMu.Lock()
	defer s.store.swarmProfilesMu.Unlock()
	record, ok, err := s.GetForAccount(accountScopeID, profileID)
	if err != nil || !ok {
		return false, err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeySwarmProfileForAccount(accountScopeID, profileID)), nil); err != nil {
		return false, err
	}
	if err := batch.Delete([]byte(KeySwarmProfileNameForAccount(accountScopeID, NormalizeSwarmProfileName(record.Name))), nil); err != nil {
		return false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return false, err
	}
	return true, nil
}
