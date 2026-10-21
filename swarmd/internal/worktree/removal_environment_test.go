package worktree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Requirement: removal fixtures must not scan inherited account data. Cloud Build
// preserves XDG_DATA_HOME, which takes precedence over HOME in WorktreeDataDir.
// A real rejected symlink receipt reproduces TestRemovalTrashRestart's leftover
// state without running its large-file scenario. Calling the queue fixture is the
// narrowest way to prove its isolation while keeping resumeTrashRoot's safety
// rejection intact; neither the foreign receipt nor its target may be changed.
func TestRemovalQueueDrainsInheritedDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	source := t.TempDir()
	root, err := worktreeCacheRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "rejected-symlink")
	entry := filepath.Join(root, ".trash", filepath.Base(path))
	if err := os.MkdirAll(entry, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(removalReceipt{source, path})
	if err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(entry, "receipt.json")
	if err := os.WriteFile(receipt, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Run("isolated_queue", TestRemovalQueueDrains)
	if got, err := os.ReadFile(receipt); err != nil || string(got) != string(data) {
		t.Fatalf("foreign receipt changed: %q, %v", got, err)
	}
	if got, err := os.Readlink(path); err != nil || got != outside {
		t.Fatalf("foreign symlink changed: %q, %v", got, err)
	}
	if info, err := os.Stat(outside); err != nil || !info.IsDir() {
		t.Fatalf("foreign target changed: %v", err)
	}
}
