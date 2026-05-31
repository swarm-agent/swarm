package api

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

const flowTestDefaultSelfWorkspaceBindingID = "flow-test-self-binding"

func seedFlowDefaultSelfWorkspaceBinding(t *testing.T, server *Server) {
	t.Helper()
	if server == nil || server.topology == nil {
		t.Fatalf("topology service not configured")
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		AccountScopeID:            testAccountScopeID,
		UserID:                    testUserID,
		BindingID:                 flowTestDefaultSelfWorkspaceBindingID,
		SourceWorkspacePath:       "workspace/project",
		SourceWorkspaceName:       "project",
		DestinationRuntimeSwarmID: "host-swarm-id",
		DestinationHostSwarmID:    "host-swarm-id",
		DestinationWorkspacePath:  "workspace/project",
		ReplicationMode:           workspaceruntime.ReplicationModeBundle,
		Writable:                  true,
		LegacyTargetKind:          "self",
	}); err != nil {
		t.Fatalf("seed default flow self workspace binding: %v", err)
	}
}

func seedFlowTopologyWorkspaceBinding(t *testing.T, server *Server, hostWorkspace, workspaceName, deploymentID, targetKind, targetSwarmID, targetWorkspacePath string) string {
	t.Helper()
	if server == nil || server.topology == nil {
		t.Fatalf("topology service not configured")
	}
	bindingID := pebblestore.CanonicalTopologyWorkspaceBindingID(deploymentID, hostWorkspace)
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		AccountScopeID:            testAccountScopeID,
		UserID:                    testUserID,
		BindingID:                 bindingID,
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
	return bindingID
}
