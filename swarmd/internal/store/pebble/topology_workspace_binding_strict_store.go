package pebblestore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

var errTopologyWorkspaceBindingPlacementMissing = errors.New("topology workspace binding runtime placement is required")

func normalizeStrictTopologyWorkspaceBinding(record TopologyWorkspaceBindingRecord) TopologyWorkspaceBindingRecord {
	record = normalizeTopologyWorkspaceBindingRecord(record)
	if record.State == "" {
		record.State = TopologyWorkspaceBindingStateBound
	}
	return record
}

func normalizeStrictTopologyWorkspaceBindingDefaults(record TopologyWorkspaceBindingRecord) TopologyWorkspaceBindingRecord {
	record = normalizeStrictTopologyWorkspaceBinding(record)
	legacyRecord := strings.TrimSpace(record.LegacyTargetKind) != ""
	if record.DestinationAuthorityHostSwarmID == "" && !legacyRecord {
		record.DestinationAuthorityHostSwarmID = record.DestinationHostSwarmID
	}
	if record.DestinationHostSwarmID == "" {
		record.DestinationHostSwarmID = record.DestinationAuthorityHostSwarmID
	}
	if record.DestinationRuntimeKind == "" && record.DestinationContainerID != "" {
		record.DestinationRuntimeKind = TopologyRuntimeKindContainer
	}
	if record.BindingGeneration <= 0 {
		record.BindingGeneration = 1
	}
	if record.State == TopologyWorkspaceBindingStateBound {
		if record.AccessMode == "" {
			record.AccessMode = TopologyWorkspaceBindingAccessModeReadWrite
		}
		if record.MaterializationKind == "" {
			record.MaterializationKind = TopologyWorkspaceBindingMaterializationSource
		}
		if record.AttestedByHostSwarmID == "" {
			record.AttestedByHostSwarmID = record.DestinationAuthorityHostSwarmID
		}
	}
	return record
}

func topologyWorkspaceBindingValidAccessMode(accessMode string) bool {
	switch strings.ToLower(strings.TrimSpace(accessMode)) {
	case TopologyWorkspaceBindingAccessModeReadOnly, TopologyWorkspaceBindingAccessModeReadWrite:
		return true
	default:
		return false
	}
}

func validateStrictTopologyWorkspaceBinding(record TopologyWorkspaceBindingRecord) error {
	record = normalizeStrictTopologyWorkspaceBindingDefaults(record)
	legacyRecord := strings.TrimSpace(record.LegacyTargetKind) != ""
	if record.BindingID == "" {
		return errors.New("topology workspace binding id is required")
	}
	if record.SourceWorkspaceID == "" && !legacyRecord {
		return errors.New("topology source workspace id is required")
	}
	if record.SourceWorkspaceGeneration <= 0 && !legacyRecord {
		return errors.New("topology source workspace generation is required")
	}
	if record.DestinationRuntimeSwarmID == "" {
		return errors.New("topology destination runtime swarm id is required")
	}
	if record.DestinationRuntimeKind == "" && !legacyRecord {
		return errors.New("topology destination runtime kind is required")
	}
	if record.DestinationRuntimeKind != "" && record.DestinationRuntimeKind != TopologyRuntimeKindHost && record.DestinationRuntimeKind != TopologyRuntimeKindContainer {
		return errors.New("topology destination runtime kind must be host or container")
	}
	if record.DestinationRuntimeKind == TopologyRuntimeKindHost && record.DestinationContainerID != "" {
		return errors.New("topology host workspace binding destination container id must be empty")
	}
	if record.DestinationRuntimeKind == TopologyRuntimeKindContainer && record.DestinationContainerID == "" {
		return errors.New("topology container workspace binding destination container id is required")
	}
	if record.DestinationWorkspacePath == "" {
		return errors.New("topology destination workspace path is required")
	}
	if record.PlacementGeneration <= 0 && !legacyRecord {
		return errors.New("topology workspace binding placement generation is required")
	}
	if record.BindingGeneration <= 0 {
		record.BindingGeneration = 1
	}
	if record.State == TopologyWorkspaceBindingStateBound {
		if record.AccessMode == "" {
			record.AccessMode = TopologyWorkspaceBindingAccessModeReadWrite
		}
		if !topologyWorkspaceBindingValidAccessMode(record.AccessMode) {
			return errors.New("topology workspace binding access mode must be read_only or read_write")
		}
		if record.MaterializationKind == "" {
			record.MaterializationKind = TopologyWorkspaceBindingMaterializationSource
		}
		if record.AttestedByHostSwarmID == "" {
			record.AttestedByHostSwarmID = record.DestinationAuthorityHostSwarmID
		}
		if record.AttestedByHostSwarmID != record.DestinationAuthorityHostSwarmID && !legacyRecord {
			return errors.New("topology workspace binding attesting host must equal destination authority host")
		}
	}
	return nil
}

func (s *TopologyStore) validateWorkspaceBindingPlacementForAccount(accountScopeID string, record TopologyWorkspaceBindingRecord) error {
	placement, ok, err := s.GetRuntimePlacementForAccount(accountScopeID, record.DestinationRuntimeSwarmID)
	if err != nil {
		return err
	}
	if !ok {
		return errTopologyWorkspaceBindingPlacementMissing
	}
	if placement.State != TopologyRuntimePlacementStateActive {
		return errors.New("topology workspace binding runtime placement must be active")
	}
	legacyRecord := strings.TrimSpace(record.LegacyTargetKind) != ""
	if placement.RuntimeKind != record.DestinationRuntimeKind {
		if !legacyRecord {
			return errors.New("topology workspace binding runtime kind does not match placement")
		}
		record.DestinationRuntimeKind = placement.RuntimeKind
	}
	if placement.AuthorityHostSwarmID != record.DestinationAuthorityHostSwarmID {
		if !legacyRecord {
			return errors.New("topology workspace binding authority host does not match placement")
		}
		record.DestinationAuthorityHostSwarmID = placement.AuthorityHostSwarmID
	}
	if placement.AuthorityContainerID != record.DestinationContainerID {
		if !legacyRecord {
			return errors.New("topology workspace binding destination container does not match placement")
		}
		record.DestinationContainerID = placement.AuthorityContainerID
	}
	if placement.PlacementGeneration != record.PlacementGeneration {
		if !legacyRecord {
			return errors.New("topology workspace binding placement generation does not match placement")
		}
		record.PlacementGeneration = placement.PlacementGeneration
	}
	return nil
}

func topologyWorkspaceBindingIsBound(record TopologyWorkspaceBindingRecord) bool {
	return normalizeStrictTopologyWorkspaceBinding(record).State == TopologyWorkspaceBindingStateBound
}

func topologyWorkspaceBindingActiveIndexKey(accountScopeID string, record TopologyWorkspaceBindingRecord) string {
	return KeyTopologyWorkspaceBindingActiveForAccount(accountScopeID, record.SourceWorkspaceID, record.DestinationRuntimeSwarmID)
}

func (s *TopologyStore) validateUniqueActiveWorkspaceBindingForAccount(accountScopeID string, record TopologyWorkspaceBindingRecord) error {
	if !topologyWorkspaceBindingIsBound(record) || strings.TrimSpace(record.SourceWorkspaceID) == "" {
		return nil
	}
	indexKey := topologyWorkspaceBindingActiveIndexKey(accountScopeID, record)
	var existingBindingID string
	ok, err := s.store.GetJSON(indexKey, &existingBindingID)
	if err != nil {
		return err
	}
	if ok && strings.TrimSpace(existingBindingID) != "" && strings.TrimSpace(existingBindingID) != record.BindingID {
		return fmt.Errorf("active workspace binding already exists for workspace %q and runtime %q", record.SourceWorkspaceID, record.DestinationRuntimeSwarmID)
	}
	records, err := s.listTopologyWorkspaceBindingRecordsForAccount(accountScopeID, 100000)
	if err != nil {
		return err
	}
	for _, existing := range records {
		existing = normalizeTopologyWorkspaceBindingRecord(existing)
		if existing.BindingID == record.BindingID || !topologyWorkspaceBindingIsBound(existing) {
			continue
		}
		if existing.SourceWorkspaceID == record.SourceWorkspaceID && existing.DestinationRuntimeSwarmID == record.DestinationRuntimeSwarmID {
			return fmt.Errorf("active workspace binding already exists for workspace %q and runtime %q", record.SourceWorkspaceID, record.DestinationRuntimeSwarmID)
		}
	}
	return nil
}

func (s *TopologyStore) putStrictWorkspaceBindingForAccount(accountScopeID string, record TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	record = normalizeStrictTopologyWorkspaceBindingDefaults(record)
	if err := validateStrictTopologyWorkspaceBinding(record); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	existing, existingOK, err := s.GetWorkspaceBindingForAccount(accountScopeID, record.BindingID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	if err := s.validateWorkspaceBindingPlacementForAccount(accountScopeID, record); err != nil {
		if strings.TrimSpace(record.LegacyTargetKind) != "" {
			if errors.Is(err, errTopologyWorkspaceBindingPlacementMissing) || strings.Contains(err.Error(), "runtime kind does not match placement") || strings.Contains(err.Error(), "authority host does not match placement") || strings.Contains(err.Error(), "destination container does not match placement") || strings.Contains(err.Error(), "placement generation does not match placement") {
				// Legacy topology bindings remain readable test/migration fixtures, but
				// strict session creation validates complete binding/placement identity.
			} else {
				return TopologyWorkspaceBindingRecord{}, err
			}
		} else {
			return TopologyWorkspaceBindingRecord{}, err
		}
	}
	if err := s.validateUniqueActiveWorkspaceBindingForAccount(accountScopeID, record); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}

	createdAt, updatedAt := nextTopologyWriteTimestamps(record.CreatedAt)
	record.CreatedAt = createdAt
	record.UpdatedAt = updatedAt
	batch := s.store.NewBatch()
	defer batch.Close()
	if err := setTopologyBatchJSON(batch, KeyTopologyWorkspaceBindingForAccount(accountScopeID, record.BindingID), record); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	if existingOK && topologyWorkspaceBindingIsBound(existing) && topologyWorkspaceBindingActiveIndexKey(accountScopeID, existing) != topologyWorkspaceBindingActiveIndexKey(accountScopeID, record) {
		if err := batch.Delete([]byte(topologyWorkspaceBindingActiveIndexKey(accountScopeID, existing)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return TopologyWorkspaceBindingRecord{}, err
		}
	}
	if topologyWorkspaceBindingIsBound(record) {
		if err := setTopologyBatchJSON(batch, topologyWorkspaceBindingActiveIndexKey(accountScopeID, record), record.BindingID); err != nil {
			return TopologyWorkspaceBindingRecord{}, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	return record, nil
}
