package topology

import (
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRebuildIgnoresLegacyWorkspaceReplicationLinks(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-rebuild-legacy.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	workspaceStore := pebblestore.NewWorkspaceStore(store)

	if _, err := workspaceStore.Add("/src", "Source"); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	legacyLink := pebblestore.WorkspaceReplicationLink{
		ID:                  "legacy-only-link",
		TargetKind:          "managed_host",
		TargetSwarmID:       "legacy-host",
		TargetWorkspacePath: "/dst",
		ReplicationMode:     "mirror",
		Writable:            true,
	}
	legacyEntry, ok, err := workspaceStore.Get("/src")
	if err != nil || !ok {
		t.Fatalf("get workspace ok=%t err=%v", ok, err)
	}
	legacyEntry.ReplicationLinks = []pebblestore.WorkspaceReplicationLink{legacyLink}
	if err := store.PutJSON(pebblestore.KeyWorkspaceEntry(legacyEntry.Path), legacyEntry); err != nil {
		t.Fatalf("seed legacy replication link: %v", err)
	}

	service := NewService(topologyStore, nil, nil, nil, nil, nil, nil, workspaceStore)
	status, err := service.Rebuild()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if status.WorkspaceBindingCount != 0 {
		t.Fatalf("workspace binding count = %d, want 0", status.WorkspaceBindingCount)
	}

	bindings, err := topologyStore.ListWorkspaceBindings(100000)
	if err != nil {
		t.Fatalf("list topology workspace bindings: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("legacy links were imported into topology: %+v", bindings)
	}
}

func TestRebuildPreservesCanonicalWorkspaceBindings(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-rebuild-canonical.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	workspaceStore := pebblestore.NewWorkspaceStore(store)

	if _, err := workspaceStore.Add("/src", "Source"); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	legacyLink := pebblestore.WorkspaceReplicationLink{
		ID:                  "legacy-only-link",
		TargetKind:          "managed_host",
		TargetSwarmID:       "legacy-host",
		TargetWorkspacePath: "/legacy-dst",
		ReplicationMode:     "mirror",
		Writable:            true,
	}
	legacyEntry, ok, err := workspaceStore.Get("/src")
	if err != nil || !ok {
		t.Fatalf("get workspace ok=%t err=%v", ok, err)
	}
	legacyEntry.ReplicationLinks = []pebblestore.WorkspaceReplicationLink{legacyLink}
	if err := store.PutJSON(pebblestore.KeyWorkspaceEntry(legacyEntry.Path), legacyEntry); err != nil {
		t.Fatalf("seed legacy replication link: %v", err)
	}

	canonicalBinding, err := topologyStore.PutWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "canonical-binding",
		SourceWorkspacePath:       "/src",
		SourceWorkspaceName:       "Source",
		DestinationRuntimeSwarmID: "canonical-child",
		DestinationWorkspacePath:  "/canonical-dst",
		ReplicationMode:           "mirror",
		Writable:                  true,
	})
	if err != nil {
		t.Fatalf("put canonical workspace binding: %v", err)
	}

	service := NewService(topologyStore, nil, nil, nil, nil, nil, nil, workspaceStore)
	status, err := service.Rebuild()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if status.WorkspaceBindingCount != 1 {
		t.Fatalf("workspace binding count = %d, want 1", status.WorkspaceBindingCount)
	}

	bindings, err := topologyStore.ListWorkspaceBindings(100000)
	if err != nil {
		t.Fatalf("list topology workspace bindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("binding count = %d, want 1: %+v", len(bindings), bindings)
	}
	if bindings[0].BindingID != canonicalBinding.BindingID {
		t.Fatalf("binding id = %q, want %q", bindings[0].BindingID, canonicalBinding.BindingID)
	}
	if bindings[0].DestinationRuntimeSwarmID != canonicalBinding.DestinationRuntimeSwarmID {
		t.Fatalf("destination runtime = %q, want %q", bindings[0].DestinationRuntimeSwarmID, canonicalBinding.DestinationRuntimeSwarmID)
	}
	if bindings[0].DestinationWorkspacePath != canonicalBinding.DestinationWorkspacePath {
		t.Fatalf("destination workspace path = %q, want %q", bindings[0].DestinationWorkspacePath, canonicalBinding.DestinationWorkspacePath)
	}
	if _, ok, err := topologyStore.GetWorkspaceBinding(legacyLink.ID); err != nil || ok {
		t.Fatalf("legacy binding lookup ok=%t err=%v", ok, err)
	}
}
