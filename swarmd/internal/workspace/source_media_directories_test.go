package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// TestLaneA_E2E010SourceMediaDirectoriesRemainOutsideWorkspaceScope covers
// E2E-010/REQ-LNK-001/REQ-PATH-001: source-media metadata never becomes
// generic workspace authorization.
func TestLaneA_E2E010SourceMediaDirectoriesRemainOutsideWorkspaceScope(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	principal := testPrincipal()
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	sourcePath := filepath.Join(root, "source")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	if _, err := service.AddForPrincipal(principal, workspacePath, "Workspace", "", false); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	resolution, err := service.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, sourcePath)
	if err != nil {
		t.Fatalf("add source media directory: %v", err)
	}
	if !reflect.DeepEqual(resolution.SourceMediaDirectories, []string{sourcePath}) {
		t.Fatalf("source media directories = %v, want %q", resolution.SourceMediaDirectories, sourcePath)
	}

	scope, err := service.ScopeForWorkspaceForPrincipal(principal, workspacePath)
	if err != nil {
		t.Fatalf("workspace scope: %v", err)
	}
	if !reflect.DeepEqual(scope.Directories, []string{workspacePath}) {
		t.Fatalf("scope directories = %v, source directory must remain excluded", scope.Directories)
	}
	outside, err := service.ScopeForPathForPrincipal(principal, filepath.Join(sourcePath, "video.mp4"))
	if err != nil {
		t.Fatalf("resolve source path scope: %v", err)
	}
	if outside.Matched {
		t.Fatalf("source media path unexpectedly matched generic workspace scope: %+v", outside)
	}

	current, ok, err := service.GetByWorkspaceIDForPrincipal(principal, resolution.WorkspaceID)
	if err != nil || !ok {
		t.Fatalf("get workspace entry ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(current.Directories, []string{workspacePath}) {
		t.Fatalf("persisted directories = %v, source directory must remain excluded", current.Directories)
	}
	if !reflect.DeepEqual(current.SourceMediaDirectories, []string{sourcePath}) {
		t.Fatalf("persisted source media directories = %v", current.SourceMediaDirectories)
	}
	removed, err := service.RemoveSourceMediaDirectoryForPrincipal(principal, workspacePath, sourcePath)
	if err != nil {
		t.Fatalf("remove source media directory: %v", err)
	}
	if len(removed.SourceMediaDirectories) != 0 {
		t.Fatalf("source media directories after remove = %v", removed.SourceMediaDirectories)
	}
}

// TestLaneA_E2E008_E2E009CompatibilityAddRemoveCreatesDeletesFlatWorkspace
// covers compatibility add/remove behavior without linked membership authority.
func TestLaneA_E2E008_E2E009CompatibilityAddRemoveCreatesDeletesFlatWorkspace(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	principal := testPrincipal()
	parent := t.TempDir()
	target := t.TempDir()

	parentResolution, err := service.AddForPrincipal(principal, parent, "Parent", "", true)
	if err != nil {
		t.Fatalf("add parent workspace: %v", err)
	}
	added, err := service.AddDirectoryForPrincipal(principal, parent, target)
	if err != nil {
		t.Fatalf("compatibility add directory: %v", err)
	}
	if added.WorkspacePath != target || added.WorkspaceID == "" || added.WorkspaceGeneration <= 0 {
		t.Fatalf("compatibility add did not create a flat workspace: %+v", added)
	}
	entries, err := service.ListKnownForPrincipal(principal, 10)
	if err != nil {
		t.Fatalf("list after compatibility add: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("workspace count after compatibility add=%d, want 2: %+v", len(entries), entries)
	}
	parentEntry, ok, err := service.GetByWorkspaceIDForPrincipal(principal, parentResolution.WorkspaceID)
	if err != nil || !ok {
		t.Fatalf("get parent after compatibility add: ok=%t err=%v", ok, err)
	}
	if !reflect.DeepEqual(parentEntry.Directories, []string{parent}) {
		t.Fatalf("compatibility add restored parent linked membership: %+v", parentEntry.Directories)
	}

	if _, err := service.RemoveDirectoryForPrincipal(principal, parent, target); err != nil {
		t.Fatalf("compatibility remove directory: %v", err)
	}
	if _, ok, err := service.GetByWorkspaceIDForPrincipal(principal, added.WorkspaceID); err != nil || ok {
		t.Fatalf("compatibility remove left target workspace: ok=%t err=%v", ok, err)
	}
	parentAfter, ok, err := service.GetByWorkspaceIDForPrincipal(principal, parentResolution.WorkspaceID)
	if err != nil || !ok || parentAfter.WorkspaceID != parentResolution.WorkspaceID || !reflect.DeepEqual(parentAfter.Directories, []string{parent}) {
		t.Fatalf("compatibility remove mutated parent: ok=%t err=%v entry=%+v", ok, err, parentAfter)
	}
}

func TestSourceMediaDirectoriesSurviveStoreReopen(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "workspace.pebble")
	workspacePath := filepath.Join(root, "workspace")
	sourcePath := filepath.Join(root, "source")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	store, err := pebblestore.Open(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	principal := testPrincipal()
	service := NewService(pebblestore.NewWorkspaceStore(store))
	if _, err := service.AddForPrincipal(principal, workspacePath, "Workspace", "", false); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if _, err := service.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, sourcePath); err != nil {
		t.Fatalf("add source media directory: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := pebblestore.Open(storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	resolution, err := NewService(pebblestore.NewWorkspaceStore(reopened)).ListSourceMediaDirectoriesForPrincipal(principal, workspacePath)
	if err != nil {
		t.Fatalf("list source media directories after reopen: %v", err)
	}
	if !reflect.DeepEqual(resolution.SourceMediaDirectories, []string{sourcePath}) {
		t.Fatalf("source media directories after reopen = %v, want %q", resolution.SourceMediaDirectories, sourcePath)
	}
}

func TestLegacyWorkspaceEntryNormalizesEmptySourceMediaDirectories(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	principal := testPrincipal()
	workspacePath := t.TempDir()
	if _, err := store.SaveForAccount(principal.AccountScopeID, workspacePath, "Workspace", "", false); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	entry, ok, err := store.GetForAccount(principal.AccountScopeID, workspacePath)
	if err != nil || !ok {
		t.Fatalf("get workspace ok=%v err=%v", ok, err)
	}
	if entry.SourceMediaDirectories != nil {
		t.Fatalf("legacy zero value normalized to %v, want nil", entry.SourceMediaDirectories)
	}
}

func TestSourceMediaDirectoryMustExistAndBeDirectory(t *testing.T) {
	store, cleanup := newTestWorkspaceStore(t)
	defer cleanup()
	service := NewService(store)
	principal := testPrincipal()
	workspacePath := t.TempDir()
	if _, err := service.AddForPrincipal(principal, workspacePath, "Workspace", "", false); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	filePath := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := service.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, filePath); err == nil {
		t.Fatal("adding a source media file unexpectedly succeeded")
	}
	if _, err := service.AddSourceMediaDirectoryForPrincipal(principal, workspacePath, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("adding a missing source media directory unexpectedly succeeded")
	}
}
