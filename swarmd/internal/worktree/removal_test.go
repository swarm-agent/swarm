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
	// XDG_DATA_HOME overrides HOME; never scan another test's durable trash.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
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
	recovery := NewService(nil, nil, nil)
	defer recovery.Close()
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
			// XDG_DATA_HOME overrides HOME; never scan another test's durable trash.
			t.Setenv("XDG_DATA_HOME", t.TempDir())
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
			// Legacy Desktop worktrees still need registration crash recovery;
			// reviewer allocation now creates a clone with no registration.
			path, err := deterministicSessionWorktreePath(repo, sessionWorkspaceID("admin-crash"))
			if err != nil {
				t.Fatal(err)
			}
			if err := ensureWorktreeParent(repo); err != nil {
				t.Fatal(err)
			}
			git("worktree", "add", "--detach", path, "HEAD")
			a := Allocation{WorkspacePath: path}
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
	// XDG_DATA_HOME overrides HOME; never scan another test's durable trash.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
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

// Requirement: service-owned periodic recovery retries a failed deletion without
// a caller rescan. Real Git/files and an unavailable source repository exercise
// the transient registration failure boundary for retained Desktop/plan worktrees
// in finishRemovalWithWait; Close must stop the worker. Worker clones have no
// source registration dependency (tested separately below).
func TestTrashRecoveryTickerRetriesFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// XDG_DATA_HOME overrides HOME; never scan another test's durable trash.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
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
	root, err := worktreeCacheRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	// Deliberately use a linked worktree: review allocation now returns a clone.
	a := struct{ WorkspacePath string }{filepath.Join(root, "ticker-retry")}
	git("worktree", "add", "--detach", a.WorkspacePath, "HEAD")
	entry := filepath.Join(filepath.Dir(a.WorkspacePath), ".trash", filepath.Base(a.WorkspacePath))
	if err := os.MkdirAll(entry, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(removalReceipt{repo, a.WorkspacePath})
	if err := os.WriteFile(filepath.Join(entry, "receipt.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo, repo+"-offline"); err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	s.startTrashRecovery(filepath.Dir(filepath.Dir(a.WorkspacePath)), 20*time.Millisecond)
	defer s.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(entry, "renamed")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial recovery did not attempt deletion")
		}
		time.Sleep(time.Millisecond)
	}
	// Serialize with the first failed pass before restoring the dependency.
	func() {
		removalMu.Lock()
		defer removalMu.Unlock()
		if _, err := os.Stat(filepath.Join(entry, "receipt.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(entry, "checkout", ".git")); err != nil {
			t.Fatal("failed registration cleanup must retain checkout:", err)
		}
		if err := os.Rename(repo+"-offline", repo); err != nil {
			t.Fatal(err)
		}
	}()
	for {
		if _, err := os.Lstat(entry); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ticker stranded failed receipt")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Close()
	select {
	case <-s.trashDone:
	default:
		t.Fatal("recovery did not stop")
	}
}

// Requirement: durable trash recovery must delete a full job clone (made before
// worker jobs moved to worktrees of the folder) without consulting the source
// repository. Real Git/files exercise resumeTrashRoot and
// finishRemovalWithWait with an unavailable source, preventing restoration of a
// shared-registration dependency while asserting both checkout and receipt removal.
func TestTrashRecoveryCloneWithOfflineSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// XDG_DATA_HOME overrides HOME; never scan another test's durable trash.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
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
	// A job clone left in the managed directory by an earlier Swarm.
	path, err := deterministicSessionWorktreePath(repo, sessionWorkspaceID("offline-clone"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktreeParent(repo); err != nil {
		t.Fatal(err)
	}
	git("clone", "-q", "--no-local", repo, path)
	a := Allocation{WorkspacePath: path}
	info, err := os.Stat(filepath.Join(a.WorkspacePath, ".git"))
	if err != nil || !info.IsDir() {
		t.Fatal("expected independent clone", err)
	}
	entry := filepath.Join(filepath.Dir(a.WorkspacePath), ".trash", filepath.Base(a.WorkspacePath))
	if err := os.MkdirAll(entry, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(removalReceipt{repo, a.WorkspacePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "receipt.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	// Move within the temporary directory so test cleanup also owns offline data.
	offline := filepath.Join(t.TempDir(), "offline")
	if err := os.Rename(repo, offline); err != nil {
		t.Fatal(err)
	}
	if err := resumeTrashRoot(filepath.Dir(filepath.Dir(a.WorkspacePath))); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{a.WorkspacePath, entry} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup left %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(offline, ".git")); err != nil {
		t.Fatal("cleanup changed source repository:", err)
	}
}
