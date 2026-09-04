package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/workspace"
)

// Requirement: adding a workspace must fail with actionable repository state
// before catalog, topology, or selection mutation. The API layer is the
// narrowest place to prove the structured response and zero side effects.
func TestWorkspaceAddRejectsNonRepositoryBeforeMutation(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "existing.txt"), []byte("untracked user content"), 0o600); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	var payload map[string]any
	rec := postWorkspaceAdd(t, server, workspacePath, "workspace", &payload)
	if rec.Code != http.StatusConflict || payload["code"] != "workspace_repository_not_ready" {
		t.Fatalf("status=%d payload=%#v", rec.Code, payload)
	}
	repository, _ := payload["repository"].(map[string]any)
	if repository["state"] != workspace.RepositoryStateNeedsAssistedSetup || repository["needs_review"] != true || repository["can_setup"] != false {
		t.Fatalf("repository state=%#v", repository)
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
	placements, err := topologyStore.ListRuntimePlacementsForAccount(workspaceAddSelfBindingPrincipal().AccountScopeID, 10)
	if err != nil || len(placements) != 1 || placements[0].RuntimeSwarmID != "local-swarm" || placements[0].PlacementGeneration != 1 {
		t.Fatalf("repository failure changed topology placements: placements=%+v err=%v", placements, err)
	}
	if _, err := os.Lstat(filepath.Join(workspacePath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("failed add changed filesystem: %v", err)
	}
}

// Requirement: selecting a saved workspace must revalidate its committed repository.
// Threat: repository drift could otherwise reopen an unsupported direct-session path.
// Boundary: the authenticated select handler must return typed repository state without changing selection.
func TestWorkspaceSelectRejectsRepositoryDriftBeforeSelectionMutation(t *testing.T) {
	server, _ := newWorkspaceAddSelfBindingTestServer(t, true)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := ensureTestWorkspaceDir(workspacePath); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	principal := workspaceAddSelfBindingPrincipal()
	if _, err := server.workspace.AddForPrincipal(principal, workspacePath, "workspace", "", false); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	gitPath := filepath.Join(workspacePath, ".git")
	removedGitPath := filepath.Join(t.TempDir(), "removed-git")
	if err := os.Rename(gitPath, removedGitPath); err != nil {
		t.Fatalf("remove repository metadata: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"path": workspacePath})
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v1/workspace/select", bytes.NewReader(body)), "workspace-user", "workspace-account")
	rec := httptest.NewRecorder()
	server.handleWorkspaceSelect(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("select status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode select response: %v", err)
	}
	if payload["code"] != "workspace_repository_not_ready" {
		t.Fatalf("select payload=%#v", payload)
	}
	if _, selected, err := server.workspace.CurrentBindingForPrincipal(principal); err != nil || selected {
		t.Fatalf("failed select changed current workspace: selected=%v err=%v", selected, err)
	}
}

func TestWorkspaceRepositorySetupRejectsNonEmptyDirectoryWithoutMutation(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	userFile := filepath.Join(workspacePath, "private.txt")
	if err := os.WriteFile(userFile, []byte("do not stage"), 0o600); err != nil {
		t.Fatalf("write user file: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"path": workspacePath, "expected_resolved_path": workspacePath})
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v1/workspace/repository/setup", bytes.NewReader(body)), "workspace-user", "workspace-account")
	rec := httptest.NewRecorder()
	server.handleWorkspaceRepositorySetup(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("setup status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode setup response: %v", err)
	}
	repository := payload["repository"].(map[string]any)
	if repository["state"] != workspace.RepositoryStateNeedsAssistedSetup || repository["needs_review"] != true {
		t.Fatalf("repository=%#v", repository)
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
	if _, err := os.Lstat(filepath.Join(workspacePath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("rejected setup created .git: %v", err)
	}
	contents, err := os.ReadFile(userFile)
	if err != nil || string(contents) != "do not stage" {
		t.Fatalf("rejected setup changed user file: %q err=%v", contents, err)
	}
}

func TestWorkspaceRepositorySetupCreatesEmptyCommitWithoutSavingWorkspace(t *testing.T) {
	server, topologyStore := newWorkspaceAddSelfBindingTestServer(t, true)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"path": workspacePath, "expected_resolved_path": workspacePath})
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, "/v1/workspace/repository/setup", bytes.NewReader(body)), "workspace-user", "workspace-account")
	rec := httptest.NewRecorder()
	server.handleWorkspaceRepositorySetup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode setup response: %v", err)
	}
	repository := payload["repository"].(map[string]any)
	if repository["state"] != workspace.RepositoryStateReady || repository["head_commit"] == "" {
		t.Fatalf("repository=%#v", repository)
	}
	assertNoWorkspaceAddSideEffects(t, server, topologyStore)
	var addPayload map[string]any
	if addRec := postWorkspaceAdd(t, server, workspacePath, "workspace", &addPayload); addRec.Code != http.StatusOK {
		t.Fatalf("add after setup status=%d body=%s", addRec.Code, addRec.Body.String())
	}
}

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
	if binding.DestinationRuntimeSwarmID != "local-swarm" || binding.DestinationAuthorityHostSwarmID != "local-swarm" || binding.DestinationHostSwarmID != "local-swarm" {
		t.Fatalf("unexpected destination binding: %+v", binding)
	}
	if binding.DestinationRuntimeKind != pebblestore.TopologyRuntimeKindHost || binding.State != pebblestore.TopologyWorkspaceBindingStateBound || binding.AccessMode != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || binding.MaterializationKind != pebblestore.TopologyWorkspaceBindingMaterializationSource || binding.PlacementGeneration != 1 || binding.BindingGeneration != 1 {
		t.Fatalf("unexpected binding defaults: %+v", binding)
	}
	if binding.DestinationWorkspacePath != workspacePath || binding.AttestedByHostSwarmID != "local-swarm" {
		t.Fatalf("unexpected materialization/attestation: %+v", binding)
	}

	entries, err := server.workspace.ListKnownForPrincipal(workspaceAddSelfBindingPrincipal(), 10)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("workspaces len=%d: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.DefinitionStatus != "" || entry.DefinitionGeneration != 0 || entry.Definition != "" || entry.DefinitionError != "" {
		t.Fatalf("workspace add initialized definition lifecycle: %+v", entry)
	}
	if _, ok := workspacePayload["definition_status"]; ok {
		t.Fatalf("workspace response unexpectedly includes definition status: %#v", workspacePayload)
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
	server.SetTopologyService(topologyruntime.NewService(topologyStore, swarmStore))
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
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	if output, err := exec.Command("git", "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", path, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@localhost", "commit", "--allow-empty", "--no-gpg-sign", "-m", "Initial commit").CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, output)
	}
	return nil
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
