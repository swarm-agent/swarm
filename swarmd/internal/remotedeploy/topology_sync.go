package remotedeploy

import (
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) syncCanonicalRemoteDeployState(record pebblestore.RemoteDeploySessionRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	childSwarmID := strings.TrimSpace(record.ChildSwarmID)
	if childSwarmID == "" {
		return nil
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.MasterSwarmID))
	runtimeContainerRef := remoteContainerNameForSession(record.ID)
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, runtimeContainerRef)
	if hostContainerID == "" {
		return nil
	}
	if err := pebblestore.UpsertTopologyHostContainerForAccount(s.topology, hostSwarmID, pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
		UserID:              hostSwarmID,
		AccountScopeID:      hostSwarmID,
		HostSwarmID:         hostSwarmID,
		RuntimeContainerRef: runtimeContainerRef,
		Name:                firstNonEmpty(record.ChildName, record.Name, record.ID),
		ContainerName:       runtimeContainerRef,
		Runtime:             strings.TrimSpace(record.RemoteRuntime),
		Status:              strings.TrimSpace(record.Status),
		HostAPIBaseURL:      strings.TrimSpace(remoteSessionEndpoint(record)),
		ObservedSources:     []string{pebblestore.TopologyHostContainerSourceRemoteDeploySession},
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}); err != nil {
		return err
	}
	if strings.TrimSpace(hostSwarmID) == "" {
		return nil
	}
	if _, err := s.topology.PutRuntimePlacementForAccount(hostSwarmID, pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       childSwarmID,
		AccountScopeID:       hostSwarmID,
		AuthorityHostSwarmID: hostSwarmID,
		AuthorityContainerID: hostContainerID,
		RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}); err != nil {
		return err
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(s.topology, hostSwarmID, pebblestore.TopologyRuntimeRecord{
		SwarmID:              childSwarmID,
		UserID:               hostSwarmID,
		AccountScopeID:       hostSwarmID,
		Name:                 firstNonEmpty(record.ChildName, record.Name, childSwarmID),
		Role:                 "child",
		Relationship:         "child",
		BackendURL:           strings.TrimSpace(remoteSessionEndpoint(record)),
		Status:               strings.TrimSpace(record.Status),
		OwnerHostSwarmID:     hostSwarmID,
		OwnerHostContainerID: hostContainerID,
		ObservedSources:      []string{pebblestore.TopologyRuntimeSourceRemoteDeploySession},
		CreatedAt:            record.CreatedAt,
		UpdatedAt:            record.UpdatedAt,
	}); err != nil {
		return err
	}
	attachmentID := pebblestore.CanonicalTopologyAttachmentID(hostContainerID, childSwarmID)
	return pebblestore.UpsertTopologyAttachmentForAccount(s.topology, hostSwarmID, pebblestore.TopologyAttachmentRecord{
		AttachmentID:          attachmentID,
		UserID:                hostSwarmID,
		AccountScopeID:        hostSwarmID,
		HostContainerID:       hostContainerID,
		RuntimeSwarmID:        childSwarmID,
		State:                 strings.TrimSpace(record.Status),
		RemoteDeploySessionID: strings.TrimSpace(record.ID),
		CreatedAt:             record.CreatedAt,
		UpdatedAt:             record.UpdatedAt,
	})
}

func (s *Service) deleteCanonicalRemoteDeployState(record pebblestore.RemoteDeploySessionRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.MasterSwarmID))
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, remoteContainerNameForSession(record.ID))
	childSwarmIDs, err := s.canonicalChildSwarmIDs(record)
	if err != nil {
		return err
	}
	if hostContainerID != "" {
		attachments, err := s.topology.ListAttachmentsByHostContainerForAccount(hostSwarmID, hostContainerID, 500)
		if err != nil {
			return err
		}
		for _, attachment := range attachments {
			if strings.TrimSpace(attachment.RemoteDeploySessionID) == strings.TrimSpace(record.ID) {
				if err := s.topology.DeleteRuntimePlacementForAccount(hostSwarmID, attachment.RuntimeSwarmID); err != nil {
					return err
				}
				if err := s.topology.DeleteAttachmentForAccount(hostSwarmID, attachment.AttachmentID); err != nil {
					return err
				}
			}
		}
		if err := s.topology.DeleteHostContainerForAccount(hostSwarmID, hostContainerID); err != nil {
			return err
		}
	}
	for _, childSwarmID := range childSwarmIDs {
		if err := pebblestore.RemoveTopologyRuntimeObservedSourceForAccount(s.topology, hostSwarmID, childSwarmID, pebblestore.TopologyRuntimeSourceRemoteDeploySession); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) canonicalChildSwarmIDs(record pebblestore.RemoteDeploySessionRecord) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, 1)
	if s != nil && s.topology != nil {
		hostSwarmID := firstNonEmpty(strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.MasterSwarmID))
		hostContainerID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, remoteContainerNameForSession(record.ID))
		if hostContainerID != "" {
			attachments, err := s.topology.ListAttachmentsByHostContainerForAccount(hostSwarmID, hostContainerID, 500)
			if err != nil {
				return nil, err
			}
			for _, attachment := range attachments {
				childSwarmID := strings.TrimSpace(attachment.RuntimeSwarmID)
				if childSwarmID == "" {
					continue
				}
				if _, ok := seen[childSwarmID]; ok {
					continue
				}
				seen[childSwarmID] = struct{}{}
				out = append(out, childSwarmID)
			}
		}
	}
	if len(out) == 0 {
		if childSwarmID := strings.TrimSpace(record.ChildSwarmID); childSwarmID != "" {
			out = append(out, childSwarmID)
		}
	}
	return out, nil
}
