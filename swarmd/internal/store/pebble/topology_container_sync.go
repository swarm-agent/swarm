package pebblestore

import "strings"

const (
	TopologyRuntimeSourceDeployContainer           = "deploy_container"
	TopologyRuntimeSourceRemoteDeploySession       = "remote_deploy_session"
	TopologyHostContainerSourceSwarmLocalContainer = "swarm_local_container"
	TopologyHostContainerSourceDeployContainer     = "deploy_container"
	TopologyHostContainerSourceRemoteDeploySession = "remote_deploy_session"
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
	return findTopologyHostContainerByRefs(topology, "", hostSwarmID, refs...)
}

func FindTopologyHostContainerByRefsForAccount(topology *TopologyStore, accountScopeID, hostSwarmID string, refs ...string) (TopologyHostContainerRecord, bool, error) {
	return findTopologyHostContainerByRefs(topology, strings.TrimSpace(accountScopeID), hostSwarmID, refs...)
}

func findTopologyHostContainerByRefs(topology *TopologyStore, accountScopeID, hostSwarmID string, refs ...string) (TopologyHostContainerRecord, bool, error) {
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
		var (
			record TopologyHostContainerRecord
			ok     bool
			err    error
		)
		if accountScopeID != "" {
			record, ok, err = topology.GetHostContainerForAccount(accountScopeID, hostContainerID)
		} else {
			record, ok, err = topology.GetHostContainer(hostContainerID)
		}
		if err != nil || ok {
			return record, ok, err
		}
	}
	return TopologyHostContainerRecord{}, false, nil
}

func UpsertTopologyHostContainer(topology *TopologyStore, incoming TopologyHostContainerRecord) error {
	return upsertTopologyHostContainer(topology, "", incoming)
}

func UpsertTopologyHostContainerForAccount(topology *TopologyStore, accountScopeID string, incoming TopologyHostContainerRecord) error {
	return upsertTopologyHostContainer(topology, strings.TrimSpace(accountScopeID), incoming)
}

func upsertTopologyHostContainer(topology *TopologyStore, accountScopeID string, incoming TopologyHostContainerRecord) error {
	if topology == nil {
		return nil
	}
	incoming = normalizeTopologyHostContainerRecord(incoming)
	if incoming.HostContainerID == "" {
		return nil
	}
	var (
		existing TopologyHostContainerRecord
		ok       bool
		err      error
	)
	if accountScopeID != "" {
		existing, ok, err = topology.GetHostContainerForAccount(accountScopeID, incoming.HostContainerID)
	} else {
		existing, ok, err = topology.GetHostContainer(incoming.HostContainerID)
	}
	if err != nil {
		return err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return err
		}
		incoming = mergeTopologyHostContainerRecord(existing, incoming)
	}
	if accountScopeID != "" {
		_, err = topology.PutHostContainerForAccount(accountScopeID, incoming)
	} else {
		_, err = topology.PutHostContainer(incoming)
	}
	return err
}

func UpsertTopologyAttachment(topology *TopologyStore, incoming TopologyAttachmentRecord) error {
	return upsertTopologyAttachment(topology, "", incoming)
}

func UpsertTopologyAttachmentForAccount(topology *TopologyStore, accountScopeID string, incoming TopologyAttachmentRecord) error {
	return upsertTopologyAttachment(topology, strings.TrimSpace(accountScopeID), incoming)
}

func upsertTopologyAttachment(topology *TopologyStore, accountScopeID string, incoming TopologyAttachmentRecord) error {
	if topology == nil {
		return nil
	}
	incoming = normalizeTopologyAttachmentRecord(incoming)
	if incoming.AttachmentID == "" {
		return nil
	}
	var (
		existing TopologyAttachmentRecord
		ok       bool
		err      error
	)
	if accountScopeID != "" {
		existing, ok, err = topology.GetAttachmentForAccount(accountScopeID, incoming.AttachmentID)
	} else {
		existing, ok, err = topology.GetAttachment(incoming.AttachmentID)
	}
	if err != nil {
		return err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return err
		}
		incoming = mergeTopologyAttachmentRecord(existing, incoming)
	}
	if accountScopeID != "" {
		_, err = topology.PutAttachmentForAccount(accountScopeID, incoming)
	} else {
		_, err = topology.PutAttachment(incoming)
	}
	return err
}

func UpsertTopologyWorkspaceBinding(topology *TopologyStore, incoming TopologyWorkspaceBindingRecord) (TopologyWorkspaceBindingRecord, error) {
	if topology == nil {
		return TopologyWorkspaceBindingRecord{}, nil
	}
	incoming = normalizeTopologyWorkspaceBindingRecord(incoming)
	if incoming.BindingID == "" || incoming.SourceWorkspacePath == "" {
		return incoming, nil
	}
	existing, ok, err := topology.GetWorkspaceBinding(incoming.BindingID)
	if err != nil {
		return TopologyWorkspaceBindingRecord{}, err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return TopologyWorkspaceBindingRecord{}, err
		}
		if incoming.CreatedAt <= 0 {
			incoming.CreatedAt = existing.CreatedAt
		}
		incoming.UserID = firstNonEmpty(incoming.UserID, existing.UserID)
		incoming.AccountScopeID = firstNonEmpty(incoming.AccountScopeID, existing.AccountScopeID)
	}
	return topology.PutWorkspaceBinding(incoming)
}

func RemoveTopologyRuntimeObservedSource(topology *TopologyStore, swarmID, source string) error {
	return removeTopologyRuntimeObservedSource(topology, swarmID, source)
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
	incoming.DeploymentID = firstNonEmpty(incoming.DeploymentID, existing.DeploymentID)
	incoming.RemoteDeploySessionID = firstNonEmpty(incoming.RemoteDeploySessionID, existing.RemoteDeploySessionID)
	incoming.LastError = firstNonEmpty(incoming.LastError, existing.LastError)
	if existing.CreatedAt > 0 && (incoming.CreatedAt <= 0 || existing.CreatedAt < incoming.CreatedAt) {
		incoming.CreatedAt = existing.CreatedAt
	}
	if incoming.UpdatedAt < existing.UpdatedAt {
		incoming.UpdatedAt = existing.UpdatedAt
	}
	return incoming
}
