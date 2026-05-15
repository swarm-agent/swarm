package deploy

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) syncCanonicalDeploymentState(record pebblestore.DeployContainerRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.SyncOwnerSwarmID))
	if hostSwarmID == "" && s.swarmStore != nil {
		if localNode, ok, err := s.swarmStore.GetLocalNode(); err != nil {
			return err
		} else if ok {
			hostSwarmID = strings.TrimSpace(localNode.SwarmID)
		}
	}
	runtimeContainerRef := firstNonEmpty(strings.TrimSpace(record.ContainerID), strings.TrimSpace(record.ContainerName), strings.TrimSpace(record.ID))
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, runtimeContainerRef)
	if hostContainerID == "" {
		return nil
	}
	if err := pebblestore.UpsertTopologyHostContainer(s.topology, pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
		HostSwarmID:         hostSwarmID,
		RuntimeContainerRef: runtimeContainerRef,
		Name:                firstNonEmpty(record.Name, record.ContainerName, record.ID),
		ContainerName:       firstNonEmpty(record.ContainerName, record.Name, record.ID),
		ContainerID:         strings.TrimSpace(record.ContainerID),
		Runtime:             strings.TrimSpace(record.Runtime),
		Image:               strings.TrimSpace(record.Image),
		Status:              firstNonEmpty(record.AttachStatus, record.Status),
		HostAPIBaseURL:      firstNonEmpty(record.HostAPIBaseURL, record.HostBackendURL),
		HostPort:            record.BackendHostPort,
		RuntimePort:         record.DesktopHostPort,
		ObservedSources:     []string{pebblestore.TopologyHostContainerSourceDeployContainer},
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}); err != nil {
		return err
	}

	childSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if childSwarmID == "" {
		return nil
	}
	if err := pebblestore.UpsertTopologyRuntimeRecord(s.topology, pebblestore.TopologyRuntimeRecord{
		SwarmID:              childSwarmID,
		Name:                 firstNonEmpty(record.ChildDisplayName, record.Name, childSwarmID),
		Role:                 "child",
		Relationship:         "child",
		BackendURL:           strings.TrimSpace(record.ChildBackendURL),
		DesktopURL:           strings.TrimSpace(record.ChildDesktopURL),
		Status:               firstNonEmpty(strings.TrimSpace(record.AttachStatus), strings.TrimSpace(record.Status)),
		OwnerHostSwarmID:     hostSwarmID,
		OwnerHostContainerID: hostContainerID,
		ObservedSources:      []string{pebblestore.TopologyRuntimeSourceDeployContainer},
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}); err != nil {
		return err
	}
	attachmentID := pebblestore.CanonicalTopologyAttachmentID(hostContainerID, childSwarmID)
	return pebblestore.UpsertTopologyAttachment(s.topology, pebblestore.TopologyAttachmentRecord{
		AttachmentID:    attachmentID,
		HostContainerID: hostContainerID,
		RuntimeSwarmID:  childSwarmID,
		State:           firstNonEmpty(strings.TrimSpace(record.AttachStatus), strings.TrimSpace(record.Status)),
		DeploymentID:    strings.TrimSpace(record.ID),
		LastError:       strings.TrimSpace(record.LastAttachError),
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	})
}

func (s *Service) deleteCanonicalDeploymentState(record pebblestore.DeployContainerRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.SyncOwnerSwarmID))
	hostContainer, ok, err := pebblestore.FindTopologyHostContainerByRefs(s.topology, hostSwarmID, record.ContainerID, record.ContainerName, record.ID)
	if err != nil {
		return err
	}
	childSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if ok {
		attachmentRecords, err := s.topology.ListAttachmentsByHostContainer(hostContainer.HostContainerID, 500)
		if err != nil {
			return err
		}
		for _, attachmentRecord := range attachmentRecords {
			if strings.TrimSpace(attachmentRecord.DeploymentID) == strings.TrimSpace(record.ID) {
				if err := s.topology.DeleteAttachment(attachmentRecord.AttachmentID); err != nil {
					return err
				}
				if childSwarmID == "" {
					childSwarmID = strings.TrimSpace(attachmentRecord.RuntimeSwarmID)
				}
			}
		}
		if err := s.topology.DeleteHostContainer(hostContainer.HostContainerID); err != nil {
			return err
		}
	}
	if childSwarmID != "" {
		if err := s.deleteCanonicalWorkspaceRoutesForRuntime(childSwarmID); err != nil {
			return err
		}
		if err := pebblestore.RemoveTopologyRuntimeObservedSource(s.topology, childSwarmID, pebblestore.TopologyRuntimeSourceDeployContainer); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deleteCanonicalWorkspaceRoutesForRuntime(runtimeSwarmID string) error {
	if s == nil || s.topology == nil {
		return nil
	}
	runtimeSwarmID = strings.TrimSpace(runtimeSwarmID)
	if runtimeSwarmID == "" {
		return nil
	}
	bindings, err := s.topology.ListWorkspaceBindings(100000)
	if err != nil {
		return err
	}
	routes, err := s.topology.ListSessionRoutes(100000)
	if err != nil {
		return err
	}
	bindingIDs := make(map[string]struct{})
	for _, binding := range bindings {
		if !strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), runtimeSwarmID) {
			continue
		}
		bindingID := strings.TrimSpace(binding.BindingID)
		if bindingID != "" {
			bindingIDs[strings.ToLower(bindingID)] = struct{}{}
		}
	}
	deletedRoutes := make(map[string]struct{})
	for _, route := range routes {
		sessionID := strings.TrimSpace(route.SessionID)
		if sessionID == "" {
			continue
		}
		_, bindingMatches := bindingIDs[strings.ToLower(strings.TrimSpace(route.WorkspaceBindingID))]
		if !bindingMatches && !strings.EqualFold(strings.TrimSpace(route.RuntimeSwarmID), runtimeSwarmID) {
			continue
		}
		if _, seen := deletedRoutes[strings.ToLower(sessionID)]; seen {
			continue
		}
		if err := s.topology.DeleteSessionRoute(sessionID); err != nil {
			return err
		}
		deletedRoutes[strings.ToLower(sessionID)] = struct{}{}
	}
	for _, binding := range bindings {
		if !strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), runtimeSwarmID) {
			continue
		}
		bindingID := strings.TrimSpace(binding.BindingID)
		if bindingID == "" {
			continue
		}
		if err := s.topology.DeleteWorkspaceBinding(bindingID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) childAttachmentRecordsForDeployment(record pebblestore.DeployContainerRecord) ([]pebblestore.TopologyAttachmentRecord, error) {
	if s == nil || s.topology == nil {
		return nil, nil
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.SyncOwnerSwarmID))
	hostContainer, ok, err := pebblestore.FindTopologyHostContainerByRefs(s.topology, hostSwarmID, record.ContainerID, record.ContainerName, record.ID)
	if err != nil || !ok {
		return nil, err
	}
	attachments, err := s.topology.ListAttachmentsByHostContainer(hostContainer.HostContainerID, 500)
	if err != nil {
		return nil, err
	}
	out := make([]pebblestore.TopologyAttachmentRecord, 0, len(attachments))
	for _, attachment := range attachments {
		if strings.TrimSpace(attachment.DeploymentID) == strings.TrimSpace(record.ID) {
			out = append(out, attachment)
		}
	}
	return out, nil
}

func (s *Service) canonicalHostContainerIDForDeployment(record pebblestore.DeployContainerRecord) (string, error) {
	if s == nil || s.topology == nil {
		return "", nil
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.SyncOwnerSwarmID))
	hostContainer, ok, err := pebblestore.FindTopologyHostContainerByRefs(s.topology, hostSwarmID, record.ContainerID, record.ContainerName, record.ID)
	if err != nil || !ok {
		return "", err
	}
	return strings.TrimSpace(hostContainer.HostContainerID), nil
}

func (s *Service) childSwarmIDsFromCanonicalAttachments(record pebblestore.DeployContainerRecord) ([]string, error) {
	attachments, err := s.childAttachmentRecordsForDeployment(record)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		swarmID := strings.TrimSpace(attachment.RuntimeSwarmID)
		if swarmID == "" {
			continue
		}
		if _, ok := seen[swarmID]; ok {
			continue
		}
		seen[swarmID] = struct{}{}
		out = append(out, swarmID)
	}
	if len(out) == 0 && strings.TrimSpace(record.ChildSwarmID) != "" {
		out = append(out, strings.TrimSpace(record.ChildSwarmID))
	}
	return out, nil
}

func requireCanonicalTopologyStore(topology *pebblestore.TopologyStore) error {
	if topology == nil {
		return fmt.Errorf("topology store is not configured")
	}
	return nil
}
