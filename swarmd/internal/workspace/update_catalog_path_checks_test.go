package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: moving a saved workspace (UpdateCatalogEntryForPrincipal with a
// new path) applies the same path checks as registering one: the path must
// be absolute, outside system and Swarm storage (as requested and once
// symlinks resolve), and not nested inside another registered workspace.
// Threat: a write-level caller (an MCP key through manage_workspace update,
// or a conversation) repointing a workspace into daemon storage or into
// another workspace and then running agents there, while create refuses the
// same folder. Negative cases assert the refusal and that the entry's path
// and generation are unchanged; a valid move, including into a subfolder of
// the workspace's own current folder, still works. Boundary: the workspace
// service, which every caller of a catalog update goes through.
func TestUpdateCatalogEntryAppliesCreatePathChecks(t *testing.T) {
	store, closeStore := newTestWorkspaceStore(t)
	defer closeStore()
	svc := NewService(store)
	p := testPrincipal()
	root := t.TempDir()
	protected := filepath.Join(root, "swarm-data")
	repo := func(rel string) string {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		makeReadyRepository(t, path)
		return path
	}
	svc.SetProtectedRoots(protected)
	moving := repo("moving")
	other := repo("other")
	entry, _, err := store.CreateForAccountIfAbsent(p.AccountScopeID, moving, "Moving", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateForAccountIfAbsent(p.AccountScopeID, other, "Other", ""); err != nil {
		t.Fatal(err)
	}
	insideProtected := repo("swarm-data/repo")
	insideOther := repo("other/nested")
	link := filepath.Join(root, "link-to-protected")
	if err := os.Symlink(insideProtected, link); err != nil {
		t.Fatal(err)
	}
	unchanged := func(stage string) {
		t.Helper()
		got, ok, err := store.GetByWorkspaceIDForAccount(p.AccountScopeID, entry.WorkspaceID)
		if err != nil || !ok || got.Path != entry.Path || got.WorkspaceGeneration != entry.WorkspaceGeneration {
			t.Fatalf("%s changed the entry: %+v (was %+v) %v", stage, got, entry, err)
		}
	}
	for _, tc := range []struct{ path, want string }{
		{insideProtected, "system or Swarm storage"},
		{link, "system or Swarm storage"},
		{insideOther, "inside the registered workspace"},
		{"relative/repo", "absolute path"},
	} {
		_, err := svc.UpdateCatalogEntryForPrincipal(p, entry.WorkspaceID, entry.WorkspaceGeneration, tc.path, nil, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("move to %s: err=%v, want %q", tc.path, err, tc.want)
		}
		unchanged("a refused move to " + tc.path)
	}
	// A valid move works, including into the workspace's own subfolder.
	target := repo("moving/inner")
	moved, err := svc.UpdateCatalogEntryForPrincipal(p, entry.WorkspaceID, entry.WorkspaceGeneration, target, nil, nil)
	if err != nil {
		t.Fatalf("valid move: %v", err)
	}
	if resolved, _ := filepath.EvalSymlinks(target); moved.ResolvedPath != resolved {
		t.Fatalf("valid move went to %q, want %q", moved.ResolvedPath, resolved)
	}
}
