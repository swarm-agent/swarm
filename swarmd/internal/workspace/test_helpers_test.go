package workspace

import (
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newTestWorkspaceStore(t *testing.T) (*pebblestore.WorkspaceStore, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "workspace.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return pebblestore.NewWorkspaceStore(store), func() {
		_ = store.Close()
	}
}

func testPrincipal() identity.Principal {
	return identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user-1", AccountScopeID: "account-1"}
}

func newReadyRepository(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	makeReadyRepository(t, path)
	return path
}

func makeReadyRepository(t *testing.T, path string) {
	t.Helper()
	if _, err := runRepositoryGit(path, "init", "--initial-branch=main"); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	if _, err := runRepositoryGit(path, "-c", "user.name=Swarm Test", "-c", "user.email=swarm-test@localhost", "commit", "--allow-empty", "--no-gpg-sign", "-m", "Initial commit"); err != nil {
		t.Fatalf("create initial commit: %v", err)
	}
}
