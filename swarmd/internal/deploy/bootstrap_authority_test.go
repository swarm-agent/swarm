package deploy

import (
	"context"
	"os"
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
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, swarmStore, nil, nil, workspaceSvc, filepath.Join(t.TempDir(), "swarm.conf"), topologyStore)

	principal := testPrincipal()
	ctx := identity.ContextWithPrincipal(context.Background(), principal)
	cfg := startupconfig.FileConfig{
		BypassPermissions: true,
		DeployContainer: startupconfig.DeployContainerBootstrap{
			Enabled:      true,
			HostDriven:   true,
			DeploymentID: "deployment-1",
		},
	}
	runtimeWorkspacePath := filepath.Join(t.TempDir(), "runtime-workspace")
	if err := os.MkdirAll(runtimeWorkspacePath, 0o755); err != nil {
		t.Fatalf("create runtime workspace: %v", err)
	}
	state := swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm", Name: "Child"}}
	status := ContainerAttachState{
		DeploymentID:     "deployment-1",
		AttachStatus:     "attached",
		ChildSwarmID:     "child-swarm",
		HostSwarmID:      "host-swarm",
		HostContainerID:  "host-swarm:container-1",
		HostDisplayName:  "Host",
		HostBackendURL:   "http://host.example:7781",
		HostDesktopURL:   "http://host.example:7782",
		GroupID:          "group-1",
		GroupName:        "Group",
		GroupNetworkName: "group-net",
	}
	err = deploySvc.finalizeChildAttach(ctx, cfg, state, status, ContainerAttachFinalizeInput{
		DeploymentID:    "deployment-1",
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		HostSwarmID:     "host-swarm",
		HostContainerID: "host-swarm:container-1",
		ChildSwarmID:    "child-swarm",
		WorkspaceBootstrap: []ContainerWorkspaceBootstrap{{
			SourceWorkspaceID:         "ws_primary_repo",
			SourceWorkspaceGeneration: 7,
			SourceWorkspacePath:       "/source/repo",
			SourceWorkspaceName:       "repo",
			TargetWorkspacePath:       runtimeWorkspacePath,
			Writable:                  true,
		}},
	})
	if err != nil {
		t.Fatalf("finalizeChildAttach() error = %v", err)
	}

	runtimeRecord, ok, err := topologyStore.GetRuntimeForAccount(principal.AccountScopeID, "child-swarm")
	if err != nil || !ok {
		t.Fatalf("get runtime ok=%t err=%v", ok, err)
	}
	if runtimeRecord.OwnerHostSwarmID != "host-swarm" || runtimeRecord.OwnerHostContainerID != "host-swarm:container-1" {
		t.Fatalf("runtime owner = %q/%q", runtimeRecord.OwnerHostSwarmID, runtimeRecord.OwnerHostContainerID)
	}
	if runtimeRecord.UserID != principal.UserID || runtimeRecord.AccountScopeID != principal.AccountScopeID {
		t.Fatalf("runtime principal = %q/%q", runtimeRecord.UserID, runtimeRecord.AccountScopeID)
	}

	placement, ok, err := topologyStore.GetRuntimePlacementForAccount(principal.AccountScopeID, "child-swarm")
	if err != nil || !ok {
		t.Fatalf("get placement ok=%t err=%v", ok, err)
	}
	if placement.RuntimeKind != pebblestore.TopologyRuntimeKindContainer || placement.AuthorityHostSwarmID != "host-swarm" || placement.AuthorityContainerID != "host-swarm:container-1" {
		t.Fatalf("placement = %#v", placement)
	}
	if placement.PlacementGeneration <= 0 || placement.State != pebblestore.TopologyRuntimePlacementStateActive {
		t.Fatalf("placement generation/state = %#v", placement)
	}

	bindings, err := topologyStore.ListWorkspaceBindingsForAccount(principal.AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("binding count = %d, want 1: %#v", len(bindings), bindings)
	}
	binding := bindings[0]
	if binding.SourceWorkspaceID != "ws_primary_repo" || binding.SourceWorkspaceGeneration != 7 {
		t.Fatalf("binding source identity = %q/%d", binding.SourceWorkspaceID, binding.SourceWorkspaceGeneration)
	}
	if binding.SourceWorkspacePath != "/source/repo" || binding.DestinationWorkspacePath != runtimeWorkspacePath {
		t.Fatalf("binding paths = %q/%q", binding.SourceWorkspacePath, binding.DestinationWorkspacePath)
	}
	if binding.DestinationRuntimeSwarmID != "child-swarm" || binding.DestinationRuntimeKind != pebblestore.TopologyRuntimeKindContainer || binding.DestinationAuthorityHostSwarmID != "host-swarm" || binding.DestinationContainerID != "host-swarm:container-1" {
		t.Fatalf("binding authority = %#v", binding)
	}
	if binding.AttestedByHostSwarmID != "host-swarm" || binding.PlacementGeneration != placement.PlacementGeneration || binding.BindingGeneration <= 0 {
		t.Fatalf("binding attestation/generation = %#v", binding)
	}

	runtimeRecord.OwnerHostSwarmID = ""
	runtimeRecord.OwnerHostContainerID = ""
	if _, err := topologyStore.PutRuntimeForAccount(principal.AccountScopeID, runtimeRecord); err != nil {
		t.Fatalf("clear runtime owner: %v", err)
	}
	if err := deploySvc.ensureChildContainerPlacementForBootstrap(principal.AccountScopeID, principal.UserID, state, status, ContainerAttachFinalizeInput{HostSwarmID: "host-swarm", HostContainerID: "host-swarm:container-1", ChildSwarmID: "child-swarm"}); err != nil {
		t.Fatalf("reseeding container placement: %v", err)
	}
	runtimeRecord, ok, err = topologyStore.GetRuntimeForAccount(principal.AccountScopeID, "child-swarm")
	if err != nil || !ok {
		t.Fatalf("get reseeded runtime ok=%t err=%v", ok, err)
	}
	if runtimeRecord.OwnerHostSwarmID == "" || runtimeRecord.OwnerHostContainerID == "" {
		t.Fatalf("runtime owner identity incomplete after reseed: %#v", runtimeRecord)
	}
}

func TestFinalizeChildAttachMissingSourceWorkspaceAuthorityFailsClosed(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, swarmStore, nil, nil, workspaceSvc, filepath.Join(t.TempDir(), "swarm.conf"), topologyStore)
	principal := testPrincipal()
	runtimeWorkspacePath := filepath.Join(t.TempDir(), "runtime-workspace")
	if err := os.MkdirAll(runtimeWorkspacePath, 0o755); err != nil {
		t.Fatalf("create runtime workspace: %v", err)
	}

	err = deploySvc.finalizeChildAttach(identity.ContextWithPrincipal(context.Background(), principal), startupconfig.FileConfig{BypassPermissions: true, DeployContainer: startupconfig.DeployContainerBootstrap{Enabled: true, HostDriven: true, DeploymentID: "deployment-1"}}, swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm", Name: "Child"}}, ContainerAttachState{DeploymentID: "deployment-1", AttachStatus: "attached", ChildSwarmID: "child-swarm", HostSwarmID: "host-swarm", HostContainerID: "host-swarm:container-1"}, ContainerAttachFinalizeInput{
		DeploymentID:    "deployment-1",
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		HostSwarmID:     "host-swarm",
		HostContainerID: "host-swarm:container-1",
		ChildSwarmID:    "child-swarm",
		WorkspaceBootstrap: []ContainerWorkspaceBootstrap{{
			SourceWorkspacePath: "/source/repo",
			SourceWorkspaceName: "repo",
			TargetWorkspacePath: runtimeWorkspacePath,
			Writable:            true,
		}},
	})
	if err == nil {
		t.Fatal("finalizeChildAttach() succeeded without source workspace authority")
	}
	if !strings.Contains(err.Error(), "source workspace id") {
		t.Fatalf("finalizeChildAttach() error = %v", err)
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
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, swarmStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), topologyStore)
	principal := testPrincipal()
	err = deploySvc.finalizeChildAttach(identity.ContextWithPrincipal(context.Background(), principal), startupconfig.FileConfig{BypassPermissions: true, DeployContainer: startupconfig.DeployContainerBootstrap{Enabled: true, HostDriven: true}}, swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "child-swarm"}}, ContainerAttachState{HostSwarmID: "host-swarm", ChildSwarmID: "child-swarm"}, ContainerAttachFinalizeInput{UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, HostSwarmID: "host-swarm"})
	if err == nil {
		t.Fatal("finalizeChildAttach() succeeded without host container authority")
	}
	if !strings.Contains(err.Error(), "authority host container id") {
		t.Fatalf("finalizeChildAttach() error = %v", err)
	}
}
