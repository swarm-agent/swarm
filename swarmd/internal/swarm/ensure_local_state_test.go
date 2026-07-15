package swarm

import (
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestEnsureLocalStateUsesInputNameOnlyForInitialBootstrap(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	swarmStore := pebblestore.NewSwarmStore(store)
	svc := NewService(swarmStore, nil, nil)

	initial, err := svc.EnsureLocalState(EnsureLocalStateInput{Name: "Initial Swarm", Role: "master"})
	if err != nil {
		t.Fatalf("ensure initial local state: %v", err)
	}
	if initial.Node.Name != "Initial Swarm" {
		t.Fatalf("initial name = %q, want Initial Swarm", initial.Node.Name)
	}
	if len(initial.Groups) != 0 || initial.CurrentGroupID != "" {
		t.Fatalf("initial local state created groups unexpectedly: current=%q groups=%d", initial.CurrentGroupID, len(initial.Groups))
	}
	if storedGroups, _, err := svc.ListGroupsForSwarm(initial.Node.SwarmID, 500); err != nil {
		t.Fatalf("list initial groups: %v", err)
	} else if len(storedGroups) != 0 {
		t.Fatalf("stored groups after initial local state = %d, want 0", len(storedGroups))
	}

	renamed, err := svc.RenameLocalSwarm(RenameLocalSwarmInput{Name: "Renamed Swarm"})
	if err != nil {
		t.Fatalf("rename local swarm: %v", err)
	}
	if renamed.Node.Name != "Renamed Swarm" {
		t.Fatalf("renamed state name = %q, want Renamed Swarm", renamed.Node.Name)
	}
	if renamed.Node.SwarmID != initial.Node.SwarmID {
		t.Fatalf("swarm id changed on rename: got %q want %q", renamed.Node.SwarmID, initial.Node.SwarmID)
	}

	reloaded, err := svc.EnsureLocalState(EnsureLocalStateInput{Name: "Startup Config Name", Role: "master"})
	if err != nil {
		t.Fatalf("ensure existing local state: %v", err)
	}
	if reloaded.Node.Name != "Renamed Swarm" {
		t.Fatalf("existing DB name was overridden by startup config: got %q", reloaded.Node.Name)
	}
	if reloaded.Node.SwarmID != initial.Node.SwarmID {
		t.Fatalf("swarm id changed after re-ensure: got %q want %q", reloaded.Node.SwarmID, initial.Node.SwarmID)
	}
	if len(reloaded.Groups) != 0 || reloaded.CurrentGroupID != "" {
		t.Fatalf("reloaded local state created groups unexpectedly: current=%q groups=%d", reloaded.CurrentGroupID, len(reloaded.Groups))
	}

	stored, ok, err := swarmStore.GetLocalNode()
	if err != nil || !ok {
		t.Fatalf("get local node ok=%t err=%v", ok, err)
	}
	if stored.Name != "Renamed Swarm" {
		t.Fatalf("stored name = %q, want Renamed Swarm", stored.Name)
	}
}

func TestEnsureLocalStateIgnoresManagedRoleInputWhenDBUnpaired(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	swarmStore := pebblestore.NewSwarmStore(store)
	svc := NewService(swarmStore, nil, nil)

	state, err := svc.EnsureLocalState(EnsureLocalStateInput{Name: "Local", Role: RelationshipManaged})
	if err != nil {
		t.Fatalf("ensure local state: %v", err)
	}
	if state.Node.Role != bootstrapRoleMaster {
		t.Fatalf("role from clean DB with managed input = %q, want %q", state.Node.Role, bootstrapRoleMaster)
	}
	if state.Pairing.PairingState != "unpaired" || state.Pairing.ParentSwarmID != "" {
		t.Fatalf("pairing = %+v, want standalone/unpaired", state.Pairing)
	}

	stored, ok, err := swarmStore.GetLocalNode()
	if err != nil || !ok {
		t.Fatalf("get local node ok=%t err=%v", ok, err)
	}
	if stored.Role != bootstrapRoleMaster {
		t.Fatalf("stored role = %q, want %q", stored.Role, bootstrapRoleMaster)
	}
}

func TestEnsureLocalStateKeepsPrimaryRoleWhenLegacyPairingIsPaired(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	swarmStore := pebblestore.NewSwarmStore(store)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{
		SwarmID: "local-swarm",
		Name:    "Local",
		Role:    RelationshipManaged,
	}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutLocalPairing(pebblestore.SwarmLocalPairingRecord{
		PairingState:      startupconfig.PairingStatePaired,
		ParentSwarmID:     "legacy-manager",
		LastUpdatedByRole: RelationshipManaged,
	}); err != nil {
		t.Fatalf("put local pairing: %v", err)
	}

	state, err := NewService(swarmStore, nil, nil).EnsureLocalState(EnsureLocalStateInput{})
	if err != nil {
		t.Fatalf("ensure local state: %v", err)
	}
	if state.Node.Role != bootstrapRoleMaster {
		t.Fatalf("role with legacy pairing = %q, want %q", state.Node.Role, bootstrapRoleMaster)
	}
	if state.Node.SwarmID != "local-swarm" || state.Node.Name != "Local" {
		t.Fatalf("stable local identity changed: %+v", state.Node)
	}
}
