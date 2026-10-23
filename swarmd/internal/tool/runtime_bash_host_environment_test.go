package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: a host-mode (no sandbox) bash command runs Git in the person's
// folder, which another uid owns, without "dubious ownership": the command
// environment carries safe.directory for exactly the authorized folders and
// continues a GIT_CONFIG_COUNT the daemon already set.
// Threat: on a sandbox-off install every agent Git command in the person's
// folder fails, so the orchestrator cannot even read the repository's log.
func TestHostCommandEnvironmentTrustsTheAuthorizedFolders(t *testing.T) {
	scope := WorkspaceScope{PrimaryPath: "/srv/a", Roots: []string{"/srv/a", "/srv/b/", "relative", ""}}
	env := hostCommandEnvironment(scope, []string{"PATH=/bin", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.sharedRepository", "GIT_CONFIG_VALUE_0=umask"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GIT_CONFIG_KEY_0=core.sharedRepository", "GIT_CONFIG_KEY_1=safe.directory", "GIT_CONFIG_VALUE_1=/srv/a", "GIT_CONFIG_KEY_2=safe.directory", "GIT_CONFIG_VALUE_2=/srv/b", "GIT_CONFIG_COUNT=3"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env misses %s:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "relative") || strings.Count(joined, "GIT_CONFIG_COUNT=") != 1 {
		t.Fatalf("relative path admitted or count duplicated:\n%s", joined)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// Git reads the entries: a repository config value set this way is visible.
	repo := t.TempDir()
	base := []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	init := exec.Command("git", "-C", repo, "init", "-q")
	init.Env = base
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	cmd := exec.Command("git", "-C", repo, "config", "--get-all", "safe.directory")
	cmd.Env = hostCommandEnvironment(WorkspaceScope{PrimaryPath: repo}, base)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != filepath.Clean(repo) {
		t.Fatalf("git did not read safe.directory: %v %q", err, out)
	}
}
