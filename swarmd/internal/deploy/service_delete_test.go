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
	localSvc := localcontainers.NewService(localStore, deploymentStore, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"))
	deploySvc := NewService(deploymentStore, localSvc, nil, nil, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"))

	deployment := pebblestore.DeployContainerRecord{
		ID:            "pc-child333",
		Kind:          "container",
		Name:          "pc child333",
		Status:        "attached",
		ContainerName: "pc-child333",
		ContainerID:   "runtime-child333",
		AttachStatus:  "attached",
		ChildSwarmID:  "child-swarm-333",
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

	result, err := deploySvc.Delete(context.Background(), []string{"pc-child333"})
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
