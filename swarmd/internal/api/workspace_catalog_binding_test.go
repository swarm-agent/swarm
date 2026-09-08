package api

import (
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: the catalog creation used by manage_workspace must produce the
// same persisted routing authority as UI Add, without selecting the workspace.
// Boundary: real workspace and topology services wired by SetTopologyService;
// the binding-to-Desktop projection must expose that authority, not invent it.
func TestWorkspaceCatalogCreateProvidesDesktopBinding(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	principal := workspaceAddSelfBindingPrincipal()
	path := t.TempDir()
	if err := ensureTestWorkspaceDir(path); err != nil {
		t.Fatal(err)
	}
	created, err := server.workspace.CreateCatalogEntryForPrincipal(principal, path, "Created", "")
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := topologyStore.ListWorkspaceBindingsForAccount(principal.AccountScopeID, 10)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	binding := bindings[0]
	if created.LocalWorkspaceBindingID == "" || created.LocalWorkspaceBindingID != binding.BindingID || binding.SourceWorkspaceID != created.WorkspaceID || binding.SourceWorkspaceGeneration != created.WorkspaceGeneration || binding.DestinationWorkspacePath != path || binding.State != pebblestore.TopologyWorkspaceBindingStateBound {
		t.Fatalf("creation lacks usable binding: %+v %+v", created, binding)
	}
	entries, err := server.workspace.ListKnownForPrincipal(principal, 10)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := server.workspaceOverviewTopologyRoutesByWorkspace(principal, []swarmTarget{{SwarmID: "local-swarm", Online: true, Selectable: true}}, entries)
	if err != nil {
		t.Fatal(err)
	}
	ids := localWorkspaceBindingIDsByWorkspaceID(entries, routes, "local-swarm")
	if ids[created.WorkspaceID] != binding.BindingID {
		t.Fatalf("Desktop hydration lost binding: %+v routes=%+v", ids, routes)
	}
	if _, selected, err := server.workspace.CurrentBindingForPrincipal(principal); err != nil || selected {
		t.Fatalf("creation changed selection: %v %v", selected, err)
	}
	foreign, err := topologyStore.ListWorkspaceBindingsForAccount("foreign-account", 10)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign bindings=%+v err=%v", foreign, err)
	}
	if _, err := server.workspace.CreateCatalogEntryForPrincipal(principal, path, "Replacement", ""); err == nil {
		t.Fatal("duplicate creation succeeded")
	}
	entry, ok, err := server.workspace.GetByWorkspaceIDForPrincipal(principal, created.WorkspaceID)
	if err != nil || !ok || entry.Name != "Created" {
		t.Fatalf("duplicate mutated entry: %+v %v", entry, err)
	}
	// Re-adding a previously catalog-only workspace is the explicit recovery
	// path: reuse its stable identity and create the missing binding.
	legacyPath := t.TempDir()
	if err := ensureTestWorkspaceDir(legacyPath); err != nil {
		t.Fatal(err)
	}
	legacy, err := server.workspace.AddForPrincipal(principal, legacyPath, "Legacy", "", false)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	response := postWorkspaceAdd(t, server, legacyPath, "Legacy", &payload)
	if response.Code != 200 || payload["workspace_id"] != legacy.WorkspaceID || payload["local_workspace_binding_id"] == "" {
		t.Fatalf("explicit repair failed: %d %+v", response.Code, payload)
	}
}

type failingCatalogBinding struct{}

func (failingCatalogBinding) EnsureLocalSelfPlacementForPrincipal(string, string) (pebblestore.TopologyRuntimePlacementRecord, error) {
	return pebblestore.TopologyRuntimePlacementRecord{}, nil
}
func (failingCatalogBinding) EnsureLocalWorkspaceSelfBindingForPrincipal(string, string, pebblestore.WorkspaceEntry) (pebblestore.TopologyWorkspaceBindingRecord, error) {
	return pebblestore.TopologyWorkspaceBindingRecord{}, errors.New("injected binding failure")
}

// Requirement: unavailable routing must not leave a successful-looking unbound
// catalog entry. Inject failure after catalog persistence and assert rollback,
// unchanged selection and absent binding through the actual creation boundary.
func TestWorkspaceCatalogCreateBindingFailureRollsBack(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	server.workspace.SetLocalBindingService(failingCatalogBinding{})
	path := t.TempDir()
	if err := ensureTestWorkspaceDir(path); err != nil {
		t.Fatal(err)
	}
	if _, err := server.workspace.CreateCatalogEntryForPrincipal(workspaceAddSelfBindingPrincipal(), path, "Failed", ""); err == nil {
		t.Fatal("creation swallowed binding failure")
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
	server.workspace.SetLocalBindingService(nil)
	if _, err := server.workspace.CreateCatalogEntryForPrincipal(workspaceAddSelfBindingPrincipal(), path, "Unconfigured", ""); err == nil {
		t.Fatal("creation without topology succeeded")
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
}
