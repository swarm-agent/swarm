package worktree

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

// Requirement: catalog config reads use exact account-owned roots without a full
// containing-workspace scan. Threat: the optimized read could expose another
// account's config, accept a nested unsaved root, or follow a replaced symlink.
// GetConfigForSavedWorkspaceForPrincipal is the narrow service boundary; compare
// its result with ordinary resolution and assert rejected reads leave config intact.
func TestSavedWorkspaceConfigExactAccountRead(t *testing.T) {
	store, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspaceSvc := workspaceruntime.NewService(pebblestore.NewWorkspaceStore(store))
	configStore := pebblestore.NewWorktreeStore(store)
	svc := NewService(configStore, workspaceSvc, nil)
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}
	repo := initRollbackTestRepository(t)
	if _, err := workspaceSvc.AddForPrincipal(principal, repo, "Repo", "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SetConfigForPrincipal(principal, repo, true, true, "dev", "agent"); err != nil {
		t.Fatal(err)
	}
	want, err := svc.GetConfigForPrincipal(principal, repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetConfigForSavedWorkspaceForPrincipal(principal, repo)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v err=%v", got, want, err)
	}
	before, _, err := configStore.GetConfigForAccount(principal.AccountScopeID, repo)
	if err != nil {
		t.Fatal(err)
	}
	foreign := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "foreign", AccountScopeID: "foreign"}
	if _, err := svc.GetConfigForSavedWorkspaceForPrincipal(foreign, repo); err == nil {
		t.Fatal("foreign read accepted")
	}
	if _, err := svc.GetConfigForSavedWorkspaceForPrincipal(identity.Principal{}, repo); err == nil {
		t.Fatal("missing principal accepted")
	}
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetConfigForSavedWorkspaceForPrincipal(principal, nested); err == nil {
		t.Fatal("unsaved nested root accepted")
	}
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(repo, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetConfigForSavedWorkspaceForPrincipal(principal, repo); err == nil {
		t.Fatal("replaced root accepted")
	}
	after, _, err := configStore.GetConfigForAccount(principal.AccountScopeID, repo)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("reads changed config: before=%+v after=%+v err=%v", before, after, err)
	}
}
