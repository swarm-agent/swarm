package localcontainers

import (
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func (s *Service) localHostSwarmID() (string, error) {
	if s == nil || s.swarmStore == nil {
		return "", fmt.Errorf("swarm store is not configured")
	}
	record, ok, err := s.swarmStore.GetLocalNode()
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(record.SwarmID) == "" {
		return "", fmt.Errorf("local swarm id is not configured")
	}
	return strings.TrimSpace(record.SwarmID), nil
}

func (s *Service) syncTopologyHostContainer(record pebblestore.SwarmLocalContainerRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	accountScopeID := strings.TrimSpace(record.AccountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("local container topology sync requires account scope id")
	}
	if strings.TrimSpace(record.UserID) == "" {
		return fmt.Errorf("local container topology sync requires user id")
	}
	hostSwarmID, err := s.localHostSwarmID()
	if err != nil {
		return err
	}
	runtimeContainerRef := firstNonEmpty(strings.TrimSpace(record.ContainerID), strings.TrimSpace(record.ContainerName), strings.TrimSpace(record.ID))
	hostContainerID := pebblestore.CanonicalTopologyHostContainerID(hostSwarmID, runtimeContainerRef)
	if hostContainerID == "" {
		return nil
	}
	return pebblestore.UpsertTopologyHostContainerForAccount(s.topology, accountScopeID, pebblestore.TopologyHostContainerRecord{
		HostContainerID:     hostContainerID,
		UserID:              strings.TrimSpace(record.UserID),
		AccountScopeID:      accountScopeID,
		HostSwarmID:         hostSwarmID,
		RuntimeContainerRef: runtimeContainerRef,
		Name:                firstNonEmpty(record.Name, record.ContainerName, record.ID),
		ContainerName:       firstNonEmpty(record.ContainerName, record.ID),
		ContainerID:         strings.TrimSpace(record.ContainerID),
		Runtime:             strings.TrimSpace(record.Runtime),
		Image:               strings.TrimSpace(record.Image),
		Status:              strings.TrimSpace(record.Status),
		HostAPIBaseURL:      strings.TrimSpace(record.HostAPIBaseURL),
		HostPort:            record.HostPort,
		RuntimePort:         record.RuntimePort,
		Mounts:              record.Mounts,
		ObservedSources:     []string{pebblestore.TopologyHostContainerSourceSwarmLocalContainer},
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	})
}

func (s *Service) deleteTopologyHostContainer(record pebblestore.SwarmLocalContainerRecord) error {
	if s == nil || s.topology == nil {
		return nil
	}
	accountScopeID := strings.TrimSpace(record.AccountScopeID)
	if accountScopeID == "" {
		return fmt.Errorf("local container topology delete requires account scope id")
	}
	hostSwarmID, err := s.localHostSwarmID()
	if err != nil {
		return err
	}
	hostContainer, ok, err := pebblestore.FindTopologyHostContainerByRefsForAccount(s.topology, accountScopeID, hostSwarmID, record.ContainerID, record.ContainerName, record.ID)
	if err != nil || !ok {
		return err
	}
	attachmentRecords, err := s.topology.ListAttachmentsByHostContainerForAccount(accountScopeID, hostContainer.HostContainerID, 500)
	if err != nil {
		return err
	}
	for _, attachmentRecord := range attachmentRecords {
		if err := s.topology.DeleteAttachmentForAccount(accountScopeID, attachmentRecord.AttachmentID); err != nil {
			return err
		}
	}
	return s.topology.DeleteHostContainerForAccount(accountScopeID, hostContainer.HostContainerID)
}
