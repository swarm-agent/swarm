package deploy

import (
	"context"
	"path/filepath"
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestDeleteRemovesMatchingLocalContainerInventoryRecord(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	localStore := pebblestore.NewSwarmLocalContainerStore(store)
	deploymentStore := pebblestore.NewDeployContainerStore(store)
	topologyStore := pebblestore.NewTopologyStore(store)
	localSvc := localcontainers.NewService(localStore, deploymentStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), topologyStore)
	deploySvc := NewService(deploymentStore, localSvc, nil, nil, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), topologyStore)

	deployment := pebblestore.DeployContainerRecord{
		ID:             "pc-child333",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Kind:           "container",
		Name:           "pc child333",
		Status:         "attached",
		ContainerName:  "pc-child333",
		ContainerID:    "runtime-child333",
		AttachStatus:   "attached",
		ChildSwarmID:   "child-swarm-333",
	}
	if _, err := deploymentStore.Put(deployment); err != nil {
		t.Fatalf("put deployment: %v", err)
	}
	if _, err := localStore.Put(pebblestore.SwarmLocalContainerRecord{
		ID:            "pc-child333",
		Name:          "pc child333",
		ContainerName: "pc-child333",
		Runtime:       "podman",
		Status:        "missing",
		ContainerID:   "runtime-child333",
	}); err != nil {
		t.Fatalf("put local container: %v", err)
	}

	result, err := deploySvc.Delete(testPrincipalContext(), []string{"pc-child333"})
	if err != nil {
		t.Fatalf("Delete() error = %v, result = %+v", err, result)
	}
	if result.Count != 1 {
		t.Fatalf("Delete() count = %d, want 1", result.Count)
	}
	if _, ok, err := deploymentStore.Get("pc-child333"); err != nil || ok {
		t.Fatalf("deployment remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := localStore.Get("pc-child333"); err != nil || ok {
		t.Fatalf("local container inventory remaining ok=%t err=%v", ok, err)
	}
}

func TestDeleteMirroredManagedDeploymentSkipsPrimaryLocalInventory(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	localStore := pebblestore.NewSwarmLocalContainerStore(store)
	deploymentStore := pebblestore.NewDeployContainerStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "primary-swarm"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	localSvc := localcontainers.NewService(localStore, deploymentStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), nil)
	deploySvc := NewService(deploymentStore, localSvc, nil, swarmStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), nil)

	deployment := pebblestore.DeployContainerRecord{
		ID:             "managed-child",
		UserID:         testPrincipal().UserID,
		AccountScopeID: testPrincipal().AccountScopeID,
		Kind:           "container",
		Name:           "managed child",
		Status:         "attached",
		ContainerName:  "managed-child",
		ContainerID:    "runtime-managed-child",
		AttachStatus:   "attached",
		ChildSwarmID:   "child-swarm-managed",
		HostSwarmID:    "managed-host-swarm",
	}
	if _, err := deploymentStore.Put(deployment); err != nil {
		t.Fatalf("put deployment: %v", err)
	}
	if _, err := localStore.Put(pebblestore.SwarmLocalContainerRecord{
		ID:            "managed-child",
		Name:          "primary stale mirror",
		ContainerName: "managed-child",
		Runtime:       "docker",
		Status:        "missing",
		ContainerID:   "runtime-managed-child",
	}); err != nil {
		t.Fatalf("put local container: %v", err)
	}

	result, err := deploySvc.Delete(testPrincipalContext(), []string{"managed-child"})
	if err != nil {
		t.Fatalf("Delete() error = %v, result = %+v", err, result)
	}
	if result.Count != 1 {
		t.Fatalf("Delete() count = %d, want 1", result.Count)
	}
	if _, ok, err := deploymentStore.Get("managed-child"); err != nil || ok {
		t.Fatalf("deployment remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := localStore.Get("managed-child"); err != nil || !ok {
		t.Fatalf("local container inventory removed ok=%t err=%v", ok, err)
	}
}

func TestListBackfillsMirroredManagedDeploymentCanonicalHostFields(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	deploymentStore := pebblestore.NewDeployContainerStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "primary-swarm"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	deploySvc := NewService(deploymentStore, nil, nil, swarmStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), nil)
	if _, err := deploymentStore.Put(pebblestore.DeployContainerRecord{
		ID:               "managed-child",
		Kind:             "container",
		Name:             "managed child",
		Status:           "attached",
		ContainerName:    "managed-child",
		ContainerID:      "runtime-managed-child",
		SyncOwnerSwarmID: "managed-host-swarm",
	}); err != nil {
		t.Fatalf("put deployment: %v", err)
	}

	deployments, err := deploySvc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(deployments) != 1 {
		t.Fatalf("List() count = %d, want 1", len(deployments))
	}
	if deployments[0].HostSwarmID != "managed-host-swarm" {
		t.Fatalf("HostSwarmID = %q, want managed-host-swarm", deployments[0].HostSwarmID)
	}
}

func TestReconcileMirroredManagedDeploymentDoesNotStartPrimaryLocalRuntime(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "swarm.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	deploymentStore := pebblestore.NewDeployContainerStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "primary-swarm"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	deploySvc := NewService(deploymentStore, nil, nil, swarmStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), nil)

	err = deploySvc.reconcileLocalDeployment(context.Background(), pebblestore.DeployContainerRecord{
		ID:            "managed-child",
		Status:        "attached",
		AttachStatus:  "attached",
		Runtime:       "docker",
		ContainerName: "managed-child",
		HostSwarmID:   "managed-host-swarm",
		AlwaysOn:      true,
	})
	if err != nil {
		t.Fatalf("reconcileLocalDeployment() error = %v", err)
	}
}
