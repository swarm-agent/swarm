package pebblestore

import (
	"errors"
	"strings"
)

func CanonicalTopologyWorkspaceBindingID(replicationID, sourceWorkspacePath string) string {
	replicationID = strings.TrimSpace(replicationID)
	sourceWorkspacePath = strings.TrimSpace(sourceWorkspacePath)
	if replicationID == "" || sourceWorkspacePath == "" {
		return ""
	}
	return "binding:replica:" + replicationID + ":" + sourceWorkspacePath
}

func UpsertTopologyWorkspaceBinding(topology *TopologyStore, incoming TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if topology == nil {
		return TopologyWorkspaceBindingRecord{}, nil
	}
	incoming = normalizeTopologyWorkspaceBindingRecord(incoming)
	if incoming.AccountScopeID == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology workspace binding account scope id is required")
	}
	return UpsertTopologyWorkspaceBindingForAccount(topology, incoming.AccountScopeID, incoming)
}

func UpsertTopologyWorkspaceBindingForAccount(topology *TopologyStore, accountScopeID string, incoming TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	if topology == nil {
		return TopologyWorkspaceBindingRecord{}, nil
	}
	incoming = normalizeTopologyWorkspaceBindingRecord(incoming)
	if incoming.BindingID == "" {
		return TopologyWorkspaceBindingRecord{}, errors.New("topology workspace binding id is required")
	}
	existing, ok, err := topology.GetWorkspaceBindingForAccount(accountScopeID, incoming.BindingID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return TopologyWorkspaceBindingRecord{}, err
		}
		incoming = mergeTopologyWorkspaceBindingRecord(existing, incoming)
	}
	return topology.PutWorkspaceBindingForAccount(accountScopeID, incoming)
}

func mergeTopologyWorkspaceBindingRecord(existing, incoming TopologyWorkspaceBindingRecord) TopologyWorkspaceBindingRecord {
	existing = normalizeTopologyWorkspaceBindingRecord(existing)
	incoming = normalizeTopologyWorkspaceBindingRecord(incoming)
	incoming.UserID = firstNonEmpty(incoming.UserID, existing.UserID)
	incoming.AccountScopeID = firstNonEmpty(incoming.AccountScopeID, existing.AccountScopeID)
	incoming.SourceWorkspaceID = firstNonEmpty(incoming.SourceWorkspaceID, existing.SourceWorkspaceID)
	if incoming.SourceWorkspaceGeneration <= 0 {
		incoming.SourceWorkspaceGeneration = existing.SourceWorkspaceGeneration
	}
	incoming.SourceWorkspacePath = firstNonEmpty(incoming.SourceWorkspacePath, existing.SourceWorkspacePath)
	incoming.SourceWorkspaceName = firstNonEmpty(incoming.SourceWorkspaceName, existing.SourceWorkspaceName)
	incoming.DestinationRuntimeSwarmID = firstNonEmpty(incoming.DestinationRuntimeSwarmID, existing.DestinationRuntimeSwarmID)
	incoming.DestinationAuthorityHostSwarmID = firstNonEmpty(incoming.DestinationAuthorityHostSwarmID, existing.DestinationAuthorityHostSwarmID)
	incoming.DestinationRuntimeKind = firstNonEmpty(incoming.DestinationRuntimeKind, existing.DestinationRuntimeKind)
	incoming.DestinationHostSwarmID = firstNonEmpty(incoming.DestinationHostSwarmID, existing.DestinationHostSwarmID)
	incoming.DestinationContainerID = firstNonEmpty(incoming.DestinationContainerID, existing.DestinationContainerID)
	incoming.DestinationWorkspacePath = firstNonEmpty(incoming.DestinationWorkspacePath, existing.DestinationWorkspacePath)
	if incoming.PlacementGeneration <= 0 {
		incoming.PlacementGeneration = existing.PlacementGeneration
	}
	if incoming.BindingGeneration <= 0 {
		incoming.BindingGeneration = existing.BindingGeneration
	}
	incoming.State = firstNonEmpty(incoming.State, existing.State)
	incoming.AccessMode = firstNonEmpty(incoming.AccessMode, existing.AccessMode)
	incoming.MaterializationKind = firstNonEmpty(incoming.MaterializationKind, existing.MaterializationKind)
	incoming.AttestedByHostSwarmID = firstNonEmpty(incoming.AttestedByHostSwarmID, existing.AttestedByHostSwarmID)
	if incoming.AttestedAt <= 0 {
		incoming.AttestedAt = existing.AttestedAt
	}
	incoming.ReplicationMode = firstNonEmpty(incoming.ReplicationMode, existing.ReplicationMode)
	incoming.LegacyTargetKind = firstNonEmpty(incoming.LegacyTargetKind, existing.LegacyTargetKind)
	if !incoming.Writable {
		incoming.Writable = existing.Writable
	}
	if !incoming.Sync.Enabled && incoming.Sync.Mode == "" && len(incoming.Sync.Modules) == 0 {
		incoming.Sync = existing.Sync
	}
	if existing.CreatedAt > 0 && (incoming.CreatedAt <= 0 || existing.CreatedAt < incoming.CreatedAt) {
		incoming.CreatedAt = existing.CreatedAt
	}
	if incoming.UpdatedAt < existing.UpdatedAt {
		incoming.UpdatedAt = existing.UpdatedAt
	}
	return incoming
}
