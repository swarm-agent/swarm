package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTopologyRuntimePlacementRejectsContainerIDForDifferentHostAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runtime-placement-authority-container-host.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	for _, host := range []string{"host-a", "host-b"} {
		if _, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
			RuntimeSwarmID:       host,
			AccountScopeID:       "account-a",
			AuthorityHostSwarmID: host,
			RuntimeKind:          TopologyRuntimeKindHost,
		}); err != nil {
			t.Fatalf("put host placement %s: %v", host, err)
		}
	}
	if err := UpsertTopologyHostContainerForAccount(topology, "account-a", TopologyHostContainerRecord{
		HostContainerID:     "host-a:container-1",
		AccountScopeID:      "account-a",
		UserID:              "user-a",
		HostSwarmID:         "host-a",
		RuntimeContainerRef: "container-1",
		Name:                "Container One",
	}); err != nil {
		t.Fatalf("put host container: %v", err)
	}

	_, err = topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "container-runtime",
		AccountScopeID:       "account-a",
		AuthorityHostSwarmID: "host-b",
		AuthorityContainerID: "host-a:container-1",
		RuntimeKind:          TopologyRuntimeKindContainer,
	})
	if err == nil || !strings.Contains(err.Error(), "must belong to authority host swarm id") {
		t.Fatalf("expected container host mismatch rejection, got %v", err)
	}
}
