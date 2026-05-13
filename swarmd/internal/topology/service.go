package topology

import (
	"fmt"
	"sort"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const SnapshotVersion = "checkpoint1-v1"

type Service struct {
	topologyStore   *pebblestore.TopologyStore
	swarmStore      *pebblestore.SwarmStore
	swarmNodes      *pebblestore.SwarmNodeStore
	localContainers *pebblestore.SwarmLocalContainerStore
	deployments     *pebblestore.DeployContainerStore
	remoteDeploys   *pebblestore.RemoteDeploySessionStore
	sessionRoutes   *pebblestore.SessionRouteStore
	workspaceStore  *pebblestore.WorkspaceStore
}

func NewService(
	topologyStore *pebblestore.TopologyStore,
	swarmStore *pebblestore.SwarmStore,
	swarmNodes *pebblestore.SwarmNodeStore,
	localContainers *pebblestore.SwarmLocalContainerStore,
	deployments *pebblestore.DeployContainerStore,
	remoteDeploys *pebblestore.RemoteDeploySessionStore,
	sessionRoutes *pebblestore.SessionRouteStore,
	workspaceStore *pebblestore.WorkspaceStore,
) *Service {
	return &Service{
		topologyStore:   topologyStore,
		swarmStore:      swarmStore,
		swarmNodes:      swarmNodes,
		localContainers: localContainers,
		deployments:     deployments,
		remoteDeploys:   remoteDeploys,
		sessionRoutes:   sessionRoutes,
		workspaceStore:  workspaceStore,
	}
}

func (s *Service) Rebuild() (pebblestore.TopologyMigrationStatusRecord, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyMigrationStatusRecord{}, fmt.Errorf("topology service is not configured")
	}
	snapshot, err := s.buildSnapshot()
	if err != nil {
		return pebblestore.TopologyMigrationStatusRecord{}, err
	}
	if err := s.topologyStore.ReplaceSnapshot(snapshot); err != nil {
		return pebblestore.TopologyMigrationStatusRecord{}, err
	}
	return snapshot.MigrationStatus, nil
}

func (s *Service) Snapshot() (pebblestore.TopologySnapshot, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologySnapshot{}, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.Snapshot()
}

func (s *Service) ListRuntimes(limit int) ([]pebblestore.TopologyRuntimeRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListRuntimes(limit)
}

func (s *Service) ListHostContainersByHost(hostSwarmID string, limit int) ([]pebblestore.TopologyHostContainerRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListHostContainersByHost(hostSwarmID, limit)
}

func (s *Service) ResolveRuntimeHostContainer(runtimeSwarmID string) (pebblestore.TopologyHostContainerRecord, pebblestore.TopologyAttachmentRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologyHostContainerRecord{}, pebblestore.TopologyAttachmentRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	attachment, ok, err := s.topologyStore.FindAttachmentByRuntime(runtimeSwarmID)
	if err != nil || !ok {
		return pebblestore.TopologyHostContainerRecord{}, attachment, ok, err
	}
	hostContainer, ok, err := s.topologyStore.GetHostContainer(attachment.HostContainerID)
	if err != nil || !ok {
		return pebblestore.TopologyHostContainerRecord{}, attachment, ok, err
	}
	return hostContainer, attachment, true, nil
}

func (s *Service) ListWorkspaceBindingsBySourcePath(sourceWorkspacePath string, limit int) ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.topologyStore == nil {
		return nil, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.ListWorkspaceBindingsBySourcePath(sourceWorkspacePath, limit)
}

func (s *Service) GetSessionRoute(sessionID string) (pebblestore.TopologySessionRouteRecord, bool, error) {
	if s == nil || s.topologyStore == nil {
		return pebblestore.TopologySessionRouteRecord{}, false, fmt.Errorf("topology service is not configured")
	}
	return s.topologyStore.GetSessionRoute(sessionID)
}

func (s *Service) buildSnapshot() (pebblestore.TopologySnapshot, error) {
	now := time.Now().UnixMilli()
	localSwarmID, localNode, _ := s.loadLocalNode()
	groupIDsBySwarm, err := s.loadGroupIDsBySwarm()
	if err != nil {
		return pebblestore.TopologySnapshot{}, err
	}

	runtimeMap := map[string]pebblestore.TopologyRuntimeRecord{}
	hostContainerMap := map[string]pebblestore.TopologyHostContainerRecord{}
	attachmentMap := map[string]pebblestore.TopologyAttachmentRecord{}

	if strings.TrimSpace(localNode.SwarmID) != "" {
		mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
			SwarmID:         localNode.SwarmID,
			Name:            firstNonEmpty(localNode.Name, localNode.SwarmID),
			Role:            localNode.Role,
			Relationship:    "self",
			Status:          "online",
			ObservedSources: []string{"swarm_local_node"},
			CreatedAt:       now,
			UpdatedAt:       now,
		})
	}

	if s.swarmStore != nil {
		trustedPeers, err := s.swarmStore.ListTrustedPeers(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, peer := range trustedPeers {
			mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
				SwarmID:         peer.SwarmID,
				Name:            firstNonEmpty(peer.Name, peer.SwarmID),
				Role:            peer.Role,
				Relationship:    peer.Relationship,
				Transport:       peer.TransportMode,
				Status:          "trusted",
				ObservedSources: []string{"swarm_trusted_peer"},
				CreatedAt:       peer.CreatedAt,
				UpdatedAt:       peer.UpdatedAt,
			})
		}
	}

	if s.swarmNodes != nil {
		nodes, err := s.swarmNodes.List(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, node := range nodes {
			mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
				SwarmID:         node.SwarmID,
				Name:            firstNonEmpty(node.Name, node.SwarmID),
				Role:            node.Role,
				Relationship:    normalizeNodeRelationship(node.Role),
				BackendURL:      node.BackendURL,
				DesktopURL:      node.DesktopURL,
				Status:          node.Status,
				Transport:       node.Transport,
				ObservedSources: []string{"swarm_node"},
				CreatedAt:       node.CreatedAt,
				UpdatedAt:       node.UpdatedAt,
			})
		}
	}

	if s.localContainers != nil {
		localContainers, err := s.localContainers.List(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, container := range localContainers {
			hostSwarmID := firstNonEmpty(localSwarmID, "local")
			runtimeContainerRef := firstNonEmpty(container.ContainerID, container.ContainerName, container.ID)
			hostContainerID := canonicalHostContainerID(hostSwarmID, runtimeContainerRef)
			mergeHostContainer(hostContainerMap, pebblestore.TopologyHostContainerRecord{
				HostContainerID:    hostContainerID,
				HostSwarmID:        hostSwarmID,
				RuntimeContainerRef: runtimeContainerRef,
				Name:               firstNonEmpty(container.Name, container.ContainerName, container.ID),
				ContainerName:      firstNonEmpty(container.ContainerName, container.ID),
				ContainerID:        container.ContainerID,
				Runtime:            container.Runtime,
				Image:              container.Image,
				Status:             container.Status,
				HostAPIBaseURL:     container.HostAPIBaseURL,
				HostPort:           container.HostPort,
				RuntimePort:        container.RuntimePort,
				Mounts:             container.Mounts,
				ObservedSources:    []string{"swarm_local_container"},
				CreatedAt:          container.CreatedAt,
				UpdatedAt:          container.UpdatedAt,
			})
		}
	}

	if s.deployments != nil {
		deployments, err := s.deployments.List(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, deployment := range deployments {
			hostSwarmID := firstNonEmpty(strings.TrimSpace(deployment.HostSwarmID), localSwarmID, "local")
			runtimeContainerRef := firstNonEmpty(strings.TrimSpace(deployment.ContainerID), strings.TrimSpace(deployment.ContainerName), strings.TrimSpace(deployment.ID))
			hostContainerID := canonicalHostContainerID(hostSwarmID, runtimeContainerRef)
			mergeHostContainer(hostContainerMap, pebblestore.TopologyHostContainerRecord{
				HostContainerID:    hostContainerID,
				HostSwarmID:        hostSwarmID,
				RuntimeContainerRef: runtimeContainerRef,
				Name:               firstNonEmpty(deployment.Name, deployment.ContainerName, deployment.ID),
				ContainerName:      firstNonEmpty(deployment.ContainerName, deployment.Name, deployment.ID),
				ContainerID:        deployment.ContainerID,
				Runtime:            deployment.Runtime,
				Image:              deployment.Image,
				Status:             firstNonEmpty(deployment.AttachStatus, deployment.Status),
				HostAPIBaseURL:     firstNonEmpty(deployment.HostAPIBaseURL, deployment.HostBackendURL),
				HostPort:           deployment.BackendHostPort,
				RuntimePort:        deployment.DesktopHostPort,
				ObservedSources:    []string{"deploy_container"},
				CreatedAt:          deployment.CreatedAt,
				UpdatedAt:          deployment.UpdatedAt,
			})
			if strings.TrimSpace(deployment.ChildSwarmID) != "" {
				mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
					SwarmID:              deployment.ChildSwarmID,
					Name:                 firstNonEmpty(deployment.ChildDisplayName, deployment.Name, deployment.ChildSwarmID),
					Role:                 "child",
					Relationship:         "child",
					BackendURL:           deployment.ChildBackendURL,
					DesktopURL:           deployment.ChildDesktopURL,
					Status:               firstNonEmpty(deployment.AttachStatus, deployment.Status),
					OwnerHostSwarmID:     hostSwarmID,
					OwnerHostContainerID: hostContainerID,
					ObservedSources:      []string{"deploy_container"},
					CreatedAt:            deployment.CreatedAt,
					UpdatedAt:            deployment.UpdatedAt,
				})
				attachmentID := canonicalAttachmentID(hostContainerID, deployment.ChildSwarmID)
				mergeAttachment(attachmentMap, pebblestore.TopologyAttachmentRecord{
					AttachmentID:    attachmentID,
					HostContainerID: hostContainerID,
					RuntimeSwarmID:  deployment.ChildSwarmID,
					State:           firstNonEmpty(deployment.AttachStatus, deployment.Status),
					DeploymentID:    deployment.ID,
					CreatedAt:       deployment.CreatedAt,
					UpdatedAt:       deployment.UpdatedAt,
				})
			}
		}
	}

	if s.remoteDeploys != nil {
		sessions, err := s.remoteDeploys.List(100000)
		if err != nil {
			return pebblestore.TopologySnapshot{}, err
		}
		for _, session := range sessions {
			if strings.TrimSpace(session.ChildSwarmID) != "" {
				mergeRuntime(runtimeMap, pebblestore.TopologyRuntimeRecord{
					SwarmID:         session.ChildSwarmID,
					Name:            firstNonEmpty(session.ChildName, session.Name, session.ChildSwarmID),
					Role:            "child",
					Relationship:    "child",
					BackendURL:      session.RemoteEndpoint,
					DesktopURL:      session.HostDesktopURL,
					Status:          session.Status,
					Transport:       session.TransportMode,
					ObservedSources: []string{"remote_deploy_session"},
					CreatedAt:       session.CreatedAt,
					UpdatedAt:       session.UpdatedAt,
				})
			}
		}
	}

	workspaceBindings, err := s.buildWorkspaceBindings()
	if err != nil {
		return pebblestore.TopologySnapshot{}, err
	}
	sessionRouteRecords, err := s.buildSessionRoutes(workspaceBindings)
	if err != nil {
		return pebblestore.TopologySnapshot{}, err
	}

	runtimes := runtimeMapValues(runtimeMap)
	for i := range runtimes {
		runtimes[i].GroupIDs = normalizeGroupIDs(groupIDsBySwarm[runtimes[i].SwarmID])
	}
	hostContainers := hostContainerMapValues(hostContainerMap)
	attachments := attachmentMapValues(attachmentMap)
	sortTopologyRuntimes(runtimes)
	sortTopologyHostContainers(hostContainers)
	sortTopologyAttachments(attachments)
	sortTopologyWorkspaceBindings(workspaceBindings)
	sortTopologySessionRoutes(sessionRouteRecords)

	migrationStatus := pebblestore.TopologyMigrationStatusRecord{
		ID:                   pebblestore.DefaultTopologyMigrationStatusID,
		Version:              SnapshotVersion,
		RebuiltAt:            now,
		RuntimeCount:         len(runtimes),
		HostContainerCount:   len(hostContainers),
		AttachmentCount:      len(attachments),
		WorkspaceBindingCount: len(workspaceBindings),
		SessionRouteCount:    len(sessionRouteRecords),
	}
	return pebblestore.TopologySnapshot{
		Runtimes:          runtimes,
		HostContainers:    hostContainers,
		Attachments:       attachments,
		WorkspaceBindings: workspaceBindings,
		SessionRoutes:     sessionRouteRecords,
		MigrationStatus:   migrationStatus,
	}, nil
}

func (s *Service) loadLocalNode() (string, pebblestore.SwarmLocalNodeRecord, error) {
	if s == nil || s.swarmStore == nil {
		return "", pebblestore.SwarmLocalNodeRecord{}, nil
	}
	record, ok, err := s.swarmStore.GetLocalNode()
	if err != nil || !ok {
		return strings.TrimSpace(record.SwarmID), record, err
	}
	return strings.TrimSpace(record.SwarmID), record, nil
}

func (s *Service) loadGroupIDsBySwarm() (map[string][]string, error) {
	out := map[string][]string{}
	if s == nil || s.swarmStore == nil {
		return out, nil
	}
	groups, err := s.swarmStore.ListGroups(100000)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		members, err := s.swarmStore.ListGroupMemberships(group.ID, 100000)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			swarmID := strings.TrimSpace(member.SwarmID)
			groupID := strings.TrimSpace(group.ID)
			if swarmID == "" || groupID == "" {
				continue
			}
			out[swarmID] = append(out[swarmID], groupID)
		}
	}
	for swarmID, groupIDs := range out {
		out[swarmID] = normalizeGroupIDs(groupIDs)
	}
	return out, nil
}

func (s *Service) buildWorkspaceBindings() ([]pebblestore.TopologyWorkspaceBindingRecord, error) {
	if s == nil || s.workspaceStore == nil {
		return nil, nil
	}
	entries, err := s.workspaceStore.List(100000)
	if err != nil {
		return nil, err
	}
	out := make([]pebblestore.TopologyWorkspaceBindingRecord, 0)
	for _, entry := range entries {
		for _, link := range entry.ReplicationLinks {
			bindingID := strings.TrimSpace(link.ID)
			targetSwarmID := strings.TrimSpace(link.TargetSwarmID)
			targetWorkspacePath := strings.TrimSpace(link.TargetWorkspacePath)
			targetKind := strings.TrimSpace(link.TargetKind)
			if bindingID == "" {
				bindingID = canonicalWorkspaceBindingID(entry.Path, targetSwarmID, targetWorkspacePath, targetKind)
			}
			hostSwarmID := ""
			if strings.EqualFold(targetKind, "managed_host") || strings.EqualFold(targetKind, "host") {
				hostSwarmID = targetSwarmID
			}
			out = append(out, pebblestore.TopologyWorkspaceBindingRecord{
				BindingID:                 bindingID,
				SourceWorkspacePath:       strings.TrimSpace(entry.Path),
				SourceWorkspaceName:       strings.TrimSpace(entry.Name),
				DestinationRuntimeSwarmID: targetSwarmID,
				DestinationHostSwarmID:    hostSwarmID,
				DestinationWorkspacePath:  targetWorkspacePath,
				ReplicationMode:           strings.TrimSpace(link.ReplicationMode),
				Writable:                  link.Writable,
				Sync:                      link.Sync,
				LegacyTargetKind:          targetKind,
				CreatedAt:                 link.CreatedAt,
				UpdatedAt:                 link.UpdatedAt,
			})
		}
	}
	return out, nil
}

func (s *Service) buildSessionRoutes(bindings []pebblestore.TopologyWorkspaceBindingRecord) ([]pebblestore.TopologySessionRouteRecord, error) {
	if s == nil || s.sessionRoutes == nil {
		return nil, nil
	}
	routes, err := s.sessionRoutes.List(100000)
	if err != nil {
		return nil, err
	}
	out := make([]pebblestore.TopologySessionRouteRecord, 0, len(routes))
	for _, route := range routes {
		out = append(out, pebblestore.TopologySessionRouteRecord{
			SessionID:            strings.TrimSpace(route.SessionID),
			RuntimeSwarmID:       strings.TrimSpace(route.ChildSwarmID),
			WorkspaceBindingID:   matchingWorkspaceBindingID(bindings, route),
			BackendURL:           strings.TrimSpace(route.ChildBackendURL),
			HostWorkspacePath:    strings.TrimSpace(route.HostWorkspacePath),
			RuntimeWorkspacePath: strings.TrimSpace(route.RuntimeWorkspacePath),
			CreatedAt:            route.CreatedAt,
			UpdatedAt:            route.UpdatedAt,
		})
	}
	return out, nil
}

func matchingWorkspaceBindingID(bindings []pebblestore.TopologyWorkspaceBindingRecord, route pebblestore.SessionRouteRecord) string {
	childSwarmID := strings.TrimSpace(route.ChildSwarmID)
	hostWorkspacePath := strings.TrimSpace(route.HostWorkspacePath)
	runtimeWorkspacePath := strings.TrimSpace(route.RuntimeWorkspacePath)
	for _, binding := range bindings {
		if childSwarmID != "" && !strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), childSwarmID) {
			continue
		}
		if hostWorkspacePath != "" && !strings.EqualFold(strings.TrimSpace(binding.SourceWorkspacePath), hostWorkspacePath) {
			continue
		}
		if runtimeWorkspacePath != "" && !strings.EqualFold(strings.TrimSpace(binding.DestinationWorkspacePath), runtimeWorkspacePath) {
			continue
		}
		return strings.TrimSpace(binding.BindingID)
	}
	return ""
}

func mergeRuntime(dst map[string]pebblestore.TopologyRuntimeRecord, incoming pebblestore.TopologyRuntimeRecord) {
	incoming = normalizeRuntime(incoming)
	if incoming.SwarmID == "" {
		return
	}
	existing, ok := dst[incoming.SwarmID]
	if !ok {
		dst[incoming.SwarmID] = incoming
		return
	}
	existing.Name = firstNonEmpty(existing.Name, incoming.Name, incoming.SwarmID)
	existing.Role = firstNonEmpty(existing.Role, incoming.Role)
	existing.Relationship = chooseRelationship(existing.Relationship, incoming.Relationship)
	existing.BackendURL = firstNonEmpty(existing.BackendURL, incoming.BackendURL)
	existing.DesktopURL = firstNonEmpty(existing.DesktopURL, incoming.DesktopURL)
	existing.Status = firstNonEmpty(existing.Status, incoming.Status)
	existing.Transport = firstNonEmpty(existing.Transport, incoming.Transport)
	existing.OwnerHostSwarmID = firstNonEmpty(existing.OwnerHostSwarmID, incoming.OwnerHostSwarmID)
	existing.OwnerHostContainerID = firstNonEmpty(existing.OwnerHostContainerID, incoming.OwnerHostContainerID)
	existing.GroupIDs = appendDedup(existing.GroupIDs, incoming.GroupIDs...)
	existing.ObservedSources = appendDedup(existing.ObservedSources, incoming.ObservedSources...)
	existing.CreatedAt = minPositive(existing.CreatedAt, incoming.CreatedAt)
	existing.UpdatedAt = maxInt64(existing.UpdatedAt, incoming.UpdatedAt)
	dst[incoming.SwarmID] = existing
}

func mergeHostContainer(dst map[string]pebblestore.TopologyHostContainerRecord, incoming pebblestore.TopologyHostContainerRecord) {
	incoming = normalizeHostContainer(incoming)
	if incoming.HostContainerID == "" {
		return
	}
	existing, ok := dst[incoming.HostContainerID]
	if !ok {
		dst[incoming.HostContainerID] = incoming
		return
	}
	existing.HostSwarmID = firstNonEmpty(existing.HostSwarmID, incoming.HostSwarmID)
	existing.RuntimeContainerRef = firstNonEmpty(existing.RuntimeContainerRef, incoming.RuntimeContainerRef)
	existing.Name = firstNonEmpty(existing.Name, incoming.Name, incoming.HostContainerID)
	existing.ContainerName = firstNonEmpty(existing.ContainerName, incoming.ContainerName)
	existing.ContainerID = firstNonEmpty(existing.ContainerID, incoming.ContainerID)
	existing.Runtime = firstNonEmpty(existing.Runtime, incoming.Runtime)
	existing.Image = firstNonEmpty(existing.Image, incoming.Image)
	existing.Status = firstNonEmpty(existing.Status, incoming.Status)
	existing.HostAPIBaseURL = firstNonEmpty(existing.HostAPIBaseURL, incoming.HostAPIBaseURL)
	existing.HostPort = maxInt(existing.HostPort, incoming.HostPort)
	existing.RuntimePort = maxInt(existing.RuntimePort, incoming.RuntimePort)
	if len(existing.Mounts) == 0 {
		existing.Mounts = incoming.Mounts
	}
	existing.ObservedSources = appendDedup(existing.ObservedSources, incoming.ObservedSources...)
	existing.CreatedAt = minPositive(existing.CreatedAt, incoming.CreatedAt)
	existing.UpdatedAt = maxInt64(existing.UpdatedAt, incoming.UpdatedAt)
	dst[incoming.HostContainerID] = existing
}

func mergeAttachment(dst map[string]pebblestore.TopologyAttachmentRecord, incoming pebblestore.TopologyAttachmentRecord) {
	incoming = normalizeAttachment(incoming)
	if incoming.AttachmentID == "" {
		return
	}
	existing, ok := dst[incoming.AttachmentID]
	if !ok {
		dst[incoming.AttachmentID] = incoming
		return
	}
	existing.HostContainerID = firstNonEmpty(existing.HostContainerID, incoming.HostContainerID)
	existing.RuntimeSwarmID = firstNonEmpty(existing.RuntimeSwarmID, incoming.RuntimeSwarmID)
	existing.State = firstNonEmpty(existing.State, incoming.State)
	existing.DeploymentID = firstNonEmpty(existing.DeploymentID, incoming.DeploymentID)
	existing.RemoteDeploySessionID = firstNonEmpty(existing.RemoteDeploySessionID, incoming.RemoteDeploySessionID)
	existing.LastError = firstNonEmpty(existing.LastError, incoming.LastError)
	existing.CreatedAt = minPositive(existing.CreatedAt, incoming.CreatedAt)
	existing.UpdatedAt = maxInt64(existing.UpdatedAt, incoming.UpdatedAt)
	dst[incoming.AttachmentID] = existing
}

func normalizeRuntime(record pebblestore.TopologyRuntimeRecord) pebblestore.TopologyRuntimeRecord {
	record.Name = firstNonEmpty(strings.TrimSpace(record.Name), strings.TrimSpace(record.SwarmID))
	record.Role = strings.ToLower(strings.TrimSpace(record.Role))
	record.Relationship = strings.ToLower(strings.TrimSpace(record.Relationship))
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	record.Transport = strings.ToLower(strings.TrimSpace(record.Transport))
	record.BackendURL = strings.TrimSpace(record.BackendURL)
	record.DesktopURL = strings.TrimSpace(record.DesktopURL)
	record.OwnerHostSwarmID = strings.TrimSpace(record.OwnerHostSwarmID)
	record.OwnerHostContainerID = strings.TrimSpace(record.OwnerHostContainerID)
	record.ObservedSources = appendDedup(nil, record.ObservedSources...)
	if record.CreatedAt <= 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = record.CreatedAt
	}
	return record
}

func normalizeHostContainer(record pebblestore.TopologyHostContainerRecord) pebblestore.TopologyHostContainerRecord {
	record.HostSwarmID = strings.TrimSpace(record.HostSwarmID)
	record.RuntimeContainerRef = strings.TrimSpace(record.RuntimeContainerRef)
	record.Name = firstNonEmpty(strings.TrimSpace(record.Name), strings.TrimSpace(record.ContainerName), strings.TrimSpace(record.ContainerID), strings.TrimSpace(record.HostContainerID))
	record.ContainerName = strings.TrimSpace(record.ContainerName)
	record.ContainerID = strings.TrimSpace(record.ContainerID)
	record.Runtime = strings.ToLower(strings.TrimSpace(record.Runtime))
	record.Image = strings.TrimSpace(record.Image)
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	record.HostAPIBaseURL = strings.TrimSpace(record.HostAPIBaseURL)
	record.ObservedSources = appendDedup(nil, record.ObservedSources...)
	if record.CreatedAt <= 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = record.CreatedAt
	}
	return record
}

func normalizeAttachment(record pebblestore.TopologyAttachmentRecord) pebblestore.TopologyAttachmentRecord {
	record.HostContainerID = strings.TrimSpace(record.HostContainerID)
	record.RuntimeSwarmID = strings.TrimSpace(record.RuntimeSwarmID)
	record.State = strings.ToLower(strings.TrimSpace(record.State))
	record.DeploymentID = strings.TrimSpace(record.DeploymentID)
	record.RemoteDeploySessionID = strings.TrimSpace(record.RemoteDeploySessionID)
	record.LastError = strings.TrimSpace(record.LastError)
	if record.CreatedAt <= 0 {
		record.CreatedAt = time.Now().UnixMilli()
	}
	if record.UpdatedAt <= 0 {
		record.UpdatedAt = record.CreatedAt
	}
	return record
}

func normalizeGroupIDs(groupIDs []string) []string {
	return appendDedup(nil, groupIDs...)
}

func appendDedup(existing []string, values ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(existing)+len(values))
	for _, raw := range existing {
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
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func minPositive(values ...int64) int64 {
	var out int64
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if out == 0 || value < out {
			out = value
		}
	}
	return out
}

func maxInt64(values ...int64) int64 {
	var out int64
	for _, value := range values {
		if value > out {
			out = value
		}
	}
	return out
}

func maxInt(values ...int) int {
	out := 0
	for _, value := range values {
		if value > out {
			out = value
		}
	}
	return out
}

func chooseRelationship(existing, incoming string) string {
	existing = strings.ToLower(strings.TrimSpace(existing))
	incoming = strings.ToLower(strings.TrimSpace(incoming))
	if existing == "self" || incoming == "self" {
		if existing == "self" {
			return existing
		}
		return incoming
	}
	if existing != "" {
		return existing
	}
	return incoming
}

func normalizeNodeRelationship(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "managed", "manager", "child", "self":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

func canonicalHostContainerID(hostSwarmID, runtimeContainerRef string) string {
	hostSwarmID = strings.TrimSpace(hostSwarmID)
	runtimeContainerRef = strings.TrimSpace(runtimeContainerRef)
	if hostSwarmID == "" || runtimeContainerRef == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", hostSwarmID, strings.ToLower(runtimeContainerRef))
}

func canonicalAttachmentID(hostContainerID, runtimeSwarmID string) string {
	hostContainerID = strings.TrimSpace(hostContainerID)
	runtimeSwarmID = strings.TrimSpace(runtimeSwarmID)
	if hostContainerID == "" || runtimeSwarmID == "" {
		return ""
	}
	return fmt.Sprintf("%s=>%s", hostContainerID, runtimeSwarmID)
}

func canonicalWorkspaceBindingID(sourceWorkspacePath, targetSwarmID, targetWorkspacePath, targetKind string) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(sourceWorkspacePath)),
		strings.ToLower(strings.TrimSpace(targetSwarmID)),
		strings.ToLower(strings.TrimSpace(targetWorkspacePath)),
		strings.ToLower(strings.TrimSpace(targetKind)),
	}
	return strings.Join(parts, "|")
}

func sortTopologyRuntimes(records []pebblestore.TopologyRuntimeRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].SwarmID) < strings.ToLower(records[j].SwarmID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologyHostContainers(records []pebblestore.TopologyHostContainerRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].HostContainerID) < strings.ToLower(records[j].HostContainerID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologyAttachments(records []pebblestore.TopologyAttachmentRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].AttachmentID) < strings.ToLower(records[j].AttachmentID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologyWorkspaceBindings(records []pebblestore.TopologyWorkspaceBindingRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].BindingID) < strings.ToLower(records[j].BindingID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func sortTopologySessionRoutes(records []pebblestore.TopologySessionRouteRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt == records[j].UpdatedAt {
			return strings.ToLower(records[i].SessionID) < strings.ToLower(records[j].SessionID)
		}
		return records[i].UpdatedAt > records[j].UpdatedAt
	})
}

func runtimeMapValues(input map[string]pebblestore.TopologyRuntimeRecord) []pebblestore.TopologyRuntimeRecord {
	out := make([]pebblestore.TopologyRuntimeRecord, 0, len(input))
	for _, value := range input {
		out = append(out, value)
	}
	return out
}

func hostContainerMapValues(input map[string]pebblestore.TopologyHostContainerRecord) []pebblestore.TopologyHostContainerRecord {
	out := make([]pebblestore.TopologyHostContainerRecord, 0, len(input))
	for _, value := range input {
		out = append(out, value)
	}
	return out
}

func attachmentMapValues(input map[string]pebblestore.TopologyAttachmentRecord) []pebblestore.TopologyAttachmentRecord {
	out := make([]pebblestore.TopologyAttachmentRecord, 0, len(input))
	for _, value := range input {
		out = append(out, value)
	}
	return out
}
