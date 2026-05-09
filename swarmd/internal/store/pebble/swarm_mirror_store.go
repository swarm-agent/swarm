package pebblestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	SwarmMirrorEventTypeUpsert   = "upsert"
	SwarmMirrorEventTypeDelete   = "delete"
	SwarmMirrorEventTypeBookmark = "bookmark"
)

type SwarmMirrorResourceRecord struct {
	ManagedSwarmID string          `json:"managed_swarm_id,omitempty"`
	Kind           string          `json:"kind"`
	ID             string          `json:"id"`
	Sequence       uint64          `json:"sequence"`
	Deleted        bool            `json:"deleted,omitempty"`
	Resource       json.RawMessage `json:"resource,omitempty"`
	UpdatedAt      int64           `json:"updated_at"`
}

type SwarmMirrorEventRecord struct {
	ManagedSwarmID string          `json:"managed_swarm_id,omitempty"`
	Sequence       uint64          `json:"sequence"`
	EventType      string          `json:"event_type"`
	Kind           string          `json:"kind,omitempty"`
	ID             string          `json:"id,omitempty"`
	Deleted        bool            `json:"deleted,omitempty"`
	Resource       json.RawMessage `json:"resource,omitempty"`
	TsUnixMs       int64           `json:"ts_unix_ms"`
}

type SwarmMirrorCursorRecord struct {
	ManagedSwarmID string `json:"managed_swarm_id"`
	LastSequence   uint64 `json:"last_sequence"`
	UpdatedAt      int64  `json:"updated_at"`
}

type SwarmMirrorStore struct {
	store *Store
}

func NewSwarmMirrorStore(store *Store) *SwarmMirrorStore {
	return &SwarmMirrorStore{store: store}
}

func (s *SwarmMirrorStore) CurrentLocalSequence() (uint64, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("swarm mirror store is not configured")
	}
	raw, ok, err := s.store.GetBytes(KeySwarmMirrorLocalSeq)
	if err != nil || !ok {
		return 0, err
	}
	return bytesToUint64(raw)
}

func (s *SwarmMirrorStore) UpsertLocalResource(kind, id string, resource any) (SwarmMirrorResourceRecord, *SwarmMirrorEventRecord, error) {
	if s == nil || s.store == nil {
		return SwarmMirrorResourceRecord{}, nil, errors.New("swarm mirror store is not configured")
	}
	kind = normalizeMirrorPart(kind)
	id = strings.TrimSpace(id)
	if kind == "" || id == "" {
		return SwarmMirrorResourceRecord{}, nil, errors.New("mirror resource kind and id are required")
	}
	raw, err := json.Marshal(resource)
	if err != nil {
		return SwarmMirrorResourceRecord{}, nil, fmt.Errorf("marshal mirror resource: %w", err)
	}
	key := KeySwarmMirrorLocalResource(kind, id)
	var existing SwarmMirrorResourceRecord
	ok, err := s.store.GetJSON(key, &existing)
	if err != nil {
		return SwarmMirrorResourceRecord{}, nil, err
	}
	if ok && !existing.Deleted && bytes.Equal(bytes.TrimSpace(existing.Resource), bytes.TrimSpace(raw)) {
		return existing, nil, nil
	}
	seq, err := s.nextLocalSequence()
	if err != nil {
		return SwarmMirrorResourceRecord{}, nil, err
	}
	now := time.Now().UnixMilli()
	record := SwarmMirrorResourceRecord{Kind: kind, ID: id, Sequence: seq, Resource: append([]byte(nil), raw...), UpdatedAt: now}
	event := SwarmMirrorEventRecord{Sequence: seq, EventType: SwarmMirrorEventTypeUpsert, Kind: kind, ID: id, Resource: append([]byte(nil), raw...), TsUnixMs: now}
	if err := s.store.PutJSON(key, record); err != nil {
		return SwarmMirrorResourceRecord{}, nil, err
	}
	if err := s.store.PutJSON(KeySwarmMirrorLocalEvent(seq), event); err != nil {
		return SwarmMirrorResourceRecord{}, nil, err
	}
	return record, &event, nil
}

func (s *SwarmMirrorStore) ListLocalResources(kinds []string, limit int) ([]SwarmMirrorResourceRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm mirror store is not configured")
	}
	if limit <= 0 {
		limit = 100000
	}
	allowed := normalizeMirrorKindSet(kinds)
	out := make([]SwarmMirrorResourceRecord, 0)
	err := s.store.IteratePrefix(SwarmMirrorLocalResourcePrefix(), limit, func(_ string, value []byte) error {
		var record SwarmMirrorResourceRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode mirror resource: %w", err)
		}
		record.Kind = normalizeMirrorPart(record.Kind)
		record.ID = strings.TrimSpace(record.ID)
		if record.Kind == "" || record.ID == "" || record.Deleted {
			return nil
		}
		if len(allowed) > 0 {
			if _, ok := allowed[record.Kind]; !ok {
				return nil
			}
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

func (s *SwarmMirrorStore) ListLocalEventsSince(since uint64, limit int) ([]SwarmMirrorEventRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm mirror store is not configured")
	}
	if limit <= 0 {
		limit = 1000
	}
	current, err := s.CurrentLocalSequence()
	if err != nil {
		return nil, err
	}
	out := make([]SwarmMirrorEventRecord, 0, min(limit, 64))
	for seq := since + 1; seq <= current && len(out) < limit; seq++ {
		var record SwarmMirrorEventRecord
		ok, err := s.store.GetJSON(KeySwarmMirrorLocalEvent(seq), &record)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, normalizeMirrorEventRecord(record))
	}
	return out, nil
}

func (s *SwarmMirrorStore) UpsertRemoteResource(managedSwarmID string, event SwarmMirrorEventRecord) (SwarmMirrorResourceRecord, error) {
	if s == nil || s.store == nil {
		return SwarmMirrorResourceRecord{}, errors.New("swarm mirror store is not configured")
	}
	managedSwarmID = strings.TrimSpace(managedSwarmID)
	if managedSwarmID == "" {
		return SwarmMirrorResourceRecord{}, errors.New("managed swarm id is required")
	}
	event = normalizeMirrorEventRecord(event)
	if event.Kind == "" || event.ID == "" || event.Sequence == 0 {
		return SwarmMirrorResourceRecord{}, errors.New("mirror event kind, id, and sequence are required")
	}
	record := SwarmMirrorResourceRecord{ManagedSwarmID: managedSwarmID, Kind: event.Kind, ID: event.ID, Sequence: event.Sequence, Deleted: event.Deleted || event.EventType == SwarmMirrorEventTypeDelete, Resource: append([]byte(nil), event.Resource...), UpdatedAt: time.Now().UnixMilli()}
	if err := s.store.PutJSON(KeySwarmMirrorRemoteResource(managedSwarmID, event.Kind, event.ID), record); err != nil {
		return SwarmMirrorResourceRecord{}, err
	}
	return record, nil
}

func (s *SwarmMirrorStore) ListRemoteResources(managedSwarmID string, kinds []string, limit int) ([]SwarmMirrorResourceRecord, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("swarm mirror store is not configured")
	}
	if limit <= 0 {
		limit = 100000
	}
	managedSwarmID = strings.TrimSpace(managedSwarmID)
	prefix := SwarmMirrorRemoteResourcePrefix()
	if managedSwarmID != "" {
		prefix = KeySwarmMirrorRemoteResourcePrefixForSwarm(managedSwarmID)
	}
	allowed := normalizeMirrorKindSet(kinds)
	out := make([]SwarmMirrorResourceRecord, 0)
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var record SwarmMirrorResourceRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode remote mirror resource: %w", err)
		}
		record.Kind = normalizeMirrorPart(record.Kind)
		record.ID = strings.TrimSpace(record.ID)
		record.ManagedSwarmID = strings.TrimSpace(record.ManagedSwarmID)
		if record.Kind == "" || record.ID == "" || record.ManagedSwarmID == "" || record.Deleted {
			return nil
		}
		if len(allowed) > 0 {
			if _, ok := allowed[record.Kind]; !ok {
				return nil
			}
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ManagedSwarmID == out[j].ManagedSwarmID {
			if out[i].Kind == out[j].Kind {
				return strings.ToLower(out[i].ID) < strings.ToLower(out[j].ID)
			}
			return out[i].Kind < out[j].Kind
		}
		return strings.ToLower(out[i].ManagedSwarmID) < strings.ToLower(out[j].ManagedSwarmID)
	})
	return out, nil
}

func (s *SwarmMirrorStore) SetRemoteCursor(managedSwarmID string, sequence uint64) (SwarmMirrorCursorRecord, error) {
	if s == nil || s.store == nil {
		return SwarmMirrorCursorRecord{}, errors.New("swarm mirror store is not configured")
	}
	managedSwarmID = strings.TrimSpace(managedSwarmID)
	if managedSwarmID == "" {
		return SwarmMirrorCursorRecord{}, errors.New("managed swarm id is required")
	}
	record := SwarmMirrorCursorRecord{ManagedSwarmID: managedSwarmID, LastSequence: sequence, UpdatedAt: time.Now().UnixMilli()}
	if err := s.store.PutJSON(KeySwarmMirrorRemoteCursor(managedSwarmID), record); err != nil {
		return SwarmMirrorCursorRecord{}, err
	}
	return record, nil
}

func (s *SwarmMirrorStore) GetRemoteCursor(managedSwarmID string) (SwarmMirrorCursorRecord, bool, error) {
	if s == nil || s.store == nil {
		return SwarmMirrorCursorRecord{}, false, errors.New("swarm mirror store is not configured")
	}
	managedSwarmID = strings.TrimSpace(managedSwarmID)
	if managedSwarmID == "" {
		return SwarmMirrorCursorRecord{}, false, errors.New("managed swarm id is required")
	}
	var record SwarmMirrorCursorRecord
	ok, err := s.store.GetJSON(KeySwarmMirrorRemoteCursor(managedSwarmID), &record)
	if err != nil || !ok {
		return SwarmMirrorCursorRecord{}, ok, err
	}
	record.ManagedSwarmID = strings.TrimSpace(record.ManagedSwarmID)
	return record, true, nil
}

func (s *SwarmMirrorStore) nextLocalSequence() (uint64, error) {
	current, err := s.CurrentLocalSequence()
	if err != nil {
		return 0, err
	}
	next := current + 1
	if err := s.store.PutBytes(KeySwarmMirrorLocalSeq, uint64ToBytes(next)); err != nil {
		return 0, err
	}
	return next, nil
}

func normalizeMirrorKindSet(kinds []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, kind := range kinds {
		kind = normalizeMirrorPart(kind)
		if kind != "" {
			out[kind] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeMirrorEventRecord(record SwarmMirrorEventRecord) SwarmMirrorEventRecord {
	record.ManagedSwarmID = strings.TrimSpace(record.ManagedSwarmID)
	record.Kind = normalizeMirrorPart(record.Kind)
	record.ID = strings.TrimSpace(record.ID)
	record.EventType = normalizeMirrorEventType(record.EventType)
	if record.TsUnixMs < 0 {
		record.TsUnixMs = 0
	}
	return record
}

func normalizeMirrorEventType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SwarmMirrorEventTypeDelete:
		return SwarmMirrorEventTypeDelete
	case SwarmMirrorEventTypeBookmark:
		return SwarmMirrorEventTypeBookmark
	default:
		return SwarmMirrorEventTypeUpsert
	}
}

func normalizeMirrorPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}
