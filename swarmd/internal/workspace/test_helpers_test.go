package workspace

import (
	"path/filepath"
	"testing"

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
