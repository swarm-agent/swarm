package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Purpose: Git the daemon routes into a sandbox (RouteGit: the container
// path's execArgs and routeLocalGit's localArgv) runs under gitUmaskScript,
// so an object it writes into a folder's store shared with
// core.sharedRepository=group is readable by group and others (0444) and
// its new fan-out directory searchable by them, whatever restrictive umask
// the daemon itself has; the agents' sandbox mask (0007) would have left
// the same object 0440 (the contrast proves the fixture).
// Threat: loose objects a step lease running as another user cannot open
// after Swarm's own commits and rebases (production: 0440 objects, fixed
// only by a manual chmod).
// Owners: withGitUmask over real Git; the routed path through
// routeLocalGit when bubblewrap is available. Umask is process-wide: do not
// parallelize.
func TestRoutedGitWritesObjectsLeasesCanRead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	write := func(wrap func(...string) []string) (os.FileMode, os.FileMode) {
		t.Helper()
		repo := t.TempDir()
		localFixtureGit(t, repo, "init", "-q", "-b", "dev")
		localFixtureGit(t, repo, "config", "core.sharedRepository", "group")
		if err := os.Chmod(filepath.Join(repo, ".git", "objects"), 0o775); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("routed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		argv := wrap("git", "-C", repo, "hash-object", "-w", "f.txt")
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		id := strings.TrimSpace(string(out))
		obj, err := os.Stat(filepath.Join(repo, ".git", "objects", id[:2], id[2:]))
		if err != nil {
			t.Fatal(err)
		}
		dir, err := os.Stat(filepath.Join(repo, ".git", "objects", id[:2]))
		if err != nil {
			t.Fatal(err)
		}
		return obj.Mode().Perm(), dir.Mode().Perm()
	}
	obj, dir := write(withGitUmask)
	if obj != 0o444 || dir&0o055 != 0o055 {
		t.Fatalf("routed Git wrote object %v in directory %v, want 0444 in a directory group and others can search", obj, dir)
	}
	if obj, _ := write(withSandboxUmask); obj&0o004 != 0 {
		t.Fatalf("fixture: the sandbox mask also gives others read (%v), so the contrast proves nothing", obj)
	}

	t.Run("through routeLocalGit", func(t *testing.T) {
		m := localTestManager(t)
		source := t.TempDir()
		localFixtureGit(t, source, "init", "-q", "-b", "primary")
		localFixtureGit(t, source, "config", "core.sharedRepository", "group")
		if err := os.Chmod(filepath.Join(source, ".git", "objects"), 0o775); err != nil {
			t.Fatal(err)
		}
		m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{source}, nil }})
		if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("routed by the daemon\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		id := localRoutedGit(t, m, source, "hash-object", "-w", "f.txt")
		info, err := os.Stat(filepath.Join(source, ".git", "objects", id[:2], id[2:]))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o444 {
			t.Fatalf("routed Git wrote object %v, want 0444", info.Mode().Perm())
		}
	})
}

// Purpose: daemon Git that RouteGit does not route (--sandbox=off on either
// backend) on a registered folder runs under gitUmaskScript too, so an
// object it writes into the folder's store is 0444 even though the daemon's
// own umask (here 0077; the service's UMask=0007) would leave it unreadable
// to others. Git on a directory outside every registered folder is left
// exactly as prepared (no folder store to protect).
// Threat: worktree add, commits, merges, rebases and fetches by the daemon
// with the sandbox off keep writing 0440 objects step leases cannot read,
// left to the periodic repair.
// Owners: Manager.RouteGit's not-routed branch and hostGitUmask, over real
// Git through the command RouteGit prepared. Umask is process-wide: do not
// parallelize.
func TestUnroutedDaemonGitWritesObjectsLeasesCanRead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	managers := map[string]func(t *testing.T) *Manager{
		"container backend off": func(t *testing.T) *Manager {
			return newTestManager(t, ModeOff, readyRunner(), Config{StateDir: t.TempDir()})
		},
		"bubblewrap backend off": func(t *testing.T) *Manager {
			m, err := NewManager(context.Background(), Config{Backend: BackendBubblewrap, Mode: ModeOff}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(m.Close)
			return m
		},
	}
	for name, newManager := range managers {
		t.Run(name, func(t *testing.T) {
			m := newManager(t)
			folder, outside := t.TempDir(), t.TempDir()
			for _, repo := range []string{folder, outside} {
				localFixtureGit(t, repo, "init", "-q", "-b", "dev")
				localFixtureGit(t, repo, "config", "core.sharedRepository", "group")
				if err := os.Chmod(filepath.Join(repo, ".git", "objects"), 0o775); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("daemon git, sandbox off\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{folder}, nil }})
			if m.Active() {
				t.Fatalf("fixture: the sandbox is active (%+v), so Git would be routed", m.Status())
			}

			hash := func(repo string) (*exec.Cmd, string) {
				t.Helper()
				cmd := exec.Command("git", "-C", repo, "hash-object", "-w", "f.txt")
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
				if err := m.RouteGit(context.Background(), cmd); err != nil {
					t.Fatal(err)
				}
				prepared := strings.Join(cmd.Args, " ")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git (%s): %v %s", prepared, err, out)
				}
				id := strings.TrimSpace(string(out))
				info, err := os.Stat(filepath.Join(repo, ".git", "objects", id[:2], id[2:]))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o444 && repo == folder {
					t.Fatalf("unrouted daemon Git (%s) wrote object %v, want 0444", prepared, info.Mode().Perm())
				}
				if info.Mode().Perm()&0o004 != 0 && repo == outside {
					t.Fatalf("fixture: the daemon's umask also gives others read (%v), so the contrast proves nothing", info.Mode().Perm())
				}
				return cmd, prepared
			}
			if cmd, prepared := hash(folder); filepath.Base(cmd.Path) != "sh" || !strings.Contains(prepared, gitUmaskScript) {
				t.Fatalf("Git on a registered folder not under the Git mask: %s", prepared)
			}
			if cmd, prepared := hash(outside); filepath.Base(cmd.Args[0]) != "git" || strings.Contains(prepared, gitUmaskScript) {
				t.Fatalf("Git outside every registered folder was rewritten: %s", prepared)
			}
		})
	}
}
