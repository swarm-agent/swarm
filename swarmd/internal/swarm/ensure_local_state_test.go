package swarm

import (
	"testing"

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

	initial, err := svc.EnsureLocalState(EnsureLocalStateInput{Name: "Initial Swarm", Role: "master", SwarmMode: true})
	if err != nil {
		t.Fatalf("ensure initial local state: %v", err)
	}
	if initial.Node.Name != "Initial Swarm" {
		t.Fatalf("initial name = %q, want Initial Swarm", initial.Node.Name)
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

	reloaded, err := svc.EnsureLocalState(EnsureLocalStateInput{Name: "Startup Config Name", Role: "master", SwarmMode: true})
	if err != nil {
		t.Fatalf("ensure existing local state: %v", err)
	}
	if reloaded.Node.Name != "Renamed Swarm" {
		t.Fatalf("existing DB name was overridden by startup config: got %q", reloaded.Node.Name)
	}
	if reloaded.Node.SwarmID != initial.Node.SwarmID {
		t.Fatalf("swarm id changed after re-ensure: got %q want %q", reloaded.Node.SwarmID, initial.Node.SwarmID)
	}

	stored, ok, err := swarmStore.GetLocalNode()
	if err != nil || !ok {
		t.Fatalf("get local node ok=%t err=%v", ok, err)
	}
	if stored.Name != "Renamed Swarm" {
		t.Fatalf("stored name = %q, want Renamed Swarm", stored.Name)
	}
}
