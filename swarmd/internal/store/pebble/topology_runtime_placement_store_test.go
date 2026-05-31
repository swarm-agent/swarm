package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTopologyRuntimePlacementForAccountHostSelfPlacement(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runtime-placement.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	placement, err := topology.PutRuntimePlacementForAccount(" account-a ", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       " local-swarm ",
		AccountScopeID:       " account-a ",
		AuthorityHostSwarmID: " local-swarm ",
		RuntimeKind:          " HOST ",
	})
	if err != nil {
		t.Fatalf("put host placement: %v", err)
	}
	if placement.PlacementID == "" {
		t.Fatalf("placement id missing: %+v", placement)
	}
	if placement.AccountScopeID != "account-a" || placement.RuntimeSwarmID != "local-swarm" {
		t.Fatalf("placement identity not normalized: %+v", placement)
	}
	if placement.RuntimeKind != TopologyRuntimeKindHost || placement.State != TopologyRuntimePlacementStateActive || placement.PlacementGeneration != 1 {
		t.Fatalf("placement defaults not applied: %+v", placement)
	}
	if placement.AuthorityHostSwarmID != placement.RuntimeSwarmID || placement.AuthorityContainerID != "" {
		t.Fatalf("host authority shape invalid: %+v", placement)
	}
	if placement.CreatedAt <= 0 || placement.UpdatedAt <= 0 {
		t.Fatalf("placement timestamps not populated: %+v", placement)
	}

	loaded, ok, err := topology.GetRuntimePlacementForAccount("account-a", "local-swarm")
	if err != nil || !ok {
		t.Fatalf("get host placement ok=%t err=%v", ok, err)
	}
	if loaded != placement {
		t.Fatalf("loaded placement mismatch: got %+v want %+v", loaded, placement)
	}
	updated, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "local-swarm",
		AccountScopeID:       "account-a",
		AuthorityHostSwarmID: "local-swarm",
		RuntimeKind:          TopologyRuntimeKindHost,
	})
	if err != nil {
		t.Fatalf("put host placement again: %v", err)
	}
	if updated.CreatedAt != placement.CreatedAt || updated.PlacementGeneration != placement.PlacementGeneration || updated.PlacementID != placement.PlacementID {
		t.Fatalf("host placement did not preserve identity fields: first=%+v updated=%+v", placement, updated)
	}

	placements, err := topology.ListRuntimePlacementsForAccount("account-a", 10)
	if err != nil {
		t.Fatalf("list host placements: %v", err)
	}
	if len(placements) != 1 || placements[0].RuntimeSwarmID != "local-swarm" {
		t.Fatalf("unexpected placement list: %+v", placements)
	}
	if _, ok, err := topology.GetRuntimePlacementForAccount("account-b", "local-swarm"); err != nil || ok {
		t.Fatalf("placement crossed account boundary ok=%t err=%v", ok, err)
	}
	if got, want := KeyTopologyRuntimePlacementForAccount("account-a", "local-swarm"), KeyTopologyRuntime("local-swarm"); got == want {
		t.Fatalf("runtime placement key reused runtime key: %q", got)
	}
	if _, ok, err := topology.GetRuntimeForAccount("account-a", "local-swarm"); err != nil || ok {
		t.Fatalf("placement write created runtime row ok=%t err=%v", ok, err)
	}
}

func TestTopologyRuntimePlacementValidation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runtime-placement-validation.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	tests := []struct {
		name   string
		record TopologyRuntimePlacementRecord
		want   string
	}{
		{name: "host authority mismatch", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime", AccountScopeID: "account-a", AuthorityHostSwarmID: "other", RuntimeKind: TopologyRuntimeKindHost}, want: "must equal runtime swarm id"},
		{name: "host container authority", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime", AccountScopeID: "account-a", AuthorityHostSwarmID: "runtime", AuthorityContainerID: "container", RuntimeKind: TopologyRuntimeKindHost}, want: "must be empty"},
		{name: "container missing host", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime", AccountScopeID: "account-a", AuthorityContainerID: "container", RuntimeKind: TopologyRuntimeKindContainer}, want: "authority host swarm id is required"},
		{name: "container missing container", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime", AccountScopeID: "account-a", AuthorityHostSwarmID: "host", RuntimeKind: TopologyRuntimeKindContainer}, want: "authority container id is required"},
		{name: "container self authority", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime", AccountScopeID: "account-a", AuthorityHostSwarmID: "runtime", AuthorityContainerID: "container", RuntimeKind: TopologyRuntimeKindContainer}, want: "must not equal runtime swarm id"},
		{name: "missing kind", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime", AccountScopeID: "account-a", AuthorityHostSwarmID: "runtime"}, want: "runtime kind must be host or container"},
		{name: "missing runtime", record: TopologyRuntimePlacementRecord{AccountScopeID: "account-a", AuthorityHostSwarmID: "runtime", RuntimeKind: TopologyRuntimeKindHost}, want: "topology runtime swarm id is required"},
		{name: "missing account scope defaults from account key", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime-default-account", AuthorityHostSwarmID: "runtime-default-account", RuntimeKind: TopologyRuntimeKindHost}, want: ""},
		{name: "placement id is accepted when provided", record: TopologyRuntimePlacementRecord{PlacementID: "rtp_existing", RuntimeSwarmID: "runtime-existing-placement", AccountScopeID: "account-a", AuthorityHostSwarmID: "runtime-existing-placement", RuntimeKind: TopologyRuntimeKindHost}, want: ""},
		{name: "missing generation defaults one", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime-default-generation", AccountScopeID: "account-a", AuthorityHostSwarmID: "runtime-default-generation", RuntimeKind: TopologyRuntimeKindHost, PlacementGeneration: 0}, want: ""},
		{name: "missing state defaults active", record: TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime-default-state", AccountScopeID: "account-a", AuthorityHostSwarmID: "runtime-default-state", RuntimeKind: TopologyRuntimeKindHost, State: "   "}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			placement, err := topology.PutRuntimePlacementForAccount("account-a", tc.record)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				if placement.PlacementID == "" {
					t.Fatalf("placement id default missing: %+v", placement)
				}
				if placement.AccountScopeID != "account-a" {
					t.Fatalf("account scope default = %q", placement.AccountScopeID)
				}
				if placement.State != TopologyRuntimePlacementStateActive {
					t.Fatalf("state default = %q", placement.State)
				}
				if placement.PlacementGeneration != 1 {
					t.Fatalf("placement generation default = %d", placement.PlacementGeneration)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}

	if _, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime", AccountScopeID: "account-b", AuthorityHostSwarmID: "runtime", RuntimeKind: TopologyRuntimeKindHost}); err == nil {
		t.Fatal("expected mismatched account scope error")
	}
	if _, err := topology.PutRuntimePlacementForAccount(" account-a ", TopologyRuntimePlacementRecord{RuntimeSwarmID: "case-runtime", AccountScopeID: "Account-A", AuthorityHostSwarmID: "case-runtime", RuntimeKind: TopologyRuntimeKindHost}); err == nil {
		t.Fatal("expected non-canonical account scope mismatch error")
	}
	if _, err := topology.PutRuntimePlacementForAccount("", TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime", AccountScopeID: "account-a", AuthorityHostSwarmID: "runtime", RuntimeKind: TopologyRuntimeKindHost}); err == nil {
		t.Fatal("expected missing account scope error")
	}
	if err := store.PutJSON(KeyTopologyRuntimePlacementForAccount("account-a", "runtime-no-account"), TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime-no-account", AuthorityHostSwarmID: "runtime-no-account", RuntimeKind: TopologyRuntimeKindHost, PlacementGeneration: 1, State: TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("seed legacy placement: %v", err)
	}
	backfilledAccount, ok, err := topology.GetRuntimePlacementForAccount("account-a", "runtime-no-account")
	if err != nil || !ok || backfilledAccount.AccountScopeID != "account-a" || backfilledAccount.PlacementID == "" {
		t.Fatalf("expected missing account placement to inherit account key/id: ok=%t err=%v record=%+v", ok, err, backfilledAccount)
	}
}

func TestTopologyRuntimePlacementContainerShape(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runtime-placement-container.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	if _, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "host-swarm",
		AccountScopeID:       "account-a",
		AuthorityHostSwarmID: "host-swarm",
		RuntimeKind:          TopologyRuntimeKindHost,
	}); err != nil {
		t.Fatalf("put host placement: %v", err)
	}
	if err := UpsertTopologyHostContainerForAccount(topology, "account-a", TopologyHostContainerRecord{
		HostContainerID:     "container-1",
		AccountScopeID:      "account-a",
		UserID:              "user-a",
		HostSwarmID:         "host-swarm",
		RuntimeContainerRef: "container-1",
		Name:                "Container One",
	}); err != nil {
		t.Fatalf("put host container: %v", err)
	}
	placement, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "container-runtime",
		AccountScopeID:       "account-a",
		AuthorityHostSwarmID: "host-swarm",
		AuthorityContainerID: "container-1",
		RuntimeKind:          TopologyRuntimeKindContainer,
	})
	if err != nil {
		t.Fatalf("put container placement: %v", err)
	}
	if placement.RuntimeKind != TopologyRuntimeKindContainer || placement.AuthorityHostSwarmID != "host-swarm" || placement.AuthorityContainerID != "container-1" {
		t.Fatalf("unexpected container placement: %+v", placement)
	}
}

func TestTopologyRuntimePlacementRejectsKnownContainerAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runtime-placement-container-authority.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	if _, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "host-swarm",
		AccountScopeID:       "account-a",
		AuthorityHostSwarmID: "host-swarm",
		RuntimeKind:          TopologyRuntimeKindHost,
	}); err != nil {
		t.Fatalf("put host placement: %v", err)
	}
	if err := UpsertTopologyHostContainerForAccount(topology, "account-a", TopologyHostContainerRecord{
		HostContainerID:     "container-1",
		AccountScopeID:      "account-a",
		UserID:              "user-a",
		HostSwarmID:         "host-swarm",
		RuntimeContainerRef: "container-1",
		Name:                "Container One",
	}); err != nil {
		t.Fatalf("put host container: %v", err)
	}
	if _, err := topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "container-runtime",
		AccountScopeID:       "account-a",
		AuthorityHostSwarmID: "host-swarm",
		AuthorityContainerID: "container-1",
		RuntimeKind:          TopologyRuntimeKindContainer,
	}); err != nil {
		t.Fatalf("put container placement: %v", err)
	}
	if err := UpsertTopologyHostContainerForAccount(topology, "account-a", TopologyHostContainerRecord{
		HostContainerID:     "container-2",
		AccountScopeID:      "account-a",
		UserID:              "user-a",
		HostSwarmID:         "container-runtime",
		RuntimeContainerRef: "container-2",
		Name:                "Container Two",
	}); err != nil {
		t.Fatalf("put nested host container: %v", err)
	}
	_, err = topology.PutRuntimePlacementForAccount("account-a", TopologyRuntimePlacementRecord{
		RuntimeSwarmID:       "nested-container-runtime",
		AccountScopeID:       "account-a",
		AuthorityHostSwarmID: "container-runtime",
		AuthorityContainerID: "container-2",
		RuntimeKind:          TopologyRuntimeKindContainer,
	})
	if err == nil || !strings.Contains(err.Error(), "must reference a host runtime") {
		t.Fatalf("expected container authority rejection, got %v", err)
	}
}
