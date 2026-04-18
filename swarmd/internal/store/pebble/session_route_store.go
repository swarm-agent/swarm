package pebblestore

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type SessionRouteRecord struct {
	SessionID            string `json:"session_id"`
	ChildSwarmID         string `json:"child_swarm_id"`
	ChildBackendURL      string `json:"child_backend_url,omitempty"`
	HostWorkspacePath    string `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath string `json:"runtime_workspace_path,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

type SessionRouteStore struct {
	store *Store
}

func NewSessionRouteStore(store *Store) *SessionRouteStore {
	return &SessionRouteStore{store: store}
}

func (s *SessionRouteStore) Put(record SessionRouteRecord) (SessionRouteRecord, error) {
	record = normalizeSessionRouteRecord(record)
	if err := s.store.PutJSON(KeySessionRoute(record.SessionID), record); err != nil {
		return SessionRouteRecord{}, err
	}
	return record, nil
}

func (s *SessionRouteStore) Get(sessionID string) (SessionRouteRecord, bool, error) {
	var record SessionRouteRecord
	ok, err := s.store.GetJSON(KeySessionRoute(sessionID), &record)
	if err != nil {
		return SessionRouteRecord{}, false, err
	}
	if !ok {
		return SessionRouteRecord{}, false, nil
	}
	return normalizeSessionRouteRecord(record), true, nil
}

func (s *SessionRouteStore) Delete(sessionID string) error {
	if s == nil || s.store == nil {
		return errors.New("session route store is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	return s.store.Delete(KeySessionRoute(sessionID))
}

func (s *SessionRouteStore) List(limit int) ([]SessionRouteRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]SessionRouteRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(SessionRoutePrefix(), limit, func(key string, value []byte) error {
		var record SessionRouteRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return err
		}
		record = normalizeSessionRouteRecord(record)
		if record.SessionID == "" {
			record.SessionID = strings.TrimSpace(strings.TrimPrefix(key, SessionRoutePrefix()))
		}
		if record.SessionID == "" {
			return nil
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func normalizeSessionRouteRecord(record SessionRouteRecord) SessionRouteRecord {
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.ChildSwarmID = strings.TrimSpace(record.ChildSwarmID)
	record.ChildBackendURL = strings.TrimSpace(record.ChildBackendURL)
	record.HostWorkspacePath = strings.TrimSpace(record.HostWorkspacePath)
	record.RuntimeWorkspacePath = strings.TrimSpace(record.RuntimeWorkspacePath)
	return record
}
