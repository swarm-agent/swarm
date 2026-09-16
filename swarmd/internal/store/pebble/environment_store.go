package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// EnvironmentStore manages persistence of reusable Environment definitions in Pebble.
// Invariant: strictly contains NO ephemeral runtime state (container IDs, runtime IPs,
// provider resource IDs, assigned ports, live status).
// Strictly scoped to AccountScopeID and WorkspaceID.
type EnvironmentStore struct {
	store *Store
	mu    sync.Mutex
}

// NewEnvironmentStore creates a new EnvironmentStore backed by the given Pebble Store.
func NewEnvironmentStore(store *Store) *EnvironmentStore {
	return &EnvironmentStore{store: store}
}

// Get retrieves an Environment by its account scope, workspace id, and environment id.
func (s *EnvironmentStore) Get(accountScopeID, workspaceID, environmentID string) (environments.Environment, bool, error) {
	if s == nil || s.store == nil {
		return environments.Environment{}, false, errors.New("environment store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	environmentID = strings.TrimSpace(environmentID)
	if accountScopeID == "" || workspaceID == "" || environmentID == "" {
		return environments.Environment{}, false, errors.New("account scope id, workspace id, and environment id are required")
	}

	key := KeyEnvironmentForAccount(accountScopeID, workspaceID, environmentID)
	var env environments.Environment
	ok, err := s.store.GetJSON(key, &env)
	if err != nil || !ok {
		return environments.Environment{}, ok, err
	}

	// Enforce strict isolation
	if env.AccountScopeID != accountScopeID || env.WorkspaceID != workspaceID {
		return environments.Environment{}, false, nil
	}

	return env, true, nil
}

// List returns environments for an account scope and workspace, up to limit.
func (s *EnvironmentStore) List(accountScopeID, workspaceID string, limit int) ([]environments.Environment, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("environment store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return nil, errors.New("account scope id and workspace id are required")
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}

	envs := make([]environments.Environment, 0)
	prefix := EnvironmentPrefixForAccount(accountScopeID, workspaceID)

	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var env environments.Environment
		if err := json.Unmarshal(value, &env); err != nil {
			return fmt.Errorf("unmarshal environment: %w", err)
		}
		if env.AccountScopeID == accountScopeID && env.WorkspaceID == workspaceID {
			envs = append(envs, env)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(envs, func(i, j int) bool {
		if envs[i].Name == envs[j].Name {
			return envs[i].ID < envs[j].ID
		}
		return envs[i].Name < envs[j].Name
	})

	return envs, nil
}

// Save persists an Environment definition, ensuring domain validation, secret verification, and proper timestamps.
func (s *EnvironmentStore) Save(env environments.Environment) (environments.Environment, error) {
	if s == nil || s.store == nil {
		return environments.Environment{}, errors.New("environment store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := env.Validate(); err != nil {
		return environments.Environment{}, fmt.Errorf("validate environment: %w", err)
	}

	// Serialize and verify that no credentials or secrets exist in the payload
	raw, err := json.Marshal(env)
	if err != nil {
		return environments.Environment{}, fmt.Errorf("marshal environment: %w", err)
	}
	if err := environments.AssertNoSecretsRaw(raw); err != nil {
		return environments.Environment{}, fmt.Errorf("security check failed: %w", err)
	}

	now := time.Now().UnixMilli()
	current, found, err := s.Get(env.AccountScopeID, env.WorkspaceID, env.ID)
	if err != nil {
		return environments.Environment{}, err
	}
	if found {
		env.CreatedAt = current.CreatedAt
	} else if env.CreatedAt <= 0 {
		env.CreatedAt = now
	}
	env.UpdatedAt = now

	key := KeyEnvironmentForAccount(env.AccountScopeID, env.WorkspaceID, env.ID)
	if err := s.store.PutJSON(key, env); err != nil {
		return environments.Environment{}, fmt.Errorf("put environment: %w", err)
	}

	return env, nil
}

// Delete removes an Environment definition by account scope, workspace id, and environment id.
func (s *EnvironmentStore) Delete(accountScopeID, workspaceID, environmentID string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("environment store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	environmentID = strings.TrimSpace(environmentID)
	if accountScopeID == "" || workspaceID == "" || environmentID == "" {
		return false, errors.New("account scope id, workspace id, and environment id are required")
	}

	_, found, err := s.Get(accountScopeID, workspaceID, environmentID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	key := KeyEnvironmentForAccount(accountScopeID, workspaceID, environmentID)
	if err := s.store.Delete(key); err != nil {
		return false, fmt.Errorf("delete environment: %w", err)
	}

	return true, nil
}
