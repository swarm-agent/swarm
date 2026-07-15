package deploy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func TestFinalizeChildAttachSeedsExplicitContainerAuthority(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, swarmStore, workspaceSvc, topologyStore)
	principal := testPrincipal()
	state := swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm", Name: "Child"}}
	status := ContainerAttachState{DeploymentID: "deployment-1", ChildSwarmID: "child-swarm", HostSwarmID: "host-swarm", HostContainerID: "host-swarm:container-1"}
	input := ContainerAttachFinalizeInput{
		DeploymentID:    "deployment-1",
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		HostSwarmID:     "host-swarm",
		HostContainerID: "host-swarm:container-1",
		ChildSwarmID:    "child-swarm",
	}
	if err := deploySvc.FinalizeLocalBootstrap(identity.ContextWithPrincipal(context.Background(), principal), startupconfig.FileConfig{DeployContainer: startupconfig.DeployContainerBootstrap{HostDriven: true}}, state, status, input); err != nil {
		t.Fatalf("finalizeChildAttach() error = %v", err)
	}
	placement, ok, err := topologyStore.GetRuntimePlacementForAccount(principal.AccountScopeID, "child-swarm")
	if err != nil || !ok {
		t.Fatalf("get placement ok=%t err=%v", ok, err)
	}
	if placement.RuntimeKind != pebblestore.TopologyRuntimeKindContainer || placement.AuthorityHostSwarmID != "host-swarm" || placement.AuthorityContainerID != "host-swarm:container-1" {
		t.Fatalf("placement = %#v", placement)
	}
}

func TestFinalizeChildAttachMissingContainerAuthorityFailsClosed(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, swarmStore, topologyStore)
	principal := testPrincipal()
	err = deploySvc.FinalizeLocalBootstrap(identity.ContextWithPrincipal(context.Background(), principal), startupconfig.FileConfig{DeployContainer: startupconfig.DeployContainerBootstrap{HostDriven: true}}, swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm"}}, ContainerAttachState{HostSwarmID: "host-swarm", ChildSwarmID: "child-swarm"}, ContainerAttachFinalizeInput{UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, HostSwarmID: "host-swarm"})
	if err == nil || !strings.Contains(err.Error(), "authority host container id") {
		t.Fatalf("finalizeChildAttach() error = %v", err)
	}
}
