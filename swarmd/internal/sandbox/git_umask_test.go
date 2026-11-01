package sandbox

import (
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
