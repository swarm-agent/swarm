package pebblestore

import "strings"

const (
	topologyRuntimeSourceLocalNode   = "swarm_local_node"
	topologyRuntimeSourceTrustedPeer = "swarm_trusted_peer"
	topologyRuntimeSourceNode        = "swarm_node"
)

func syncTopologyRuntimeFromLocalNode(topology *TopologyStore, record SwarmLocalNodeRecord) error {
	if topology == nil {
		return nil
	}
	incoming := TopologyRuntimeRecord{
		SwarmID:         strings.TrimSpace(record.SwarmID),
		Name:            firstNonEmpty(record.Name, record.SwarmID),
		Role:            strings.ToLower(strings.TrimSpace(record.Role)),
		Relationship:    "self",
		Status:          "online",
		Transport:       topologyTransportFromSwarmTransports(record.Transports),
		ObservedSources: []string{topologyRuntimeSourceLocalNode},
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}
	return UpsertTopologyRuntimeRecord(topology, incoming)
}

func syncTopologyRuntimeFromTrustedPeer(topology *TopologyStore, record SwarmTrustedPeerRecord) error {
	if topology == nil {
		return nil
	}
	incoming := TopologyRuntimeRecord{
		SwarmID:         strings.TrimSpace(record.SwarmID),
		Name:            firstNonEmpty(record.Name, record.SwarmID),
		Role:            strings.ToLower(strings.TrimSpace(record.Role)),
		Relationship:    strings.ToLower(strings.TrimSpace(record.Relationship)),
		Transport:       strings.ToLower(strings.TrimSpace(record.TransportMode)),
		ObservedSources: []string{topologyRuntimeSourceTrustedPeer},
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}
	return UpsertTopologyRuntimeRecord(topology, incoming)
}

func syncTopologyRuntimeFromNode(topology *TopologyStore, record SwarmNodeRecord) error {
	if topology == nil {
		return nil
	}
	incoming := TopologyRuntimeRecord{
		SwarmID:         strings.TrimSpace(record.SwarmID),
		Name:            firstNonEmpty(record.Name, record.SwarmID),
		Role:            strings.ToLower(strings.TrimSpace(record.Role)),
		Relationship:    topologyRelationshipFromSwarmNodeRole(record.Role),
		BackendURL:      strings.TrimSpace(record.BackendURL),
		DesktopURL:      strings.TrimSpace(record.DesktopURL),
		Status:          strings.ToLower(strings.TrimSpace(record.Status)),
		Transport:       strings.ToLower(strings.TrimSpace(record.Transport)),
		ObservedSources: []string{topologyRuntimeSourceNode},
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}
	return UpsertTopologyRuntimeRecord(topology, incoming)
}

func removeTopologyRuntimeTrustedPeer(topology *TopologyStore, swarmID string) error {
	return removeTopologyRuntimeObservedSource(topology, swarmID, topologyRuntimeSourceTrustedPeer)
}

func removeTopologyRuntimeNodeObservation(topology *TopologyStore, swarmID string) error {
	return removeTopologyRuntimeObservedSource(topology, swarmID, topologyRuntimeSourceNode)
}

func UpsertTopologyRuntimeRecord(topology *TopologyStore, incoming TopologyRuntimeRecord) error {
	if topology == nil {
		return nil
	}
	incoming = normalizeTopologyRuntimeRecord(incoming)
	if incoming.SwarmID == "" {
		return nil
	}
	existing, ok, err := topology.GetRuntime(incoming.SwarmID)
	if err != nil {
		return err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return err
		}
		incoming = mergeTopologyRuntimeRecord(existing, incoming)
	}
	_, err = topology.PutRuntime(incoming)
	return err
}

func mergeTopologyRuntimeRecord(existing, incoming TopologyRuntimeRecord) TopologyRuntimeRecord {
	existing = normalizeTopologyRuntimeRecord(existing)
	incoming = normalizeTopologyRuntimeRecord(incoming)
	incoming.UserID = firstNonEmpty(incoming.UserID, existing.UserID)
	incoming.AccountScopeID = firstNonEmpty(incoming.AccountScopeID, existing.AccountScopeID)
	incoming.Name = firstNonEmpty(incoming.Name, existing.Name, incoming.SwarmID)
	incoming.Role = firstNonEmpty(incoming.Role, existing.Role)
	incoming.Relationship = mergeTopologyRuntimeRelationship(existing.Relationship, incoming.Relationship)
	incoming.BackendURL = firstNonEmpty(incoming.BackendURL, existing.BackendURL)
	incoming.DesktopURL = firstNonEmpty(incoming.DesktopURL, existing.DesktopURL)
	incoming.Status = firstNonEmpty(incoming.Status, existing.Status)
	incoming.Transport = firstNonEmpty(incoming.Transport, existing.Transport)
	incoming.OwnerHostSwarmID = firstNonEmpty(incoming.OwnerHostSwarmID, existing.OwnerHostSwarmID)
	incoming.OwnerHostContainerID = firstNonEmpty(incoming.OwnerHostContainerID, existing.OwnerHostContainerID)
	incoming.GroupIDs = normalizeTopologyStringList(append(append([]string(nil), existing.GroupIDs...), incoming.GroupIDs...))
	incoming.ObservedSources = normalizeTopologyStringList(append(append([]string(nil), existing.ObservedSources...), incoming.ObservedSources...))
	if existing.CreatedAt > 0 && (incoming.CreatedAt <= 0 || existing.CreatedAt < incoming.CreatedAt) {
		incoming.CreatedAt = existing.CreatedAt
	}
	if incoming.UpdatedAt < existing.UpdatedAt {
		incoming.UpdatedAt = existing.UpdatedAt
	}
	return incoming
}

func removeTopologyRuntimeObservedSource(topology *TopologyStore, swarmID, source string) error {
	if topology == nil {
		return nil
	}
	swarmID = strings.TrimSpace(swarmID)
	source = strings.TrimSpace(source)
	if swarmID == "" || source == "" {
		return nil
	}
	record, ok, err := topology.GetRuntime(swarmID)
	if err != nil || !ok {
		return err
	}
	record.ObservedSources = removeTopologyObservedSource(record.ObservedSources, source)
	if len(record.ObservedSources) == 0 {
		return topology.DeleteRuntime(swarmID)
	}
	switch source {
	case topologyRuntimeSourceNode:
		record.BackendURL = ""
		record.DesktopURL = ""
		record.Status = ""
	case topologyRuntimeSourceTrustedPeer:
		if strings.EqualFold(strings.TrimSpace(record.Relationship), "self") {
			break
		}
		if !topologyObservedSourcePresent(record.ObservedSources, topologyRuntimeSourceNode) {
			record.Relationship = ""
		}
	}
	_, err = topology.PutRuntime(record)
	return err
}

func removeTopologyObservedSource(observedSources []string, target string) []string {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return normalizeTopologyStringList(observedSources)
	}
	filtered := make([]string, 0, len(observedSources))
	for _, raw := range observedSources {
		value := strings.TrimSpace(raw)
		if strings.EqualFold(value, target) {
			continue
		}
		filtered = append(filtered, value)
	}
	return normalizeTopologyStringList(filtered)
}

func topologyObservedSourcePresent(observedSources []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, raw := range observedSources {
		if strings.EqualFold(strings.TrimSpace(raw), target) {
			return true
		}
	}
	return false
}

func mergeTopologyRuntimeRelationship(existing, incoming string) string {
	existing = strings.ToLower(strings.TrimSpace(existing))
	incoming = strings.ToLower(strings.TrimSpace(incoming))
	if existing == "self" || incoming == "self" {
		return "self"
	}
	if incoming != "" {
		return incoming
	}
	return existing
}

func topologyRelationshipFromSwarmNodeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "managed", "manager", "child", "self":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

func topologyTransportFromSwarmTransports(transports []SwarmTransportRecord) string {
	for _, transport := range transports {
		kind := strings.ToLower(strings.TrimSpace(transport.Kind))
		if kind != "" {
			return kind
		}
	}
	return ""
}
