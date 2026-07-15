package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/workspace"
)

func TestWorkspaceAddCreatesLocalSelfBinding(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := ensureTestWorkspaceDir(workspacePath); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	var payload map[string]any
	rec := postWorkspaceAdd(t, server, workspacePath, "workspace", &payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace add status=%d body=%s", rec.Code, rec.Body.String())
	}
	workspacePayload := payload["workspace"].(map[string]any)
	workspaceID, _ := workspacePayload["workspace_id"].(string)
	bindingID, _ := workspacePayload["local_workspace_binding_id"].(string)
	if workspaceID == "" || bindingID == "" || payload["workspace_id"] != workspaceID || payload["local_workspace_binding_id"] != bindingID {
		t.Fatalf("missing workspace/binding ids: %#v", payload)
	}

	bindings, err := topologyStore.ListWorkspaceBindingsForAccount(workspaceAddSelfBindingPrincipal().AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings len=%d: %+v", len(bindings), bindings)
	}
	binding := bindings[0]
	if binding.BindingID != bindingID || binding.SourceWorkspaceID != workspaceID || binding.SourceWorkspaceGeneration != 1 {
		t.Fatalf("unexpected source binding: %+v", binding)
	}
	if binding.DestinationRuntimeSwarmID != "local-swarm" || binding.DestinationAuthorityHostSwarmID != "local-swarm" || binding.DestinationHostSwarmID != "local-swarm" || binding.DestinationContainerID != "" {
		t.Fatalf("unexpected destination binding: %+v", binding)
	}
	if binding.DestinationRuntimeKind != pebblestore.TopologyRuntimeKindHost || binding.State != pebblestore.TopologyWorkspaceBindingStateBound || binding.AccessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || binding.MaterializationKind != pebblestore.TopologyWorkspaceBindingMaterializationSource || binding.PlacementGeneration != 1 || binding.BindingGeneration != 1 {
		t.Fatalf("unexpected binding defaults: %+v", binding)
	}
	if binding.DestinationWorkspacePath != workspacePath || binding.AttestedByHostSwarmID != "local-swarm" {
		t.Fatalf("unexpected materialization/attestation: %+v", binding)
	}
}

func TestWorkspaceAddDuplicateReusesLocalSelfBinding(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := ensureTestWorkspaceDir(workspacePath); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	var first map[string]any
	if rec := postWorkspaceAdd(t, server, workspacePath, "workspace", &first); rec.Code != http.StatusOK {
		t.Fatalf("first add status=%d body=%s", rec.Code, rec.Body.String())
	}
	firstBindingID := first["local_workspace_binding_id"].(string)
	firstWorkspaceID := first["workspace_id"].(string)
	var second map[string]any
	if rec := postWorkspaceAdd(t, server, workspacePath, "workspace", &second); rec.Code != http.StatusOK {
		t.Fatalf("second add status=%d body=%s", rec.Code, rec.Body.String())
	}
	if second["local_workspace_binding_id"] != firstBindingID || second["workspace_id"] != firstWorkspaceID {
		t.Fatalf("duplicate add ids binding=%v workspace=%v want binding=%s workspace=%s", second["local_workspace_binding_id"], second["workspace_id"], firstBindingID, firstWorkspaceID)
	}
	bindings, err := topologyStore.ListWorkspaceBindingsForAccount(workspaceAddSelfBindingPrincipal().AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 1 || bindings[0].BindingID != firstBindingID {
		t.Fatalf("duplicate add created ambiguous bindings: %+v", bindings)
	}
}

func TestWorkspaceAddFailsAndRollsBackWhenSelfPlacementMissing(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, false)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := ensureTestWorkspaceDir(workspacePath); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	var payload map[string]any
	rec := postWorkspaceAdd(t, server, workspacePath, "workspace", &payload)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "local swarm id is required for self placement") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
}

func TestWorkspaceAddFailureLeavesExistingCurrentSelection(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, false)
	principal := workspaceAddSelfBindingPrincipal()
	currentPath := filepath.Join(t.TempDir(), "current")
	if err := ensureTestWorkspaceDir(currentPath); err != nil {
		t.Fatalf("create current workspace dir: %v", err)
	}
	if _, err := server.workspace.AddForPrincipal(principal, currentPath, "current", "", true); err != nil {
		t.Fatalf("seed current workspace: %v", err)
	}
	beforeEntries, err := server.workspace.ListKnownForPrincipal(principal, 10)
	if err != nil {
		t.Fatalf("list workspaces before add: %v", err)
	}
	if len(beforeEntries) != 1 || beforeEntries[0].Path != currentPath || !beforeEntries[0].Active {
		t.Fatalf("seed current workspace not active before failed add: %+v", beforeEntries)
	}

	newWorkspacePath := filepath.Join(t.TempDir(), "new-workspace")
	if err := ensureTestWorkspaceDir(newWorkspacePath); err != nil {
		t.Fatalf("create new workspace dir: %v", err)
	}
	var payload map[string]any
	rec := postWorkspaceAdd(t, server, newWorkspacePath, "new-workspace", &payload)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "local swarm id is required for self placement") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	entries, err := server.workspace.ListKnownForPrincipal(principal, 10)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != currentPath || !entries[0].Active {
		t.Fatalf("failed add changed workspace/current state: %+v", entries)
	}
	bindings, err := topologyStore.ListWorkspaceBindingsForAccount(principal.AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("unexpected bindings after failure: %+v", bindings)
	}
}

func newWorkspaceAddSelfBindingTestServer(t *testing.T, withLocalNode bool) (*Server, *pebblestore.TopologyStore) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace-add.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspaceStore := pebblestore.NewWorkspaceStore(store)
	workspaceSvc := workspace.NewService(workspaceStore)
	server := NewServer(nil, nil, nil, nil, nil, workspaceSvc, nil, nil, nil, nil, nil, nil, stream.NewHub(nil))
	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store)
	if withLocalNode {
		if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "local-swarm", Name: "Local", Role: "host"}); err != nil {
			t.Fatalf("put local node: %v", err)
		}
		if _, err := topologyStore.PutRuntimePlacementForAccount(workspaceAddSelfBindingPrincipal().AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "local-swarm", AccountScopeID: workspaceAddSelfBindingPrincipal().AccountScopeID, AuthorityHostSwarmID: "local-swarm", RuntimeKind: pebblestore.TopologyRuntimeKindHost, State: pebblestore.TopologyRuntimePlacementStateActive, PlacementGeneration: 1}); err != nil {
			t.Fatalf("put local placement: %v", err)
		}
	}
	server.SetTopologyService(topologyruntime.NewService(topologyStore, swarmStore, nil, nil, workspaceStore))
	return server, topologyStore
}

func assertNoWorkspaceAddSideEffects(t *testing.T, server *Server, topologyStore *pebblestore.TopologyStore) {
	t.Helper()
	principal := workspaceAddSelfBindingPrincipal()
	entries, err := server.workspace.ListKnownForPrincipal(principal, 10)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace add left unbound workspace after failure: %+v", entries)
	}
	current, ok, err := server.workspace.CurrentBindingForPrincipal(principal)
	if err != nil {
		t.Fatalf("current workspace: %v", err)
	}
	if ok {
		t.Fatalf("workspace add left current selection after failure: %+v", current)
	}
	bindings, err := topologyStore.ListWorkspaceBindingsForAccount(principal.AccountScopeID, 10)
	if err != nil {
		t.Fatalf("list bindings: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("unexpected bindings after failure: %+v", bindings)
	}
}

func ensureTestWorkspaceDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func postWorkspaceAdd(t *testing.T, server *Server, workspacePath, name string, out *map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"path": workspacePath, "name": name})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v1/workspace/add", bytes.NewReader(body)), "workspace-user", "workspace-account")
	rec := httptest.NewRecorder()
	server.handleWorkspaceAdd(rec, req)
	if out != nil && rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), out)
	}
	return rec
}

func workspaceAddSelfBindingPrincipal() identity.Principal {
	return identity.Principal{Type: identity.PrincipalTypeUser, UserID: "workspace-user", AccountScopeID: "workspace-account", AccountScopeSource: identity.AccountScopeSourceServerState}
}
