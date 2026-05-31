package deploy

import (
	"crypto/sha256"
	"encoding/hex"
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
