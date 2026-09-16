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

// ConnectionStore manages persistence of Connection records in Pebble.
// Strictly scoped to AccountScopeID and WorkspaceID.
type ConnectionStore struct {
	store *Store
	mu    sync.Mutex
}

// NewConnectionStore creates a new ConnectionStore backed by the given Pebble Store.
func NewConnectionStore(store *Store) *ConnectionStore {
	return &ConnectionStore{store: store}
}

// Get retrieves a Connection by its account scope, workspace id, and connection id.
func (s *ConnectionStore) Get(accountScopeID, workspaceID, connectionID string) (environments.Connection, bool, error) {
	if s == nil || s.store == nil {
		return environments.Connection{}, false, errors.New("connection store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	connectionID = strings.TrimSpace(connectionID)
	if accountScopeID == "" || workspaceID == "" || connectionID == "" {
		return environments.Connection{}, false, errors.New("account scope id, workspace id, and connection id are required")
	}

	key := KeyConnectionForAccount(accountScopeID, workspaceID, connectionID)
	var conn environments.Connection
	ok, err := s.store.GetJSON(key, &conn)
	if err != nil || !ok {
		return environments.Connection{}, ok, err
	}

	// Enforce strict isolation
	if conn.AccountScopeID != accountScopeID || conn.WorkspaceID != workspaceID {
		return environments.Connection{}, false, nil
	}

	return conn, true, nil
}

// List returns connections for an account scope and workspace, up to limit.
func (s *ConnectionStore) List(accountScopeID, workspaceID string, limit int) ([]environments.Connection, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("connection store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	if accountScopeID == "" || workspaceID == "" {
		return nil, errors.New("account scope id and workspace id are required")
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}

	connections := make([]environments.Connection, 0)
	prefix := ConnectionPrefixForAccount(accountScopeID, workspaceID)

	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var conn environments.Connection
		if err := json.Unmarshal(value, &conn); err != nil {
			return fmt.Errorf("unmarshal connection: %w", err)
		}
		if conn.AccountScopeID == accountScopeID && conn.WorkspaceID == workspaceID {
			connections = append(connections, conn)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(connections, func(i, j int) bool {
		if connections[i].Name == connections[j].Name {
			return connections[i].ID < connections[j].ID
		}
		return connections[i].Name < connections[j].Name
	})

	return connections, nil
}

// Save persists a Connection, ensuring domain validation, secret verification, and proper timestamps.
func (s *ConnectionStore) Save(conn environments.Connection) (environments.Connection, error) {
	if s == nil || s.store == nil {
		return environments.Connection{}, errors.New("connection store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := conn.Validate(); err != nil {
		return environments.Connection{}, fmt.Errorf("validate connection: %w", err)
	}

	// Serialize and verify that no credentials or secrets exist in the payload
	raw, err := json.Marshal(conn)
	if err != nil {
		return environments.Connection{}, fmt.Errorf("marshal connection: %w", err)
	}
	if err := environments.AssertNoSecretsRaw(raw); err != nil {
		return environments.Connection{}, fmt.Errorf("security check failed: %w", err)
	}

	now := time.Now().UnixMilli()
	current, found, err := s.Get(conn.AccountScopeID, conn.WorkspaceID, conn.ID)
	if err != nil {
		return environments.Connection{}, err
	}
	if found {
		conn.CreatedAt = current.CreatedAt
	} else if conn.CreatedAt <= 0 {
		conn.CreatedAt = now
	}
	conn.UpdatedAt = now

	key := KeyConnectionForAccount(conn.AccountScopeID, conn.WorkspaceID, conn.ID)
	if err := s.store.PutJSON(key, conn); err != nil {
		return environments.Connection{}, fmt.Errorf("put connection: %w", err)
	}

	return conn, nil
}

// Delete removes a Connection by account scope, workspace id, and connection id.
func (s *ConnectionStore) Delete(accountScopeID, workspaceID, connectionID string) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("connection store is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	accountScopeID = strings.TrimSpace(accountScopeID)
	workspaceID = strings.TrimSpace(workspaceID)
	connectionID = strings.TrimSpace(connectionID)
	if accountScopeID == "" || workspaceID == "" || connectionID == "" {
		return false, errors.New("account scope id, workspace id, and connection id are required")
	}

	_, found, err := s.Get(accountScopeID, workspaceID, connectionID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	key := KeyConnectionForAccount(accountScopeID, workspaceID, connectionID)
	if err := s.store.Delete(key); err != nil {
		return false, fmt.Errorf("delete connection: %w", err)
	}

	return true, nil
}
