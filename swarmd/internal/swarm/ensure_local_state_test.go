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
	remapAttempt, err := svc.EnsureLocalState(EnsureLocalStateInput{SwarmID: "replacement-swarm-id"})
	if err != nil {
		t.Fatalf("ensure local state with replacement id: %v", err)
	}
	if remapAttempt.Node.SwarmID != initial.Node.SwarmID {
		t.Fatalf("persisted canonical swarm id was remapped: got %q want %q", remapAttempt.Node.SwarmID, initial.Node.SwarmID)
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

func TestEnsureLocalStateMintReportStateDistinguishesGeneratedRestoredAndExisting(t *testing.T) {
	t.Run("generated identity is pending until completed", func(t *testing.T) {
		svc, swarmStore := newTestService(t)
		state, err := svc.EnsureLocalState(EnsureLocalStateInput{})
		if err != nil {
			t.Fatalf("ensure generated local state: %v", err)
		}
		pendingID, pending, err := svc.PendingMintReport()
		if err != nil || !pending || pendingID != state.Node.SwarmID {
			t.Fatalf("pending report id=%q pending=%t err=%v", pendingID, pending, err)
		}
		if err := svc.CompleteMintReport(state.Node.SwarmID); err != nil {
			t.Fatalf("complete mint report: %v", err)
		}
		if err := svc.CompleteMintReport(state.Node.SwarmID); err != nil {
			t.Fatalf("complete mint report idempotently: %v", err)
		}
		if _, pending, err := svc.PendingMintReport(); err != nil || pending {
			t.Fatalf("completed report pending=%t err=%v", pending, err)
		}
		stored, ok, err := swarmStore.GetLocalNode()
		if err != nil || !ok || stored.MintReportState != mintReportStateCompleted || stored.MintReportCompletedAt <= 0 {
			t.Fatalf("stored completed state=%+v ok=%t err=%v", stored, ok, err)
		}
	})

	t.Run("explicit restore is never a mint", func(t *testing.T) {
		svc, _ := newTestService(t)
		if _, err := svc.EnsureLocalState(EnsureLocalStateInput{SwarmID: "restored-swarm-id"}); err != nil {
			t.Fatalf("ensure restored local state: %v", err)
		}
		if _, pending, err := svc.PendingMintReport(); err != nil || pending {
			t.Fatalf("restored identity pending=%t err=%v", pending, err)
		}
	})

	t.Run("pre-existing record is never retroactively marked", func(t *testing.T) {
		svc, swarmStore := newTestService(t)
		if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "existing-swarm-id", Role: bootstrapRoleMaster}); err != nil {
			t.Fatalf("seed existing record: %v", err)
		}
		if _, err := svc.EnsureLocalState(EnsureLocalStateInput{}); err != nil {
			t.Fatalf("ensure existing local state: %v", err)
		}
		if _, pending, err := svc.PendingMintReport(); err != nil || pending {
			t.Fatalf("existing identity pending=%t err=%v", pending, err)
		}
	})
}

func newTestService(t *testing.T) (*Service, *pebblestore.SwarmStore) {
	t.Helper()
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	swarmStore := pebblestore.NewSwarmStore(store)
	service := NewService(swarmStore, nil, nil)
	service.SetSecretStore(store)
	return service, swarmStore
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
	stored, ok, err := swarmStore.GetLocalNode()
	if err != nil || !ok {
		t.Fatalf("get local node ok=%t err=%v", ok, err)
	}
	if stored.Role != bootstrapRoleMaster {
		t.Fatalf("stored role = %q, want %q", stored.Role, bootstrapRoleMaster)
	}
}
