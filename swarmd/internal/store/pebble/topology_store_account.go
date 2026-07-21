package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// SnapshotForAccount reads only account-scoped topology keys. It intentionally
// does not fall back to legacy/global topology prefixes; those prefixes are
// migration inputs only after Checkpoint 1.2.
func (s *TopologyStore) SnapshotForAccount(accountScopeID string) (TopologySnapshot, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologySnapshot{}, err
	}
	runtimes, err := s.ListRuntimesForAccount(accountScopeID, 100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	runtimePlacements, err := s.ListRuntimePlacementsForAccount(accountScopeID, 100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	workspaceBindings, err := s.ListWorkspaceBindingsForAccount(accountScopeID, 100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	return TopologySnapshot{
		Runtimes:          runtimes,
		RuntimePlacements: runtimePlacements,
		WorkspaceBindings: workspaceBindings,
		MigrationStatus: TopologyMigrationStatusRecord{
			ID:                    DefaultTopologyMigrationStatusID,
			Version:               TopologySnapshotVersion,
			RebuiltAt:             time.Now().UnixMilli(),
			RuntimeCount:          len(runtimes),
			WorkspaceBindingCount: len(workspaceBindings),
		},
	}, nil
}

// ReplaceSnapshotForAccount replaces only one account's topology records. It
// never deletes or reads legacy/global topology keys and never touches another
// account's keyspace.
func (s *TopologyStore) ReplaceSnapshotForAccount(accountScopeID string, snapshot TopologySnapshot) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	snapshot.Runtimes = normalizeTopologyRuntimeRecords(snapshot.Runtimes)
	snapshot.RuntimePlacements = normalizeTopologyRuntimePlacementRecords(snapshot.RuntimePlacements)
	snapshot.WorkspaceBindings = normalizeTopologyWorkspaceBindingRecords(snapshot.WorkspaceBindings)
	for i := range snapshot.Runtimes {
		if snapshot.Runtimes[i], err = enforceTopologyRuntimeAccount(accountScopeID, snapshot.Runtimes[i]); err != nil {
			return err
		}
	}
	for i := range snapshot.RuntimePlacements {
		if snapshot.RuntimePlacements[i], err = enforceTopologyRuntimePlacementAccount(accountScopeID, snapshot.RuntimePlacements[i]); err != nil {
			return err
		}
		if snapshot.RuntimePlacements[i].PlacementID == "" {
			snapshot.RuntimePlacements[i].PlacementID = legacyTopologyRuntimePlacementID(snapshot.RuntimePlacements[i].AccountScopeID, snapshot.RuntimePlacements[i].RuntimeSwarmID)
		}
		if err := validateTopologyRuntimePlacement(snapshot.RuntimePlacements[i]); err != nil {
			return err
		}
	}
	for i := range snapshot.WorkspaceBindings {
		if snapshot.WorkspaceBindings[i], err = enforceTopologyWorkspaceBindingAccount(accountScopeID, snapshot.WorkspaceBindings[i]); err != nil {
			return err
		}
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, prefix := range []string{
		TopologyRuntimePrefixForAccount(accountScopeID),
		TopologyRuntimePlacementPrefixForAccount(accountScopeID),
		TopologyWorkspaceBindingPrefixForAccount(accountScopeID),
	} {
		if err := s.deletePrefixWithBatch(batch, prefix); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Runtimes {
		if err := setTopologyBatchJSON(batch, KeyTopologyRuntimeForAccount(accountScopeID, record.SwarmID), record); err != nil {
			return fmt.Errorf("marshal topology runtime %q: %w", record.SwarmID, err)
		}
	}
	for _, record := range snapshot.RuntimePlacements {
		if err := setTopologyBatchJSON(batch, KeyTopologyRuntimePlacementForAccount(accountScopeID, record.RuntimeSwarmID), record); err != nil {
			return fmt.Errorf("marshal topology runtime placement %q: %w", record.RuntimeSwarmID, err)
		}
	}
	for _, record := range snapshot.WorkspaceBindings {
		if err := setTopologyBatchJSON(batch, KeyTopologyWorkspaceBindingForAccount(accountScopeID, record.BindingID), record); err != nil {
			return fmt.Errorf("marshal topology workspace binding %q: %w", record.BindingID, err)
		}
	}
	return batch.Commit(nil)
}

func (s *TopologyStore) ListRuntimesForAccount(accountScopeID string, limit int) ([]TopologyRuntimeRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	return s.listTopologyRuntimeRecordsForAccount(accountScopeID, limit)
}

func (s *TopologyStore) GetRuntimeForAccount(accountScopeID, swarmID string) (TopologyRuntimeRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyRuntimeRecord{}, false, err
	}
	if s == nil || s.store == nil {
		return TopologyRuntimeRecord{}, false, errors.New("topology store is not configured")
	}
	swarmID = normalizeTopologyKeyValue(swarmID)
	if swarmID == "" {
		return TopologyRuntimeRecord{}, false, errors.New("topology runtime swarm id is required")
	}
	var record TopologyRuntimeRecord
	ok, err := s.store.GetJSON(KeyTopologyRuntimeForAccount(accountScopeID, swarmID), &record)
	if err != nil || !ok {
		return TopologyRuntimeRecord{}, ok, err
	}
	record = normalizeTopologyRuntimeRecord(record)
	if record.SwarmID == "" {
		record.SwarmID = swarmID
	}
	record, err = enforceTopologyRuntimeAccount(accountScopeID, record)
	if err != nil {
		return TopologyRuntimeRecord{}, false, err
	}
	return record, true, nil
}

func (s *TopologyStore) PutRuntimeForAccount(accountScopeID string, record TopologyRuntimeRecord) (TopologyRuntimeRecord, error) {
	if s == nil || s.store == nil {
		return TopologyRuntimeRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyRuntimeRecord{}, err
	}
	record = normalizeTopologyRuntimeRecord(record)
	if record.SwarmID == "" {
		return TopologyRuntimeRecord{}, errors.New("topology runtime swarm id is required")
	}
	if record.Name == "" {
		record.Name = record.SwarmID
	}
	if record, err = enforceTopologyRuntimeAccount(accountScopeID, record); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	record.CreatedAt, record.UpdatedAt = nextTopologyWriteTimestamps(record.CreatedAt)
	if err := s.store.PutJSON(KeyTopologyRuntimeForAccount(accountScopeID, record.SwarmID), record); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteRuntimeForAccount(accountScopeID, swarmID string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	swarmID = normalizeTopologyKeyValue(swarmID)
	if swarmID == "" {
		return errors.New("topology runtime swarm id is required")
	}
	if err := s.store.Delete(KeyTopologyRuntimeForAccount(accountScopeID, swarmID)); err != nil {
		return err
	}
	return s.DeleteRuntimePlacementForAccount(accountScopeID, swarmID)
}

func (s *TopologyStore) ListWorkspaceBindingsForAccount(accountScopeID string, limit int) ([]TopologyWorkspaceBindingRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	return s.listTopologyWorkspaceBindingRecordsForAccount(accountScopeID, limit)
}

func (s *TopologyStore) ListWorkspaceBindingsBySourcePathForAccount(accountScopeID, sourceWorkspacePath string, limit int) ([]TopologyWorkspaceBindingRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return nil, err
	}
	sourceWorkspacePath = strings.TrimSpace(sourceWorkspacePath)
	if sourceWorkspacePath == "" {
		return nil, errors.New("topology source workspace path is required")
	}
	records, err := s.listTopologyWorkspaceBindingRecordsForAccount(accountScopeID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]TopologyWorkspaceBindingRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.SourceWorkspacePath), sourceWorkspacePath) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *TopologyStore) GetWorkspaceBindingForAccount(accountScopeID, bindingID string) (TopologyWorkspaceBindingRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, false, err
	}
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, false, errors.New("topology store is not configured")
	}
	bindingID = normalizeTopologyKeyValue(bindingID)
	if bindingID == "" {
		return TopologyWorkspaceBindingRecord{}, false, errors.New("topology workspace binding id is required")
	}
	var record TopologyWorkspaceBindingRecord
	ok, err := s.store.GetJSON(KeyTopologyWorkspaceBindingForAccount(accountScopeID, bindingID), &record)
	if err != nil || !ok {
		return TopologyWorkspaceBindingRecord{}, ok, err
	}
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record.BindingID == "" {
		record.BindingID = bindingID
	}
	record, err = enforceTopologyWorkspaceBindingAccount(accountScopeID, record)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, false, err
	}
	return record, true, nil
}

func (s *TopologyStore) PutWorkspaceBindingForAccount(accountScopeID string, record TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology store is not configured")
	}
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record, err = enforceTopologyWorkspaceBindingAccount(accountScopeID, record); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	return s.putStrictWorkspaceBindingForAccount(accountScopeID, record)
}

func (s *TopologyStore) DeleteWorkspaceBindingForAccount(accountScopeID, bindingID string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	bindingID = normalizeTopologyKeyValue(bindingID)
	if bindingID == "" {
		return errors.New("topology workspace binding id is required")
	}
	record, ok, err := s.GetWorkspaceBindingForAccount(accountScopeID, bindingID)
	if err != nil {
		return err
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete([]byte(KeyTopologyWorkspaceBindingForAccount(accountScopeID, bindingID)), nil); err != nil {
		return err
	}
	if ok && topologyWorkspaceBindingIsBound(record) {
		if err := batch.Delete([]byte(topologyWorkspaceBindingActiveIndexKey(accountScopeID, record)), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

func (s *TopologyStore) listTopologyRuntimeRecordsForAccount(accountScopeID string, limit int) ([]TopologyRuntimeRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologyRuntimePrefixForAccount(accountScopeID), func(key string, value []byte) (TopologyRuntimeRecord, bool, error) {
		var record TopologyRuntimeRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologyRuntimeRecord{}, false, fmt.Errorf("decode topology runtime: %w", err)
		}
		record = normalizeTopologyRuntimeRecord(record)
		if record.SwarmID == "" {
			record.SwarmID = decodeTopologyKeyValue(key, TopologyRuntimePrefixForAccount(accountScopeID))
		}
		if record.SwarmID == "" {
			return TopologyRuntimeRecord{}, false, nil
		}
		var err error
		record, err = enforceTopologyRuntimeAccount(accountScopeID, record)
		return record, err == nil, err
	}, limit)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return strings.ToLower(out[i].SwarmID) < strings.ToLower(out[j].SwarmID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *TopologyStore) listTopologyWorkspaceBindingRecordsForAccount(accountScopeID string, limit int) ([]TopologyWorkspaceBindingRecord, error) {
	out, err := listTopologyRecordsForAccount(s, TopologyWorkspaceBindingPrefixForAccount(accountScopeID), func(key string, value []byte) (TopologyWorkspaceBindingRecord, bool, error) {
		var record TopologyWorkspaceBindingRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return TopologyWorkspaceBindingRecord{}, false, fmt.Errorf("decode topology workspace binding: %w", err)
		}
		record = normalizeTopologyWorkspaceBindingRecord(record)
		if record.BindingID == "" {
			record.BindingID = decodeTopologyKeyValue(key, TopologyWorkspaceBindingPrefixForAccount(accountScopeID))
		}
		if record.BindingID == "" {
			return TopologyWorkspaceBindingRecord{}, false, nil
		}
		var err error
		record, err = enforceTopologyWorkspaceBindingAccount(accountScopeID, record)
		return record, err == nil, err
	}, limit)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return strings.ToLower(out[i].BindingID) < strings.ToLower(out[j].BindingID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func listTopologyRecordsForAccount[T any](s *TopologyStore, prefix string, decode func(key string, value []byte) (T, bool, error), limit int) ([]T, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("topology store is not configured")
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]T, 0, min(limit, 16))
	err := s.store.IteratePrefix(prefix, limit, func(key string, value []byte) error {
		record, ok, err := decode(key, value)
		if err != nil {
			return err
		}
		if ok {
			out = append(out, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func setTopologyBatchJSON(batch interface {
	Set([]byte, []byte, *pebble.WriteOptions) error
}, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return batch.Set([]byte(key), payload, nil)
}

func requireTopologyAccountScopeID(accountScopeID string) (string, error) {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return "", errors.New("account scope id is required")
	}
	return accountScopeID, nil
}

func nextTopologyWriteTimestamps(createdAt int64) (int64, int64) {
	now := time.Now().UnixMilli()
	if createdAt <= 0 {
		createdAt = now
	}
	return createdAt, now
}

func enforceTopologyRuntimeAccount(accountScopeID string, record TopologyRuntimeRecord) (TopologyRuntimeRecord, error) {
	if err := validateTopologyRecordAccount(accountScopeID, record.UserID, record.AccountScopeID); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	record.AccountScopeID = strings.TrimSpace(accountScopeID)
	record.UserID = strings.TrimSpace(record.UserID)
	return record, nil
}

func enforceTopologyWorkspaceBindingAccount(accountScopeID string, record TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if err := validateTopologyRecordAccount(accountScopeID, record.UserID, record.AccountScopeID); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	record.AccountScopeID = strings.TrimSpace(accountScopeID)
	record.UserID = strings.TrimSpace(record.UserID)
	return record, nil
}

func validateTopologyRecordAccount(accountScopeID, userID, recordAccountScopeID string) error {
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account scope id is required")
	}
	if strings.TrimSpace(userID) == "" {
		return errors.New("topology record user id is required")
	}
	recordAccountScopeID = strings.TrimSpace(recordAccountScopeID)
	if recordAccountScopeID == "" {
		return errors.New("topology record account scope id is required")
	}
	if recordAccountScopeID != accountScopeID {
		return errors.New("topology record account scope id does not match account key")
	}
	return nil
}
