package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestTopologyRuntimeUpsertCreatesOnlyLocalSelfPlacement(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runtime-sync-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	if err := UpsertTopologyRuntimeRecordForAccount(topology, "account-a", TopologyRuntimeRecord{
		SwarmID:        "local-swarm",
		AccountScopeID: "account-a",
		UserID:         "user-a",
		Name:           "Local",
		Relationship:   "self",
	}); err != nil {
		t.Fatalf("upsert self runtime: %v", err)
	}
	selfPlacement, ok, err := topology.GetRuntimePlacementForAccount("account-a", "local-swarm")
	if err != nil || !ok {
		t.Fatalf("get self placement ok=%t err=%v", ok, err)
	}
	if selfPlacement.RuntimeKind != TopologyRuntimeKindHost || selfPlacement.AuthorityHostSwarmID != "local-swarm" || selfPlacement.AuthorityContainerID != "" {
		t.Fatalf("unexpected self placement: %+v", selfPlacement)
	}

	if _, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "remote-swarm",
		AccountScopeID:       "account-a",
		AuthorityHostSwarmID: "remote-authority",
		RuntimeKind:          "future-runner",
	}); err != nil {
		t.Fatalf("put generic remote placement: %v", err)
	}
	if err := UpsertTopologyRuntimeRecordForAccount(topology, "account-a", TopologyRuntimeRecord{
		SwarmID:        "remote-swarm",
		AccountScopeID: "account-a",
		UserID:         "user-a",
		Relationship:   "managed",
	}); err != nil {
		t.Fatalf("upsert remote runtime: %v", err)
	}
	remotePlacement, ok, err := topology.GetRuntimePlacementForAccount("account-a", "remote-swarm")
	if err != nil || !ok {
		t.Fatalf("get remote placement ok=%t err=%v", ok, err)
	}
	if remotePlacement.RuntimeKind != "future-runner" || remotePlacement.AuthorityHostSwarmID != "remote-authority" {
		t.Fatalf("remote placement was overwritten: %+v", remotePlacement)
	}

	if err := UpsertTopologyRuntimeRecordForAccount(topology, "account-a", TopologyRuntimeRecord{
		SwarmID:        "unplaced-swarm",
		AccountScopeID: "account-a",
		UserID:         "user-a",
		Relationship:   "child",
	}); err != nil {
		t.Fatalf("upsert unplaced runtime: %v", err)
	}
	if _, ok, err := topology.GetRuntimePlacementForAccount("account-a", "unplaced-swarm"); err != nil || ok {
		t.Fatalf("non-self runtime received implicit placement ok=%t err=%v", ok, err)
	}
}
