package gitenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Git started by the daemon creates directories without setgid even
// in a folder shared with core.sharedRepository=group (as `install.sh
// workspace add` shares a person's folder), so the daemon's RestrictSUIDSGID
// never refuses a new worktree; the setting continues an existing
// GIT_CONFIG_COUNT and is not added twice.
// Threat: every task and job worktree in a person's folder failing with
// "could not create leading directories ...: Operation not permitted", or
// the daemon's other Git config entries (safe.directory) dropped.
// Owners: AppendConfig over real Git: a repository shared with
// core.sharedRepository=group and `git worktree add` with the resulting
// environment. RestrictSUIDSGID itself is systemd's and not simulated here:
// the test checks Git no longer asks for setgid.
func TestDaemonGitCreatesNoSetgidDirectories(t *testing.T) {
	env := AppendConfig([]string{"PATH=/bin", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=/srv/a"}, "core.sharedRepository", "umask")
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=/srv/a", "GIT_CONFIG_KEY_1=core.sharedRepository", "GIT_CONFIG_VALUE_1=umask", "GIT_CONFIG_COUNT=2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env misses %s:\n%s", want, joined)
		}
	}
	if again := strings.Join(AppendConfig(env, "core.sharedRepository", "umask"), "\n"); strings.Count(again, "core.sharedRepository") != 1 || !strings.Contains(again, "GIT_CONFIG_COUNT=2") {
		t.Fatalf("added twice:\n%s", again)
	}

	base := []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	git := func(env []string, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	repo := t.TempDir()
	git(base, repo, "init", "-q", "-b", "dev")
	git(base, repo, "config", "core.sharedRepository", "group")
	git(base, repo, "commit", "-q", "--allow-empty", "-m", "base")
	setgid := func(path string) bool {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode()&os.ModeSetgid != 0
	}
	// The daemon's Git (first, so no parent it creates in is setgid already:
	// the kernel passes a parent's setgid on by itself) asks for no setgid
	// anywhere it creates directories.
	lane := filepath.Join(t.TempDir(), "lane")
	git(AppendConfig(base, "core.sharedRepository", "umask"), repo, "worktree", "add", "-q", "--detach", lane, "HEAD")
	for _, dir := range []string{lane, filepath.Join(repo, ".git", "worktrees"), filepath.Join(repo, ".git", "worktrees", "lane")} {
		if setgid(dir) {
			t.Fatalf("%s was created setgid", dir)
		}
	}
	// Without the override the same Git does ask for it (what the daemon's
	// RestrictSUIDSGID refused).
	shared := filepath.Join(t.TempDir(), "shared")
	git(base, repo, "worktree", "add", "-q", "--detach", shared, "HEAD")
	if !setgid(shared) {
		t.Fatalf("%s: this Git did not make a shared worktree setgid, so the fixture proves nothing", shared)
	}
}

// Purpose: the daemon's own core.sharedRepository=umask entry is never
// mistaken for an inherited Git config, while any other GIT_CONFIG entry is
// reported as foreign.
// Threat: every bash and git_commit with a workspace_path in a project chat
// refused with "explicit recovery refuses inherited GIT_CONFIG_COUNT" because
// the daemon set that variable itself at startup.
func TestForeignConfigKnowsTheDaemonsOwnEntries(t *testing.T) {
	ownMu.Lock()
	own = map[string]string{"core.sharedrepository": "umask"}
	ownMu.Unlock()
	daemon := AppendConfig([]string{"PATH=/bin"}, "core.sharedRepository", "umask")
	if got := ForeignConfig(daemon); got != "" {
		t.Fatalf("the daemon's own entry reported foreign: %q", got)
	}
	if got := ForeignConfig([]string{"PATH=/bin"}); got != "" {
		t.Fatalf("no entries reported foreign: %q", got)
	}
	for _, env := range [][]string{
		AppendConfig(daemon, "core.worktree", "/elsewhere"),
		{"GIT_CONFIG_COUNT=1"},
		{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.sharedRepository", "GIT_CONFIG_VALUE_0=group"},
		{"GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0=/elsewhere"},
	} {
		if ForeignConfig(env) == "" {
			t.Fatalf("foreign entry accepted: %v", env)
		}
	}
}
