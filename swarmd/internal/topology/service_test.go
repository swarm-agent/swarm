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

	if _, err := workspaceStore.AddForAccount("account-1", "/src", "Source"); err != nil {
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
	legacyEntry, ok, err := workspaceStore.GetForAccount("account-1", "/src")
	if err != nil || !ok {
		t.Fatalf("get workspace ok=%t err=%v", ok, err)
	}
	legacyEntry.ReplicationLinks = []pebblestore.WorkspaceReplicationLink{legacyLink}
	if err := store.PutJSON(pebblestore.KeyWorkspaceEntryForAccount("account-1", legacyEntry.Path), legacyEntry); err != nil {
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

func TestSessionRouteEnrichmentUsesExplicitWorkspaceBindingID(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-session-route-owner.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	sessionRouteStore := pebblestore.NewSessionRouteStore(store)
	deployStore := pebblestore.NewDeployContainerStore(store)

	if _, err := deployStore.Put(pebblestore.DeployContainerRecord{
		ID:              "deployment-1",
		Name:            "Child",
		Status:          "running",
		AttachStatus:    "attached",
		ContainerName:   "container-1",
		ChildSwarmID:    "child-swarm",
		HostSwarmID:     "host-swarm",
		HostContainerID: "host-swarm:container-1",
	}); err != nil {
		t.Fatalf("put deployment: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding:replica:deployment-1",
		SourceWorkspacePath:       "/src",
		DestinationRuntimeSwarmID: "child-swarm",
		DestinationWorkspacePath:  "/workspaces/src",
		ReplicationMode:           "mirror",
		Writable:                  true,
	}); err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}
	if _, err := sessionRouteStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            "session-1",
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:3900",
		HostWorkspacePath:    "/src",
		RuntimeWorkspacePath: "/workspaces/src",
		WorkspaceBindingID:   "binding:replica:deployment-1",
	}); err != nil {
		t.Fatalf("put session route: %v", err)
	}

	service := NewService(topologyStore, nil, nil, nil, deployStore, nil, sessionRouteStore, nil)
	if _, err := service.Rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	route, ok, err := topologyStore.GetSessionRoute("session-1")
	if err != nil || !ok {
		t.Fatalf("get topology session route ok=%t err=%v", ok, err)
	}
	if route.HostSwarmID != "host-swarm" {
		t.Fatalf("host swarm id = %q, want %q", route.HostSwarmID, "host-swarm")
	}
	if route.HostContainerID != "host-swarm:container-1" {
		t.Fatalf("host container id = %q, want %q", route.HostContainerID, "host-swarm:container-1")
	}
	if route.WorkspaceBindingID != "binding:replica:deployment-1" {
		t.Fatalf("workspace binding id = %q, want %q", route.WorkspaceBindingID, "binding:replica:deployment-1")
	}
}

func TestSessionRouteEnrichmentDoesNotInferWorkspaceBindingIDFromPaths(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-session-route-no-path-infer.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	sessionRouteStore := pebblestore.NewSessionRouteStore(store)

	if _, err := topologyStore.PutWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding:replica:path-match",
		SourceWorkspacePath:       "/src",
		DestinationRuntimeSwarmID: "child-swarm",
		DestinationWorkspacePath:  "/workspaces/src",
		DestinationHostSwarmID:    "host-swarm-from-binding",
	}); err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}
	if _, err := sessionRouteStore.Put(pebblestore.SessionRouteRecord{
		SessionID:            "session-legacy-path-only",
		ChildSwarmID:         "child-swarm",
		ChildBackendURL:      "http://127.0.0.1:3900",
		HostWorkspacePath:    "/src",
		RuntimeWorkspacePath: "/workspaces/src",
	}); err != nil {
		t.Fatalf("put session route: %v", err)
	}

	service := NewService(topologyStore, nil, nil, nil, nil, nil, sessionRouteStore, nil)
	if _, err := service.Rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	route, ok, err := topologyStore.GetSessionRoute("session-legacy-path-only")
	if err != nil || !ok {
		t.Fatalf("get topology session route ok=%t err=%v", ok, err)
	}
	if route.WorkspaceBindingID != "" {
		t.Fatalf("workspace binding id = %q, want no path-inferred binding", route.WorkspaceBindingID)
	}
	if route.HostSwarmID == "host-swarm-from-binding" {
		t.Fatalf("host swarm id = %q, want no binding-derived enrichment", route.HostSwarmID)
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

	if _, err := workspaceStore.AddForAccount("account-1", "/src", "Source"); err != nil {
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
	legacyEntry, ok, err := workspaceStore.GetForAccount("account-1", "/src")
	if err != nil || !ok {
		t.Fatalf("get workspace ok=%t err=%v", ok, err)
	}
	legacyEntry.ReplicationLinks = []pebblestore.WorkspaceReplicationLink{legacyLink}
	if err := store.PutJSON(pebblestore.KeyWorkspaceEntryForAccount("account-1", legacyEntry.Path), legacyEntry); err != nil {
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
