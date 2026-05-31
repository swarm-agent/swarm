package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTopologyRuntimeUpsertCreatesHostAndContainerPlacements(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runtime-sync-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	if err := UpsertTopologyRuntimeRecordForAccount(topology, "account-a", TopologyRuntimeRecord{
		SwarmID:        "host-swarm",
		AccountScopeID: "account-a",
		UserID:         "user-a",
		Name:           "Host",
		Relationship:   "managed",
	}); err != nil {
		t.Fatalf("upsert host runtime: %v", err)
	}
	hostPlacement, ok, err := topology.GetRuntimePlacementForAccount("account-a", "host-swarm")
	if err != nil || !ok {
		t.Fatalf("get host placement ok=%t err=%v", ok, err)
	}
	if hostPlacement.RuntimeKind != TopologyRuntimeKindHost || hostPlacement.AuthorityHostSwarmID != "host-swarm" || hostPlacement.AuthorityContainerID != "" {
		t.Fatalf("unexpected host placement: %+v", hostPlacement)
	}

	if err := UpsertTopologyHostContainerForAccount(topology, "account-a", TopologyHostContainerRecord{
		HostContainerID:     "host-swarm:container-1",
		AccountScopeID:      "account-a",
		UserID:              "user-a",
		HostSwarmID:         "host-swarm",
		RuntimeContainerRef: "container-1",
		Name:                "Container One",
	}); err != nil {
		t.Fatalf("upsert host container: %v", err)
	}
	if err := UpsertTopologyRuntimeRecordForAccount(topology, "account-a", TopologyRuntimeRecord{
		SwarmID:              "container-runtime",
		AccountScopeID:       "account-a",
		UserID:               "user-a",
		Name:                 "Container",
		Relationship:         "child",
		OwnerHostSwarmID:     "host-swarm",
		OwnerHostContainerID: "host-swarm:container-1",
	}); err != nil {
		t.Fatalf("upsert container runtime: %v", err)
	}
	containerPlacement, ok, err := topology.GetRuntimePlacementForAccount("account-a", "container-runtime")
	if err != nil || !ok {
		t.Fatalf("get container placement ok=%t err=%v", ok, err)
	}
	if containerPlacement.RuntimeKind != TopologyRuntimeKindContainer || containerPlacement.AuthorityHostSwarmID != "host-swarm" || containerPlacement.AuthorityContainerID != "host-swarm:container-1" {
		t.Fatalf("unexpected container placement: %+v", containerPlacement)
	}
}

func TestTopologyRuntimeUpsertRejectsInvalidContainerPlacement(t *testing.T) {
	t.Run("host runtime with container id rejected", func(t *testing.T) {
		store, err := Open(filepath.Join(t.TempDir(), "runtime-sync-placement-host-invalid.pebble"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer func() { _ = store.Close() }()

		topology := NewTopologyStore(store)
		_, err = topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
			RuntimeSwarmID:       "host-runtime",
			AccountScopeID:       "account-a",
			AuthorityHostSwarmID: "host-runtime",
			AuthorityContainerID: "container-1",
			RuntimeKind:          TopologyRuntimeKindHost,
		})
		if err == nil {
			t.Fatal("expected host runtime with container id error")
		}
	})

	store, err := Open(filepath.Join(t.TempDir(), "runtime-sync-placement-invalid.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	if err := UpsertTopologyRuntimeRecordForAccount(topology, "account-a", TopologyRuntimeRecord{
		SwarmID:          "container-runtime",
		AccountScopeID:   "account-a",
		UserID:           "user-a",
		OwnerHostSwarmID: "host-swarm",
	}); err == nil {
		t.Fatal("expected missing container id error")
	} else if !strings.Contains(err.Error(), "authority container id is required") {
		t.Fatalf("expected missing container id error, got %v", err)
	}
	if err := UpsertTopologyRuntimeRecordForAccount(topology, "account-a", TopologyRuntimeRecord{
		SwarmID:              "host-with-container",
		AccountScopeID:       "account-a",
		UserID:               "user-a",
		OwnerHostContainerID: "host-swarm:container-1",
	}); err == nil {
		t.Fatal("expected missing authority host error")
	} else if !strings.Contains(err.Error(), "authority host swarm id is required") {
		t.Fatalf("expected missing authority host error, got %v", err)
	}
}
