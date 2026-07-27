package pebblestore

import "strings"

const topologyRuntimeSourceLocalNode = "swarm_local_node"

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
	if strings.TrimSpace(incoming.AccountScopeID) != "" {
		if _, err := enforceTopologyRuntimeAccount(incoming.AccountScopeID, incoming); err != nil {
			return err
		}
		if err := ensureTopologyLocalSelfPlacementForRuntime(topology, incoming.AccountScopeID, incoming); err != nil {
			return err
		}
		_, err = topology.PutRuntimeForAccount(incoming.AccountScopeID, incoming)
		return err
	}
	_, err = topology.PutRuntime(incoming)
	return err
}

func UpsertTopologyRuntimeRecordForAccount(topology *TopologyStore, accountScopeID string, incoming TopologyRuntimeRecord) error {
	accountScopeID, err := requireTopologyAccountScopeID(accountScopeID)
	if err != nil {
		return err
	}
	if topology == nil {
		return nil
	}
	incoming = normalizeTopologyRuntimeRecord(incoming)
	if incoming.SwarmID == "" {
		return nil
	}
	existing, ok, err := topology.GetRuntimeForAccount(accountScopeID, incoming.SwarmID)
	if err != nil {
		return err
	}
	if ok {
		if err := ensureTopologyMergeSameAccount(existing.AccountScopeID, incoming.AccountScopeID); err != nil {
			return err
		}
		incoming = mergeTopologyRuntimeRecord(existing, incoming)
	}
	if _, err := enforceTopologyRuntimeAccount(accountScopeID, incoming); err != nil {
		return err
	}
	if err := ensureTopologyLocalSelfPlacementForRuntime(topology, accountScopeID, incoming); err != nil {
		return err
	}
	_, err = topology.PutRuntimeForAccount(accountScopeID, incoming)
	return err
}

func ensureTopologyLocalSelfPlacementForRuntime(topology *TopologyStore, accountScopeID string, runtime TopologyRuntimeRecord) error {
	if topology == nil {
		return nil
	}
	runtime = normalizeTopologyRuntimeRecord(runtime)
	if runtime.SwarmID == "" || !strings.EqualFold(runtime.Relationship, "self") {
		return nil
	}
	accountScopeID = strings.TrimSpace(firstNonEmpty(accountScopeID, runtime.AccountScopeID))
	if accountScopeID == "" {
		return nil
	}
	if _, ok, err := topology.GetRuntimePlacementForAccount(accountScopeID, runtime.SwarmID); err != nil || ok {
		return err
	}
	_, err := topology.PutRuntimePlacementForAccount(accountScopeID, TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       runtime.SwarmID,
		AccountScopeID:       accountScopeID,
		AuthorityHostSwarmID: runtime.SwarmID,
		RuntimeKind:          TopologyRuntimeKindHost,
	})
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

func topologyTransportFromSwarmTransports(transports []SwarmTransportRecord) string {
	for _, transport := range transports {
		kind := strings.ToLower(strings.TrimSpace(transport.Kind))
		if kind != "" {
			return kind
		}
	}
	return ""
}
