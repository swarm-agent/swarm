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
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.ChildSwarmID), strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.MasterSwarmID))
	runtimeContainerRef := remoteContainerNameForSession(record.ID)
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, runtimeContainerRef)
	if hostContainerID == "" {
		return nil
	}
	if err := pebblestore.UpsertTopologyHostContainer(s.topology, pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
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
	if err := pebblestore.UpsertTopologyRuntimeRecord(s.topology, pebblestore.TopologyRuntimeRecord{
		SwarmID:              childSwarmID,
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
	return pebblestore.UpsertTopologyAttachment(s.topology, pebblestore.TopologyAttachmentRecord{
		AttachmentID:          attachmentID,
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
	hostSwarmID := firstNonEmpty(strings.TrimSpace(record.ChildSwarmID), strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.MasterSwarmID))
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, remoteContainerNameForSession(record.ID))
	childSwarmIDs, err := s.canonicalChildSwarmIDs(record)
	if err != nil {
		return err
	}
	if hostContainerID != "" {
		attachments, err := s.topology.ListAttachmentsByHostContainer(hostContainerID, 500)
		if err != nil {
			return err
		}
		for _, attachment := range attachments {
			if strings.TrimSpace(attachment.RemoteDeploySessionID) == strings.TrimSpace(record.ID) {
				if err := s.topology.DeleteAttachment(attachment.AttachmentID); err != nil {
					return err
				}
			}
		}
		if err := s.topology.DeleteHostContainer(hostContainerID); err != nil {
			return err
		}
	}
	for _, childSwarmID := range childSwarmIDs {
		if err := pebblestore.RemoveTopologyRuntimeObservedSource(s.topology, childSwarmID, pebblestore.TopologyRuntimeSourceRemoteDeploySession); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) canonicalChildSwarmIDs(record pebblestore.RemoteDeploySessionRecord) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, 1)
	if s != nil && s.topology != nil {
		hostSwarmID := firstNonEmpty(strings.TrimSpace(record.ChildSwarmID), strings.TrimSpace(record.HostSwarmID), strings.TrimSpace(record.MasterSwarmID))
		hostContainerID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, remoteContainerNameForSession(record.ID))
		if hostContainerID != "" {
			attachments, err := s.topology.ListAttachmentsByHostContainer(hostContainerID, 500)
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
