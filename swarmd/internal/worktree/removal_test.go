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

// Requirement: the external admin identity survives partial recursive deletion.
// Exercise real Git registrations at each crash boundary; unrelated damaged
// entries must neither block cleanup nor be removed by another job's receipt.
func TestRemovalAdminCrashRecovery(t *testing.T) {
	for _, phase := range []string{"identified", "pointer_removed", "admin_removed"} {
		t.Run(phase, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-b", "dev")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
			a, err := (&Service{}).allocateReviewWorkspace(repo, "admin-crash", git("rev-parse", "HEAD"))
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(a.WorkspacePath, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			admin := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
			entry := filepath.Join(filepath.Dir(a.WorkspacePath), ".trash", filepath.Base(a.WorkspacePath))
			if err := os.MkdirAll(entry, 0700); err != nil {
				t.Fatal(err)
			}
			receipt := removalReceipt{repo, a.WorkspacePath}
			data, _ = json.Marshal(receipt)
			if err := os.WriteFile(filepath.Join(entry, "receipt.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(a.WorkspacePath, filepath.Join(entry, "checkout")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(entry, "admin-entry"), []byte(filepath.Base(admin)), 0600); err != nil {
				t.Fatal(err)
			}
			if phase == "pointer_removed" {
				if err := os.Remove(filepath.Join(admin, "gitdir")); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "admin_removed" {
				if err := os.RemoveAll(admin); err != nil {
					t.Fatal(err)
				}
			}
			unrelated := filepath.Join(filepath.Dir(admin), "unrelated-damaged")
			if err := os.MkdirAll(unrelated, 0700); err != nil {
				t.Fatal(err)
			}
			removalMu.Lock()
			err = finishRemoval(entry, receipt)
			removalMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{entry, admin, a.WorkspacePath} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("retained %s: %v", path, err)
				}
			}
			if _, err := os.Stat(unrelated); err != nil {
				t.Fatal("unrelated entry changed", err)
			}
		})
	}
}

// Requirement: every durable receipt, not just the first available slots, is
// retried by recovery. Real Git/files prove queue saturation cannot strand trash.
func TestRemovalQueueDrains(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	head := git("rev-parse", "HEAD")
	var entries []string
	// Occupy deletion capacity to deterministically exercise the durable queue.
	removalSlots <- struct{}{}
	removalSlots <- struct{}{}
	for i := 0; i < 6; i++ {
		a, err := (&Service{}).allocateReviewWorkspace(repo, fmt.Sprintf("queue-%d", i), head)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(a.WorkspacePath, "payload"), []byte("keep until quarantine"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := removeManagedWorktree(repo, a.WorkspacePath); err == nil {
			t.Fatal("expected queued deletion")
		}
		entries = append(entries, filepath.Join(filepath.Dir(a.WorkspacePath), ".trash", filepath.Base(a.WorkspacePath)))
	}
	<-removalSlots
	<-removalSlots
	// Re-scan the durable queue, as the retry worker does after restart/failure.
	root := filepath.Dir(filepath.Dir(filepath.Dir(entries[0])))
	if err := resumeTrashRoot(root); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := os.Lstat(entry); !os.IsNotExist(err) {
			t.Fatal("stranded receipt", entry, err)
		}
	}
}
