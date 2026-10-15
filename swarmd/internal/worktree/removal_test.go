package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Requirement: removal must quarantine an entire checkout before deleting any
// files, recover interrupted quarantine after restart, and retain other Git
// registrations. Real Git and 33,000 real files exercise the filesystem boundary
// in removeManagedWorktree/ResumeTrash without substituting a Git mock.
func TestRemovalTrashRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	s := &Service{}
	a, err := s.allocateReviewWorkspace(repo, "trash-restart", git("rev-parse", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 33000; i++ {
		if err := os.WriteFile(filepath.Join(a.WorkspacePath, fmt.Sprintf("file-%d", i)), []byte("payload"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Dir(a.WorkspacePath)
	entry := filepath.Join(root, ".trash", filepath.Base(a.WorkspacePath))
	if err := os.MkdirAll(entry, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(removalReceipt{repo, a.WorkspacePath})
	if err := os.WriteFile(filepath.Join(entry, "receipt.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(a.WorkspacePath, filepath.Join(entry, "checkout")); err != nil {
		t.Fatal(err)
	}
	// This is the durable on-disk state at a crash between rename and deletion.
	if _, err := os.Stat(filepath.Join(entry, "checkout", ".git")); err != nil {
		t.Fatal("quarantine was torn", err)
	}
	// Expiring the caller's wait must not kill deletion or restore a torn lane.
	removalMu.Lock()
	err = finishRemovalWithWait(entry, removalReceipt{repo, a.WorkspacePath}, 0)
	removalMu.Unlock()
	if err == nil {
		t.Fatal("short wait unexpectedly completed a large deletion")
	}
	if _, err := os.Lstat(a.WorkspacePath); !os.IsNotExist(err) {
		t.Fatal("live lane retained", err)
	}
	// Recreate the service against the same filesystem, as daemon startup does.
	NewService(nil, nil, nil)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Lstat(entry); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup retained trash")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(git("worktree", "list", "--porcelain"), a.WorkspacePath) {
		t.Fatal("registration retained")
	}
	// A symlink checkout may never be followed or removed as a managed lane.
	outside := t.TempDir()
	if err := os.Symlink(outside, a.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedWorktree(repo, a.WorkspacePath); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("symlink target changed", err)
	}
}
