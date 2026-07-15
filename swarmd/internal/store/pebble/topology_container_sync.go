package pebblestore

import (
	"errors"
	"strings"
)

const (
	TopologyHostContainerSourceSwarmLocalContainer = "swarm_local_container"
)

func CanonicalTopologyHostContainerID(hostSwarmID, runtimeContainerRef string) string {
	hostSwarmID = strings.TrimSpace(hostSwarmID)
	runtimeContainerRef = strings.TrimSpace(runtimeContainerRef)
	if hostSwarmID == "" || runtimeContainerRef == "" {
		return ""
	}
	return hostSwarmID + ":" + strings.ToLower(runtimeContainerRef)
}

func CanonicalTopologyAttachmentID(hostContainerID, runtimeSwarmID string) string {
	hostContainerID = strings.TrimSpace(hostContainerID)
	runtimeSwarmID = strings.TrimSpace(runtimeSwarmID)
	if hostContainerID == "" || runtimeSwarmID == "" {
		return ""
	}
	return hostContainerID + "=>" + runtimeSwarmID
}

func CanonicalTopologyWorkspaceBindingID(replicationID, sourceWorkspacePath string) string {
	replicationID = strings.TrimSpace(replicationID)
	sourceWorkspacePath = strings.TrimSpace(sourceWorkspacePath)
	if replicationID == "" || sourceWorkspacePath == "" {
		return ""
	}
	return "binding:replica:" + replicationID + ":" + sourceWorkspacePath
}

func FindTopologyHostContainerByRefs(topology *TopologyStore, hostSwarmID string, refs ...string) (TopologyHostContainerRecord, bool, error) {
	if topology == nil {
		return TopologyHostContainerRecord{}, false, nil
	}
	hostSwarmID = strings.TrimSpace(hostSwarmID)
	seen := map[string]struct{}{}
	for _, rawRef := range refs {
		ref := strings.TrimSpace(rawRef)
		if ref == "" {
			continue
		}
		hostContainerID := CanonicalTopologyHostContainerID(hostSwarmID, ref)
		if hostContainerID == "" {
			continue
		}
		if _, ok := seen[hostContainerID]; ok {
			continue
		}
		seen[hostContainerID] = struct{}{}
		record, ok, err := topology.GetHostContainer(hostContainerID)
		if err != nil || ok {
			return record, ok, err
		}
	}
	return TopologyHostContainerRecord{}, false, nil
}

func FindTopologyHostContainerByRefsForAccount(topology *TopologyStore, accountScopeID, hostSwarmID string, refs ...string) (TopologyHostContainerRecord, bool, error) {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return TopologyHostContainerRecord{}, false, err
	}
	if topology == nil {
		return TopologyHostContainerRecord{}, false, nil
	}
	hostSwarmID = strings.TrimSpace(hostSwarmID)
	seen := map[string]struct{}{}
	for _, rawRef := range refs {
		ref := strings.TrimSpace(rawRef)
		if ref == "" {
			continue
		}
		hostContainerID := CanonicalTopologyHostContainerID(hostSwarmID, ref)
		if hostContainerID == "" {
			continue
		}
		if _, ok := seen[hostContainerID]; ok {
			continue
		}
		seen[hostContainerID] = struct{}{}
		record, ok, err := topology.GetHostContainerForAccount(accountScopeID, hostContainerID)
		if err != nil || ok {
			return record, ok, err
		}
	}
	return TopologyHostContainerRecord{}, false, nil
}

func UpsertTopologyHostContainer(topology *TopologyStore, incoming TopologyHostContainerRecord) error {
	if topology == nil {
		return nil
	}
	incoming = normalizeTopologyHostContainerRecord(incoming)
	if incoming.HostContainerID == "" {
		return nil
	}
	existing, ok, err := topology.GetHostContainer(incoming.HostContainerID)
	if err != nil {
		return err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return err
		}
		incoming = mergeTopologyHostContainerRecord(existing, incoming)
	}
	_, err = topology.PutHostContainer(incoming)
	return err
}

func UpsertTopologyHostContainerForAccount(topology *TopologyStore, accountScopeID string, incoming TopologyHostContainerRecord) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if topology == nil {
		return nil
	}
	incoming = normalizeTopologyHostContainerRecord(incoming)
	if incoming.HostContainerID == "" {
		return nil
	}
	existing, ok, err := topology.GetHostContainerForAccount(accountScopeID, incoming.HostContainerID)
	if err != nil {
		return err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return err
		}
		incoming = mergeTopologyHostContainerRecord(existing, incoming)
	}
	_, err = topology.PutHostContainerForAccount(accountScopeID, incoming)
	return err
}

func UpsertTopologyAttachment(topology *TopologyStore, incoming TopologyAttachmentRecord) error {
	if topology == nil {
		return nil
	}
	incoming = normalizeTopologyAttachmentRecord(incoming)
	if incoming.AttachmentID == "" {
		return nil
	}
	existing, ok, err := topology.GetAttachment(incoming.AttachmentID)
	if err != nil {
		return err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return err
		}
		incoming = mergeTopologyAttachmentRecord(existing, incoming)
	}
	_, err = topology.PutAttachment(incoming)
	return err
}

func UpsertTopologyAttachmentForAccount(topology *TopologyStore, accountScopeID string, incoming TopologyAttachmentRecord) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if topology == nil {
		return nil
	}
	incoming = normalizeTopologyAttachmentRecord(incoming)
	if incoming.AttachmentID == "" {
		return nil
	}
	existing, ok, err := topology.GetAttachmentForAccount(accountScopeID, incoming.AttachmentID)
	if err != nil {
		return err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return err
		}
		incoming = mergeTopologyAttachmentRecord(existing, incoming)
	}
	_, err = topology.PutAttachmentForAccount(accountScopeID, incoming)
	return err
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

func RemoveTopologyRuntimeObservedSource(topology *TopologyStore, swarmID, source string) error {
	return removeTopologyRuntimeObservedSource(topology, swarmID, source)
}

func RemoveTopologyRuntimeObservedSourceForAccount(topology *TopologyStore, accountScopeID, swarmID, source string) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if topology == nil {
		return nil
	}
	swarmID = strings.TrimSpace(swarmID)
	source = strings.TrimSpace(source)
	if swarmID == "" || source == "" {
		return nil
	}
	record, ok, err := topology.GetRuntimeForAccount(accountScopeID, swarmID)
	if err != nil || !ok {
		return err
	}
	record.ObservedSources = removeTopologyObservedSource(record.ObservedSources, source)
	if len(record.ObservedSources) == 0 {
		return topology.DeleteRuntimeForAccount(accountScopeID, swarmID)
	}
	_, err = topology.PutRuntimeForAccount(accountScopeID, record)
	return err
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

func mergeTopologyHostContainerRecord(existing, incoming TopologyHostContainerRecord) TopologyHostContainerRecord {
	existing = normalizeTopologyHostContainerRecord(existing)
	incoming = normalizeTopologyHostContainerRecord(incoming)
	incoming.UserID = firstNonEmpty(incoming.UserID, existing.UserID)
	incoming.AccountScopeID = firstNonEmpty(incoming.AccountScopeID, existing.AccountScopeID)
	incoming.HostSwarmID = firstNonEmpty(incoming.HostSwarmID, existing.HostSwarmID)
	incoming.RuntimeContainerRef = firstNonEmpty(incoming.RuntimeContainerRef, existing.RuntimeContainerRef)
	incoming.Name = firstNonEmpty(incoming.Name, existing.Name, incoming.HostContainerID)
	incoming.ContainerName = firstNonEmpty(incoming.ContainerName, existing.ContainerName)
	incoming.ContainerID = firstNonEmpty(incoming.ContainerID, existing.ContainerID)
	incoming.Runtime = firstNonEmpty(incoming.Runtime, existing.Runtime)
	incoming.Image = firstNonEmpty(incoming.Image, existing.Image)
	incoming.Status = firstNonEmpty(incoming.Status, existing.Status)
	incoming.HostAPIBaseURL = firstNonEmpty(incoming.HostAPIBaseURL, existing.HostAPIBaseURL)
	if incoming.HostPort <= 0 {
		incoming.HostPort = existing.HostPort
	}
	if incoming.RuntimePort <= 0 {
		incoming.RuntimePort = existing.RuntimePort
	}
	if len(incoming.Mounts) == 0 {
		incoming.Mounts = existing.Mounts
	}
	incoming.ObservedSources = normalizeTopologyStringList(append(append([]string(nil), existing.ObservedSources...), incoming.ObservedSources...))
	if existing.CreatedAt > 0 && (incoming.CreatedAt <= 0 || existing.CreatedAt < incoming.CreatedAt) {
		incoming.CreatedAt = existing.CreatedAt
	}
	if incoming.UpdatedAt < existing.UpdatedAt {
		incoming.UpdatedAt = existing.UpdatedAt
	}
	return incoming
}

func mergeTopologyAttachmentRecord(existing, incoming TopologyAttachmentRecord) TopologyAttachmentRecord {
	existing = normalizeTopologyAttachmentRecord(existing)
	incoming = normalizeTopologyAttachmentRecord(incoming)
	incoming.UserID = firstNonEmpty(incoming.UserID, existing.UserID)
	incoming.AccountScopeID = firstNonEmpty(incoming.AccountScopeID, existing.AccountScopeID)
	incoming.HostContainerID = firstNonEmpty(incoming.HostContainerID, existing.HostContainerID)
	incoming.RuntimeSwarmID = firstNonEmpty(incoming.RuntimeSwarmID, existing.RuntimeSwarmID)
	incoming.State = firstNonEmpty(incoming.State, existing.State)
	incoming.LastError = firstNonEmpty(incoming.LastError, existing.LastError)
	if existing.CreatedAt > 0 && (incoming.CreatedAt <= 0 || existing.CreatedAt < incoming.CreatedAt) {
		incoming.CreatedAt = existing.CreatedAt
	}
	if incoming.UpdatedAt < existing.UpdatedAt {
		incoming.UpdatedAt = existing.UpdatedAt
	}
	return incoming
}
