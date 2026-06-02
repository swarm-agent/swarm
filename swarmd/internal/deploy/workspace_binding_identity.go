package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func deploymentWorkspaceBindingWorkspaceID(accountScopeID, sourceWorkspacePath string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accountScopeID) + "\x00" + strings.TrimSpace(sourceWorkspacePath)))
	return "workspace_" + hex.EncodeToString(sum[:16])
}

func (s *Service) ensureChildSelfPlacementForBootstrap(accountScopeID, userID string, state swarmruntime.LocalState) error {
	if s == nil || s.topology == nil {
		return nil
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	userID = strings.TrimSpace(userID)
	if accountScopeID == "" || userID == "" {
		return nil
	}
	childSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if childSwarmID == "" {
		return nil
	}
	if _, err := s.topology.PutRuntimeForAccount(accountScopeID, pebblestore.TopologyRuntimeRecord{
		SwarmID:        childSwarmID,
		UserID:         userID,
		AccountScopeID: accountScopeID,
		Name:           firstNonEmpty(state.Node.Name, childSwarmID),
		Role:           "child",
		Relationship:   "self",
		Status:         "online",
	}); err != nil {
		return err
	}
	_, err := s.topology.PutRuntimePlacementForAccount(accountScopeID, pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       childSwarmID,
		AccountScopeID:       accountScopeID,
		AuthorityHostSwarmID: childSwarmID,
		RuntimeKind:          pebblestore.TopologyRuntimeKindHost,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
	})
	return err
}

func (s *Service) ensureChildContainerPlacementForBootstrap(accountScopeID, userID string, state swarmruntime.LocalState, status ContainerAttachState, finalizeInput ContainerAttachFinalizeInput) error {
	if s == nil || s.topology == nil {
		return nil
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	userID = strings.TrimSpace(userID)
	if accountScopeID == "" || userID == "" {
		return nil
	}
	childSwarmID := strings.TrimSpace(state.Node.SwarmID)
	if childSwarmID == "" {
		return fmt.Errorf("child container topology bootstrap requires child swarm id")
	}
	if explicitChildSwarmID := strings.TrimSpace(finalizeInput.ChildSwarmID); explicitChildSwarmID != "" && explicitChildSwarmID != childSwarmID {
		return fmt.Errorf("child container topology bootstrap child swarm id mismatch")
	}
	if statusChildSwarmID := strings.TrimSpace(status.ChildSwarmID); statusChildSwarmID != "" && statusChildSwarmID != childSwarmID {
		return fmt.Errorf("child container topology bootstrap attach state child swarm id mismatch")
	}
	hostSwarmID := firstNonEmpty(strings.TrimSpace(finalizeInput.HostSwarmID), strings.TrimSpace(status.HostSwarmID))
	hostContainerID := firstNonEmpty(strings.TrimSpace(finalizeInput.HostContainerID), strings.TrimSpace(status.HostContainerID))
	if hostSwarmID == "" {
		return fmt.Errorf("child container topology bootstrap requires authority host swarm id")
	}
	if hostContainerID == "" {
		return fmt.Errorf("child container topology bootstrap requires authority host container id")
	}
	if hostSwarmID == childSwarmID {
		return fmt.Errorf("child container topology bootstrap authority host swarm id must not equal child swarm id")
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(s.topology, accountScopeID, pebblestore.TopologyRuntimeRecord{
		SwarmID:              childSwarmID,
		UserID:               userID,
		AccountScopeID:       accountScopeID,
		Name:                 firstNonEmpty(state.Node.Name, childSwarmID),
		Role:                 "child",
		Relationship:         "child",
		Status:               "online",
		OwnerHostSwarmID:     hostSwarmID,
		OwnerHostContainerID: hostContainerID,
		ObservedSources:      []string{pebblestore.TopologyRuntimeSourceDeployContainer},
	}); err != nil {
		return err
	}
	_, err := s.topology.PutRuntimePlacementForAccount(accountScopeID, pebblestore.TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       childSwarmID,
		AccountScopeID:       accountScopeID,
		AuthorityHostSwarmID: hostSwarmID,
		AuthorityContainerID: hostContainerID,
		RuntimeKind:          pebblestore.TopologyRuntimeKindContainer,
		PlacementGeneration:  1,
		State:                pebblestore.TopologyRuntimePlacementStateActive,
	})
	return err
}
