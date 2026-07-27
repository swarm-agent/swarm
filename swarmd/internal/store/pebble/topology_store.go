package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	DefaultTopologyMigrationStatusID = "default"
	TopologySnapshotVersion          = "checkpoint1-v1"

	TopologyWorkspaceBindingStateBound            = "bound"
	TopologyWorkspaceBindingAccessModeLocal       = "local"
	TopologyWorkspaceBindingAccessModeReadOnly    = "read_only"
	TopologyWorkspaceBindingAccessModeReadWrite   = "read_write"
	TopologyWorkspaceBindingMaterializationSource = "source"
)

type TopologyRuntimeRecord struct {
	SwarmID              string   `json:"swarm_id"`
	UserID               string   `json:"user_id,omitempty"`
	AccountScopeID       string   `json:"account_scope_id,omitempty"`
	Name                 string   `json:"name"`
	Role                 string   `json:"role,omitempty"`
	Relationship         string   `json:"relationship,omitempty"`
	DesktopURL           string   `json:"desktop_url,omitempty"`
	Status               string   `json:"status,omitempty"`
	Transport            string   `json:"transport,omitempty"`
	OwnerHostSwarmID     string   `json:"owner_host_swarm_id,omitempty"`
	OwnerHostContainerID string   `json:"owner_host_container_id,omitempty"`
	GroupIDs             []string `json:"group_ids,omitempty"`
	ObservedSources      []string `json:"observed_sources,omitempty"`
	CreatedAt            int64    `json:"created_at"`
	UpdatedAt            int64    `json:"updated_at"`
}

type TopologyWorkspaceBindingRecord struct {
	BindingID                       string                   `json:"binding_id"`
	UserID                          string                   `json:"user_id,omitempty"`
	AccountScopeID                  string                   `json:"account_scope_id,omitempty"`
	SourceWorkspaceID               string                   `json:"source_workspace_id,omitempty"`
	SourceWorkspaceGeneration       int64                    `json:"source_workspace_generation,omitempty"`
	SourceWorkspacePath             string                   `json:"source_workspace_path"`
	SourceWorkspaceName             string                   `json:"source_workspace_name,omitempty"`
	DestinationRuntimeSwarmID       string                   `json:"destination_runtime_swarm_id,omitempty"`
	DestinationAuthorityHostSwarmID string                   `json:"destination_authority_host_swarm_id,omitempty"`
	DestinationRuntimeKind          string                   `json:"destination_runtime_kind,omitempty"`
	DestinationHostSwarmID          string                   `json:"destination_host_swarm_id,omitempty"`
	DestinationContainerID          string                   `json:"destination_container_id,omitempty"`
	DestinationWorkspacePath        string                   `json:"destination_workspace_path,omitempty"`
	PlacementGeneration             int                      `json:"placement_generation,omitempty"`
	BindingGeneration               int                      `json:"binding_generation,omitempty"`
	State                           string                   `json:"state,omitempty"`
	AccessMode                      string                   `json:"access_mode,omitempty"`
	MaterializationKind             string                   `json:"materialization_kind,omitempty"`
	AttestedByHostSwarmID           string                   `json:"attested_by_host_swarm_id,omitempty"`
	AttestedAt                      int64                    `json:"attested_at,omitempty"`
	ReplicationMode                 string                   `json:"replication_mode,omitempty"`
	Writable                        bool                     `json:"writable"`
	Sync                            WorkspaceReplicationSync `json:"sync,omitempty"`
	LegacyTargetKind                string                   `json:"legacy_target_kind,omitempty"`
	CreatedAt                       int64                    `json:"created_at"`
	UpdatedAt                       int64                    `json:"updated_at"`
}

type TopologyMigrationStatusRecord struct {
	ID                    string `json:"id"`
	Version               string `json:"version"`
	RebuiltAt             int64  `json:"rebuilt_at"`
	RuntimeCount          int    `json:"runtime_count"`
	WorkspaceBindingCount int    `json:"workspace_binding_count"`
}

type TopologySnapshot struct {
	Runtimes          []TopologyRuntimeRecord          `json:"runtimes,omitempty"`
	RuntimePlacements []TopologyRuntimePlacementRecord `json:"runtime_placements,omitempty"`
	WorkspaceBindings []TopologyWorkspaceBindingRecord `json:"workspace_bindings,omitempty"`
	MigrationStatus   TopologyMigrationStatusRecord    `json:"migration_status"`
}

type TopologyStore struct {
	store *Store
}

func NewTopologyStore(store *Store) *TopologyStore {
	return &TopologyStore{store: store}
}

// ReplaceSnapshot rewrites legacy/global topology keys for explicit migration/internal rebuilds only.
// Account-owned product topology must use ReplaceSnapshotForAccount so an account update
// cannot delete another account's topology records or fall back to global mutable keys.
func (s *TopologyStore) ReplaceSnapshot(snapshot TopologySnapshot) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	snapshot.Runtimes = normalizeTopologyRuntimeRecords(snapshot.Runtimes)
	snapshot.RuntimePlacements = normalizeTopologyRuntimePlacementRecords(snapshot.RuntimePlacements)
	snapshot.WorkspaceBindings = normalizeTopologyWorkspaceBindingRecords(snapshot.WorkspaceBindings)
	snapshot.MigrationStatus = normalizeTopologyMigrationStatusRecord(snapshot.MigrationStatus)
	batch := s.store.NewBatch()
	defer batch.Close()
	for _, prefix := range []string{
		TopologyRuntimePrefix(),
		TopologyRuntimePlacementPrefix(),
		TopologyWorkspaceBindingPrefix(),
		TopologyMigrationStatusPrefix(),
	} {
		if err := s.deletePrefixWithBatch(batch, prefix); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Runtimes {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology runtime %q: %w", record.SwarmID, err)
		}
		if err := batch.Set([]byte(KeyTopologyRuntime(record.SwarmID)), payload, nil); err != nil {
			return err
		}
	}
	for _, record := range snapshot.RuntimePlacements {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology runtime placement %q: %w", record.RuntimeSwarmID, err)
		}
		if err := batch.Set([]byte(KeyTopologyRuntimePlacement(record.RuntimeSwarmID)), payload, nil); err != nil {
			return err
		}
	}
	for _, record := range snapshot.WorkspaceBindings {
		payload, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("marshal topology workspace binding %q: %w", record.BindingID, err)
		}
		if err := batch.Set([]byte(KeyTopologyWorkspaceBinding(record.BindingID)), payload, nil); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(snapshot.MigrationStatus)
	if err != nil {
		return fmt.Errorf("marshal topology migration status %q: %w", snapshot.MigrationStatus.ID, err)
	}
	if err := batch.Set([]byte(KeyTopologyMigrationStatus(snapshot.MigrationStatus.ID)), payload, nil); err != nil {
		return err
	}
	return batch.Commit(nil)
}

func (s *TopologyStore) Snapshot() (TopologySnapshot, error) {
	if s == nil || s.store == nil {
		return TopologySnapshot{}, errors.New("topology store is not configured")
	}
	runtimes, err := s.ListRuntimes(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	runtimePlacements, err := s.listTopologyRuntimePlacementRecords(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	workspaceBindings, err := s.ListWorkspaceBindings(100000)
	if err != nil {
		return TopologySnapshot{}, err
	}
	migrationStatus, _, err := s.GetMigrationStatus(DefaultTopologyMigrationStatusID)
	if err != nil {
		return TopologySnapshot{}, err
	}
	return TopologySnapshot{
		Runtimes:          runtimes,
		RuntimePlacements: runtimePlacements,
		WorkspaceBindings: workspaceBindings,
		MigrationStatus:   migrationStatus,
	}, nil
}

func (s *TopologyStore) GetRuntime(swarmID string) (TopologyRuntimeRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologyRuntimeRecord{}, false, nil
	}
	swarmID = normalizeTopologyKeyValue(swarmID)
	if swarmID == "" {
		return TopologyRuntimeRecord{}, false, errors.New("topology runtime swarm id is required")
	}
	var record TopologyRuntimeRecord
	ok, err := s.store.GetJSON(KeyTopologyRuntime(swarmID), &record)
	if err != nil {
		return TopologyRuntimeRecord{}, false, err
	}
	if !ok {
		return TopologyRuntimeRecord{}, false, nil
	}
	record = normalizeTopologyRuntimeRecord(record)
	if record.SwarmID == "" {
		record.SwarmID = swarmID
	}
	return record, true, nil
}

func (s *TopologyStore) PutRuntime(record TopologyRuntimeRecord) (TopologyRuntimeRecord, error) {
	if s == nil || s.store == nil {
		return TopologyRuntimeRecord{}, errors.New("topology store is not configured")
	}
	record = normalizeTopologyRuntimeRecord(record)
	if record.SwarmID == "" {
		return TopologyRuntimeRecord{}, errors.New("topology runtime swarm id is required")
	}
	if record.Name == "" {
		record.Name = record.SwarmID
	}
	now := time.Now().UnixMilli()
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	if err := s.store.PutJSON(KeyTopologyRuntime(record.SwarmID), record); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	if _, err := s.refreshMigrationStatus(); err != nil {
		return TopologyRuntimeRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) DeleteRuntime(swarmID string) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	swarmID = normalizeTopologyKeyValue(swarmID)
	if swarmID == "" {
		return errors.New("topology runtime swarm id is required")
	}
	if err := s.store.Delete(KeyTopologyRuntime(swarmID)); err != nil {
		return err
	}
	if err := s.DeleteRuntimePlacement(swarmID); err != nil {
		return err
	}
	_, err := s.refreshMigrationStatus()
	return err
}

func (s *TopologyStore) ListRuntimes(limit int) ([]TopologyRuntimeRecord, error) {
	return s.listTopologyRuntimeRecords(limit)
}

func (s *TopologyStore) ListWorkspaceBindings(limit int) ([]TopologyWorkspaceBindingRecord, error) {
	return s.listTopologyWorkspaceBindingRecords(limit)
}

func (s *TopologyStore) ListWorkspaceBindingsBySourcePath(sourceWorkspacePath string, limit int) ([]TopologyWorkspaceBindingRecord, error) {
	sourceWorkspacePath = strings.TrimSpace(sourceWorkspacePath)
	if sourceWorkspacePath == "" {
		return nil, errors.New("topology source workspace path is required")
	}
	records, err := s.listTopologyWorkspaceBindingRecords(limit)
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

func (s *TopologyStore) GetWorkspaceBinding(bindingID string) (TopologyWorkspaceBindingRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, false, nil
	}
	bindingID = normalizeTopologyKeyValue(bindingID)
	if bindingID == "" {
		return TopologyWorkspaceBindingRecord{}, false, errors.New("topology workspace binding id is required")
	}
	var record TopologyWorkspaceBindingRecord
	ok, err := s.store.GetJSON(KeyTopologyWorkspaceBinding(bindingID), &record)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, false, err
	}
	if !ok {
		return TopologyWorkspaceBindingRecord{}, false, nil
	}
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record.BindingID == "" {
		record.BindingID = bindingID
	}
	return record, true, nil
}

func (s *TopologyStore) PutWorkspaceBinding(record TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.store == nil {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology store is not configured")
	}
	return TopologyWorkspaceBindingRecord{}, errors.New("legacy global topology workspace binding writes are forbidden; use account-scoped strict binding writes")
}

func (s *TopologyStore) DeleteWorkspaceBinding(bindingID string) error {
	if s == nil || s.store == nil {
		return errors.New("topology store is not configured")
	}
	bindingID = normalizeTopologyKeyValue(bindingID)
	if bindingID == "" {
		return errors.New("topology workspace binding id is required")
	}
	if err := s.store.Delete(KeyTopologyWorkspaceBinding(bindingID)); err != nil {
		return err
	}
	_, err := s.refreshMigrationStatus()
	return err
}

func (s *TopologyStore) GetMigrationStatus(id string) (TopologyMigrationStatusRecord, bool, error) {
	if s == nil || s.store == nil {
		return TopologyMigrationStatusRecord{}, false, nil
	}
	id = normalizeTopologyKeyValue(id)
	if id == "" {
		id = DefaultTopologyMigrationStatusID
	}
	var record TopologyMigrationStatusRecord
	ok, err := s.store.GetJSON(KeyTopologyMigrationStatus(id), &record)
	if err != nil {
		return TopologyMigrationStatusRecord{}, false, err
	}
	if !ok {
		return TopologyMigrationStatusRecord{}, false, nil
	}
	record = normalizeTopologyMigrationStatusRecord(record)
	if record.ID == "" {
		record.ID = id
	}
	return record, true, nil
}

func (s *TopologyStore) PutMigrationStatus(record TopologyMigrationStatusRecord) (TopologyMigrationStatusRecord, error) {
	if s == nil || s.store == nil {
		return TopologyMigrationStatusRecord{}, errors.New("topology store is not configured")
	}
	record = normalizeTopologyMigrationStatusRecord(record)
	if record.RebuiltAt <= 0 {
		record.RebuiltAt = time.Now().UnixMilli()
	}
	if err := s.store.PutJSON(KeyTopologyMigrationStatus(record.ID), record); err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	return record, nil
}

func (s *TopologyStore) RefreshMigrationStatus() (TopologyMigrationStatusRecord, error) {
	if s == nil || s.store == nil {
		return TopologyMigrationStatusRecord{}, errors.New("topology store is not configured")
	}
	return s.refreshMigrationStatus()
}

func (s *TopologyStore) refreshMigrationStatus() (TopologyMigrationStatusRecord, error) {
	runtimes, err := s.ListRuntimes(100000)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	accountRuntimeCount, err := s.countTopologyJSONRecordsWithPrefix(KeyTopologyRuntimeAccountPrefix)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	workspaceBindings, err := s.ListWorkspaceBindings(100000)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	accountWorkspaceBindingCount, err := s.countTopologyJSONRecordsWithPrefix(KeyTopologyWorkspaceBindingAccountPrefix)
	if err != nil {
		return TopologyMigrationStatusRecord{}, err
	}
	return s.PutMigrationStatus(TopologyMigrationStatusRecord{
		ID:                    DefaultTopologyMigrationStatusID,
		Version:               TopologySnapshotVersion,
		RebuiltAt:             time.Now().UnixMilli(),
		RuntimeCount:          len(runtimes) + accountRuntimeCount,
		WorkspaceBindingCount: len(workspaceBindings) + accountWorkspaceBindingCount,
	})
}

func (s *TopologyStore) countTopologyJSONRecordsWithPrefix(prefix string) (int, error) {
	if s == nil || s.store == nil {
		return 0, errors.New("topology store is not configured")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return 0, errors.New("topology record prefix is required")
	}
	count := 0
	err := s.store.IteratePrefix(prefix, 100000, func(key string, value []byte) error {
		var raw json.RawMessage
		if err := json.Unmarshal(value, &raw); err != nil {
			return fmt.Errorf("decode topology record %q: %w", key, err)
		}
		count++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *TopologyStore) listTopologyRuntimeRecords(limit int) ([]TopologyRuntimeRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]TopologyRuntimeRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(TopologyRuntimePrefix(), limit, func(key string, value []byte) error {
		var record TopologyRuntimeRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode topology runtime: %w", err)
		}
		record = normalizeTopologyRuntimeRecord(record)
		if record.SwarmID == "" {
			record.SwarmID = decodeTopologyKeyValue(key, TopologyRuntimePrefix())
		}
		if record.SwarmID == "" {
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
			return strings.ToLower(out[i].SwarmID) < strings.ToLower(out[j].SwarmID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *TopologyStore) listTopologyWorkspaceBindingRecords(limit int) ([]TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]TopologyWorkspaceBindingRecord, 0, min(limit, 16))
	err := s.store.IteratePrefix(TopologyWorkspaceBindingPrefix(), limit, func(key string, value []byte) error {
		var record TopologyWorkspaceBindingRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode topology workspace binding: %w", err)
		}
		record = normalizeTopologyWorkspaceBindingRecord(record)
		if record.BindingID == "" {
			record.BindingID = decodeTopologyKeyValue(key, TopologyWorkspaceBindingPrefix())
		}
		if record.BindingID == "" {
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
			return strings.ToLower(out[i].BindingID) < strings.ToLower(out[j].BindingID)
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *TopologyStore) deletePrefixWithBatch(batch *pebble.Batch, prefix string) error {
	iter, err := s.store.db.NewIter(nil)
	if err != nil {
		return fmt.Errorf("create topology prefix iterator: %w", err)
	}
	defer iter.Close()
	for iter.SeekGE([]byte(prefix)); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, prefix) {
			break
		}
		if err := batch.Delete(iter.Key(), nil); err != nil {
			return err
		}
	}
	return iter.Error()
}

func normalizeTopologyRuntimeRecords(records []TopologyRuntimeRecord) []TopologyRuntimeRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]TopologyRuntimeRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, raw := range records {
		record := normalizeTopologyRuntimeRecord(raw)
		if record.SwarmID == "" {
			continue
		}
		if _, ok := seen[record.SwarmID]; ok {
			continue
		}
		seen[record.SwarmID] = struct{}{}
		out = append(out, record)
	}
	return out
}

func normalizeTopologyRuntimeRecord(record TopologyRuntimeRecord) TopologyRuntimeRecord {
	record.SwarmID = normalizeTopologyKeyValue(record.SwarmID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.Name = strings.TrimSpace(record.Name)
	record.Role = strings.ToLower(strings.TrimSpace(record.Role))
	record.Relationship = strings.ToLower(strings.TrimSpace(record.Relationship))
	record.DesktopURL = strings.TrimSpace(record.DesktopURL)
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	record.Transport = strings.ToLower(strings.TrimSpace(record.Transport))
	record.OwnerHostSwarmID = strings.TrimSpace(record.OwnerHostSwarmID)
	record.OwnerHostContainerID = strings.TrimSpace(record.OwnerHostContainerID)
	record.GroupIDs = normalizeTopologyStringList(record.GroupIDs)
	record.ObservedSources = normalizeTopologyStringList(record.ObservedSources)
	record.CreatedAt, record.UpdatedAt = normalizeTopologyTimestamps(record.CreatedAt, record.UpdatedAt)
	return record
}

func normalizeTopologyWorkspaceBindingRecords(records []TopologyWorkspaceBindingRecord) []TopologyWorkspaceBindingRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]TopologyWorkspaceBindingRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, raw := range records {
		record := normalizeTopologyWorkspaceBindingRecord(raw)
		if record.BindingID == "" {
			continue
		}
		if _, ok := seen[record.BindingID]; ok {
			continue
		}
		seen[record.BindingID] = struct{}{}
		out = append(out, record)
	}
	return out
}

func normalizeTopologyWorkspaceBindingRecord(record TopologyWorkspaceBindingRecord) TopologyWorkspaceBindingRecord {
	record.BindingID = normalizeTopologyKeyValue(record.BindingID)
	record.UserID = strings.TrimSpace(record.UserID)
	record.AccountScopeID = strings.TrimSpace(record.AccountScopeID)
	record.SourceWorkspaceID = normalizeTopologyKeyValue(record.SourceWorkspaceID)
	if record.SourceWorkspaceGeneration < 0 {
		record.SourceWorkspaceGeneration = 0
	}
	record.SourceWorkspacePath = strings.TrimSpace(record.SourceWorkspacePath)
	record.SourceWorkspaceName = strings.TrimSpace(record.SourceWorkspaceName)
	record.DestinationRuntimeSwarmID = strings.TrimSpace(record.DestinationRuntimeSwarmID)
	record.DestinationAuthorityHostSwarmID = strings.TrimSpace(record.DestinationAuthorityHostSwarmID)
	record.DestinationRuntimeKind = strings.ToLower(strings.TrimSpace(record.DestinationRuntimeKind))
	record.DestinationHostSwarmID = strings.TrimSpace(record.DestinationHostSwarmID)
	record.DestinationContainerID = strings.TrimSpace(record.DestinationContainerID)
	record.DestinationWorkspacePath = strings.TrimSpace(record.DestinationWorkspacePath)
	if record.PlacementGeneration < 0 {
		record.PlacementGeneration = 0
	}
	if record.BindingGeneration < 0 {
		record.BindingGeneration = 0
	}
	record.State = strings.ToLower(strings.TrimSpace(record.State))
	record.AccessMode = strings.ToLower(strings.TrimSpace(record.AccessMode))
	record.MaterializationKind = strings.ToLower(strings.TrimSpace(record.MaterializationKind))
	record.AttestedByHostSwarmID = strings.TrimSpace(record.AttestedByHostSwarmID)
	if record.AttestedAt < 0 {
		record.AttestedAt = 0
	}
	record.ReplicationMode = strings.TrimSpace(record.ReplicationMode)
	record.LegacyTargetKind = strings.TrimSpace(record.LegacyTargetKind)
	record.CreatedAt, record.UpdatedAt = normalizeTopologyTimestamps(record.CreatedAt, record.UpdatedAt)
	return record
}

func normalizeTopologyMigrationStatusRecord(record TopologyMigrationStatusRecord) TopologyMigrationStatusRecord {
	record.ID = normalizeTopologyKeyValue(record.ID)
	if record.ID == "" {
		record.ID = DefaultTopologyMigrationStatusID
	}
	record.Version = strings.TrimSpace(record.Version)
	if record.RebuiltAt < 0 {
		record.RebuiltAt = 0
	}
	if record.RuntimeCount < 0 {
		record.RuntimeCount = 0
	}
	if record.WorkspaceBindingCount < 0 {
		record.WorkspaceBindingCount = 0
	}
	return record
}

func normalizeTopologyKeyValue(value string) string {
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeTopologyStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeTopologyTimestamps(createdAt, updatedAt int64) (int64, int64) {
	now := time.Now().UnixMilli()
	if createdAt <= 0 {
		createdAt = now
	}
	if updatedAt <= 0 {
		updatedAt = createdAt
	}
	if createdAt < 0 {
		createdAt = 0
	}
	if updatedAt < 0 {
		updatedAt = createdAt
	}
	return createdAt, updatedAt
}

func decodeTopologyKeyValue(key, prefix string) string {
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	decoded, err := urlPathUnescape(strings.TrimPrefix(key, prefix))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(decoded)
}

func urlPathUnescape(value string) (string, error) {
	return url.PathUnescape(value)
}
