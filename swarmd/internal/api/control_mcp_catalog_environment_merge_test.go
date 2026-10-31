package api

import (
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
)

// Purpose:
//   - Requirement: manage_projects update_environment always merges (only
//     the fields given change), whether the folder is named by
//     workspace_path or the swarm_* way (project, workspace); a full replace
//     needs replace: true with base_image, and clears setup and env not
//     given.
//     The declared description says exactly this.
//   - Threat/regression: a swarm_*-shaped update of one field
//     ({project, env}) going to swarm_set_environment's PUT and silently
//     wiping the folder's image and setup.
//   - Boundary/authority: catalogUpdateEnvironment (PATCH route,
//     UpdateSandboxEnvironment) and ManageFolderEnvironment
//     (UpdateSandboxEnvironment, or replaceSandboxEnvironment for replace);
//     the folder's SandboxEnvironmentStore record is the observable state.
//   - Narrowest layer: Swarm Control's tools/call on the in-process harness
//     with the real environment store and a fake image builder (no Docker).
func TestUpdateEnvironmentMergesUnlessReplaceIsTrue(t *testing.T) {
	h := newCatalogParityHarness(t)
	if h.home.s.workspace == nil {
		h.home.s.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(h.home.s.sessions.Store().Underlying()))
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: h.home.user, AccountScopeID: h.home.account}
	added, err := h.home.s.workspace.AddForPrincipal(p, folder, "env", "", false)
	if err != nil {
		t.Fatal(err)
	}
	store := h.home.s.sessions.Store()
	if err := store.PutProject(h.home.account, &pebblestore.ProjectRecord{ID: "env-project", AccountID: h.home.account, Name: "Env project", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	envs := pebblestore.NewSandboxEnvironmentStore(store.Underlying())
	h.home.s.SetSandboxEnvironmentStore(envs)
	h.home.s.envBuilder = &fakeEnvironmentBuilder{}
	definition := func() pebblestore.SandboxEnvironmentDefinition {
		t.Helper()
		env, found, err := envs.Get(folder)
		if err != nil || !found {
			t.Fatalf("environment: %v %v", found, err)
		}
		return env.Definition
	}
	update := func(args map[string]any) string {
		t.Helper()
		call := map[string]any{"action": "update_environment", "wait_seconds": float64(5)}
		for key, value := range args {
			call[key] = value
		}
		_, refused := h.controlTool(t, catalogParityFullKey, "manage_projects", call)
		return refused
	}

	if refused := update(map[string]any{"workspace_path": folder, "base_image": "img:1", "setup": []any{"make"}, "env": map[string]any{"A": "1", "B": "2"}}); refused != "" {
		t.Fatalf("define: %s", refused)
	}
	want := pebblestore.SandboxEnvironmentDefinition{BaseImage: "img:1", Setup: []string{"make"}, Env: map[string]string{"A": "1", "B": "2"}}
	if got := definition(); !reflect.DeepEqual(got, want) {
		t.Fatalf("defined %+v, want %+v", got, want)
	}

	// The swarm_* shape, one field (the finding's own call): a merge.
	if refused := update(map[string]any{"project": "env-project", "environment": map[string]any{"env": map[string]any{"A": "9"}}}); refused != "" {
		t.Fatalf("swarm_*-shaped update: %s", refused)
	}
	want.Env = map[string]string{"A": "9", "B": "2"}
	if got := definition(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a swarm_*-shaped update of env changed other fields: %+v, want %+v", got, want)
	}
	// The workspace_path shape, one field: the same merge.
	if refused := update(map[string]any{"workspace_path": folder, "setup": []any{"make", "make test"}}); refused != "" {
		t.Fatalf("workspace_path update: %s", refused)
	}
	want.Setup = []string{"make", "make test"}
	if got := definition(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a workspace_path update of setup changed other fields: %+v, want %+v", got, want)
	}

	// replace with a merge-only input is refused, and changes nothing.
	for _, args := range []map[string]any{
		{"project": "env-project", "replace": true, "append_setup": []any{"x"}},
		{"workspace_path": folder, "replace": true, "base_image": "img:9", "unset_env": []any{"A"}},
		{"project": "env-project", "replace": true, "env": map[string]any{"C": "3"}},
		{"workspace_path": folder, "replace": true, "setup": []any{}},
	} {
		if refused := update(args); !strings.Contains(refused, "does not go with it") && !strings.Contains(refused, "needs base_image") {
			t.Fatalf("replace with a merge input was not refused: %q", refused)
		}
	}
	if got := definition(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a refused replace changed the definition: %+v", got)
	}

	// replace: true in either shape sets the whole definition.
	if refused := update(map[string]any{"project": "env-project", "replace": true, "base_image": "img:3", "env": map[string]any{"C": "3"}}); refused != "" {
		t.Fatalf("swarm_*-shaped replace: %s", refused)
	}
	if got := definition(); got.BaseImage != "img:3" || len(got.Setup) != 0 || !reflect.DeepEqual(got.Env, map[string]string{"C": "3"}) {
		t.Fatalf("a swarm_*-shaped replace kept fields not given: %+v", got)
	}
	if refused := update(map[string]any{"workspace_path": folder, "replace": true, "base_image": "img:2"}); refused != "" {
		t.Fatalf("workspace_path replace: %s", refused)
	}
	if got := definition(); got.BaseImage != "img:2" || len(got.Setup) != 0 || len(got.Env) != 0 {
		t.Fatalf("a workspace_path replace kept fields not given: %+v", got)
	}

	// The declared description states the merge and the replace.
	catalog, _ := tool.CatalogToolNamed("manage_projects")
	declared, _ := catalog.Action(tool.FolderEnvironmentUpdate)
	if declared.Description != "Define or change a folder's environment. It merges: only the fields given change, the rest stay. replace: true sets the whole definition instead: base_image is required, and setup and env not given are cleared." || !containsString(declared.Inputs, "replace") {
		t.Fatalf("update_environment's declaration does not state merge and replace: %q %v", declared.Description, declared.Inputs)
	}
}
