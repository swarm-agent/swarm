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
	accountScopeID := strings.TrimSpace(record.AccountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("deploy topology sync requires account scope id")
	}
	userID := strings.TrimSpace(record.UserID)
	if userID == "" {
		return fmt.Errorf("deploy topology sync requires user id")
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
	hostContainerID := firstNonEmpty(strings.TrimSpace(record.HostContainerID), pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, runtimeContainerRef))
	if hostContainerID == "" {
		return nil
	}
	if err := pebblestore.UpsertTopologyHostContainerForAccount(s.topology, accountScopeID, pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
		UserID:              userID,
		AccountScopeID:      accountScopeID,
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
	if hostSwarmID == "" {
		return fmt.Errorf("deploy topology sync requires host swarm id for child placement")
	}
	if _, err := s.topology.PutRuntimePlacementForAccount(accountScopeID, pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       childSwarmID,
		AccountScopeID:       accountScopeID,
		AuthorityHostSwarmID: hostSwarmID,
		AuthorityContainerID: hostContainerID,
		RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}); err != nil {
		return err
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(s.topology, accountScopeID, pebblestore.TopologyRuntimeRecord{
		SwarmID:              childSwarmID,
		UserID:               userID,
		AccountScopeID:       accountScopeID,
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
	if err := pebblestore.UpsertTopologyAttachmentForAccount(s.topology, accountScopeID, pebblestore.TopologyAttachmentRecord{
		AttachmentID:    attachmentID,
		UserID:          userID,
		AccountScopeID:  accountScopeID,
		HostContainerID: hostContainerID,
		RuntimeSwarmID:  childSwarmID,
		State:           firstNonEmpty(strings.TrimSpace(record.AttachStatus), strings.TrimSpace(record.Status)),
		DeploymentID:    strings.TrimSpace(record.ID),
		LastError:       strings.TrimSpace(record.LastAttachError),
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}); err != nil {
		return err
	}
	return s.syncCanonicalWorkspaceBindings(record)
}

func (s *Service) syncCanonicalWorkspaceBindings(record pebblestore.DeployContainerRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	accountScopeID := strings.TrimSpace(record.AccountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("deploy topology workspace sync requires account scope id")
	}
	userID := strings.TrimSpace(record.UserID)
	if userID == "" {
		return fmt.Errorf("deploy topology workspace sync requires user id")
	}
	childSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if childSwarmID == "" {
		return nil
	}
	for _, item := range record.WorkspaceBootstrap {
		if strings.TrimSpace(item.SourceWorkspacePath) == "" {
			continue
		}
		placement, ok, err := s.topology.GetRuntimePlacementForAccount(accountScopeID, childSwarmID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("deploy topology workspace sync requires runtime placement for %s", childSwarmID)
		}
		if _, err := pebblestore.UpsertTopologyWorkspaceBindingForAccount(s.topology, accountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
			BindingID:                       pebblestore.CanonicalTopologyWorkspaceBindingID(record.ID, strings.TrimSpace(item.SourceWorkspacePath)),
			UserID:                          userID,
			AccountScopeID:                  accountScopeID,
			SourceWorkspacePath:             strings.TrimSpace(item.SourceWorkspacePath),
			SourceWorkspaceName:             strings.TrimSpace(item.SourceWorkspaceName),
			DestinationRuntimeSwarmID:       childSwarmID,
			DestinationAuthorityHostSwarmID: placement.AuthorityHostSwarmID,
			DestinationHostSwarmID:          placement.AuthorityHostSwarmID,
			DestinationContainerID:          placement.AuthorityContainerID,
			DestinationRuntimeKind:          placement.RuntimeKind,
			DestinationWorkspacePath:        strings.TrimSpace(item.TargetWorkspacePath),
			PlacementGeneration:             placement.PlacementGeneration,
			BindingGeneration:               1,
			State:                           pebblestore.TopologyWorkspaceBindingStateBound,
			AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
			MaterializationKind:             pebblestore.TopologyWorkspaceBindingMaterializationSource,
			AttestedByHostSwarmID:           placement.AuthorityHostSwarmID,
			ReplicationMode:                 strings.TrimSpace(item.ReplicationMode),
			Writable:                        item.Writable,
			Sync:                            item.Sync,
			LegacyTargetKind:                "local",
			CreatedAt:                       record.CreatedAt,
			UpdatedAt:                       record.UpdatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deleteCanonicalDeploymentState(record pebblestore.DeployContainerRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	accountScopeID := strings.TrimSpace(record.AccountScopeID)
	if accountScopeID == "" {
		return nil
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.SyncOwnerSwarmID))
	hostContainer, ok, err := pebblestore.FindTopologyHostContainerByRefsForAccount(s.topology, accountScopeID, hostSwarmID, record.ContainerID, record.ContainerName, record.ID)
	if err != nil {
		return err
	}
	childSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if ok {
		attachmentRecords, err := s.topology.ListAttachmentsByHostContainerForAccount(accountScopeID, hostContainer.HostContainerID, 500)
		if err != nil {
			return err
		}
		for _, attachmentRecord := range attachmentRecords {
			if strings.TrimSpace(attachmentRecord.DeploymentID) == strings.TrimSpace(record.ID) {
				if err := s.topology.DeleteRuntimePlacementForAccount(accountScopeID, attachmentRecord.RuntimeSwarmID); err != nil {
					return err
				}
				if err := s.topology.DeleteAttachmentForAccount(accountScopeID, attachmentRecord.AttachmentID); err != nil {
					return err
				}
				if childSwarmID == "" {
					childSwarmID = strings.TrimSpace(attachmentRecord.RuntimeSwarmID)
				}
			}
		}
		if err := s.topology.DeleteHostContainerForAccount(accountScopeID, hostContainer.HostContainerID); err != nil {
			return err
		}
	}
	if childSwarmID != "" {
		if err := s.deleteCanonicalWorkspaceRoutesForRuntimeForAccount(accountScopeID, childSwarmID); err != nil {
			return err
		}
		if err := pebblestore.RemoveTopologyRuntimeObservedSourceForAccount(s.topology, accountScopeID, childSwarmID, pebblestore.TopologyRuntimeSourceDeployContainer); err != nil {
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
	return s.deleteCanonicalWorkspaceRoutesForRuntimeFromRecords(runtimeSwarmID, bindings, routes, func(route pebblestore.TopologySessionRouteRecord) error {
		return s.topology.DeleteSessionRoute(route.SessionID)
	}, func(binding pebblestore.TopologyWorkspaceBindingRecord) error {
		return s.topology.DeleteWorkspaceBinding(binding.BindingID)
	})
}

func (s *Service) deleteCanonicalWorkspaceRoutesForRuntimeForAccount(accountScopeID, runtimeSwarmID string) error {
	if s == nil || s.topology == nil {
		return nil
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("deploy topology workspace cleanup requires account scope id")
	}
	runtimeSwarmID = strings.TrimSpace(runtimeSwarmID)
	if runtimeSwarmID == "" {
		return nil
	}
	bindings, err := s.topology.ListWorkspaceBindingsForAccount(accountScopeID, 100000)
	if err != nil {
		return err
	}
	routes, err := s.topology.ListSessionRoutesForAccount(accountScopeID, 100000)
	if err != nil {
		return err
	}
	return s.deleteCanonicalWorkspaceRoutesForRuntimeFromRecords(runtimeSwarmID, bindings, routes, func(route pebblestore.TopologySessionRouteRecord) error {
		return s.topology.DeleteSessionRouteForAccount(accountScopeID, route.SessionID)
	}, func(binding pebblestore.TopologyWorkspaceBindingRecord) error {
		return s.topology.DeleteWorkspaceBindingForAccount(accountScopeID, binding.BindingID)
	})
}

func (s *Service) deleteCanonicalWorkspaceRoutesForRuntimeFromRecords(runtimeSwarmID string, bindings []pebblestore.TopologyWorkspaceBindingRecord, routes []pebblestore.TopologySessionRouteRecord, deleteRoute func(pebblestore.TopologySessionRouteRecord) error, deleteBinding func(pebblestore.TopologyWorkspaceBindingRecord) error) error {
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
		if err := deleteRoute(route); err != nil {
			return err
		}
		deletedRoutes[strings.ToLower(sessionID)] = struct{}{}
	}
	for _, binding := range bindings {
		if !strings.EqualFold(strings.TrimSpace(binding.DestinationRuntimeSwarmID), runtimeSwarmID) {
			continue
		}
		if strings.TrimSpace(binding.BindingID) == "" {
			continue
		}
		if err := deleteBinding(binding); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) childAttachmentRecordsForDeployment(record pebblestore.DeployContainerRecord) ([]pebblestore.TopologyAttachmentRecord, error) {
	if s == nil || s.topology == nil {
		return nil, nil
	}
	accountScopeID := strings.TrimSpace(record.AccountScopeID)
	if accountScopeID == "" {
		return nil, fmt.Errorf("deploy topology attachment lookup requires account scope id")
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.SyncOwnerSwarmID))
	hostContainer, ok, err := pebblestore.FindTopologyHostContainerByRefsForAccount(s.topology, accountScopeID, hostSwarmID, record.ContainerID, record.ContainerName, record.ID)
	if err != nil || !ok {
		return nil, err
	}
	attachments, err := s.topology.ListAttachmentsByHostContainerForAccount(accountScopeID, hostContainer.HostContainerID, 500)
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
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.SyncOwnerSwarmID))
	if hostContainerID := strings.TrimSpace(record.HostContainerID); hostContainerID != "" {
		return hostContainerID, nil
	}
	runtimeContainerRef := firstNonEmpty(strings.TrimSpace(record.ContainerID), strings.TrimSpace(record.ContainerName), strings.TrimSpace(record.ID))
	if canonicalID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, runtimeContainerRef); canonicalID != "" {
		return canonicalID, nil
	}
	if s == nil || s.topology == nil {
		return "", nil
	}
	accountScopeID := strings.TrimSpace(record.AccountScopeID)
	if accountScopeID == "" {
		return "", fmt.Errorf("deploy topology host container lookup requires account scope id")
	}
	hostContainer, ok, err := pebblestore.FindTopologyHostContainerByRefsForAccount(s.topology, accountScopeID, hostSwarmID, record.ContainerID, record.ContainerName, record.ID)
	if err != nil || !ok {
		return "", err
	}
	return strings.TrimSpace(hostContainer.HostContainerID), nil
}

func (s *Service) childSwarmIDsFromCanonicalAttachments(record pebblestore.DeployContainerRecord) ([]string, error) {
	attachments, err := s.childAttachmentRecordsForDeployment(record)
	if err != nil {
		if strings.TrimSpace(record.AccountScopeID) != "" {
			return nil, err
		}
		attachments = nil
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
