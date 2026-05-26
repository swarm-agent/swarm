package api

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func seedFlowTopologyWorkspaceBinding(t *testing.T, server *Server, hostWorkspace, workspaceName, deploymentID, targetKind, targetSwarmID, targetWorkspacePath string) {
	t.Helper()
	if server == nil || server.topology == nil {
		t.Fatalf("topology service not configured")
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		AccountScopeID:            testAccountScopeID,
		UserID:                    testUserID,
		BindingID:                 pebblestore.CanonicalTopologyWorkspaceBindingID(deploymentID, hostWorkspace),
		SourceWorkspacePath:       hostWorkspace,
		SourceWorkspaceName:       workspaceName,
		DestinationRuntimeSwarmID: targetSwarmID,
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationContainerID:    deploymentID,
		DestinationWorkspacePath:  targetWorkspacePath,
		ReplicationMode:           workspaceruntime.ReplicationModeBundle,
		Writable:                  true,
		LegacyTargetKind:          targetKind,
	}); err != nil {
		t.Fatalf("seed topology workspace binding: %v", err)
	}
}
