package tool

import (
	"os"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/gitenv"
)

// Purpose: the recovery gate behind bash/git_commit with a workspace_path in a
// project chat admits the daemon's own Git environment (core.sharedRepository
// set at startup by gitenv.UseUmaskSharing) and still refuses an inherited
// Git selector or a foreign config entry.
// Threat: the orchestrator in every project chat unable to run any command
// in the project's folder ("explicit recovery refuses inherited
// GIT_CONFIG_COUNT") on a daemon that sets that variable itself.
func TestRecoveryEnvironmentAdmitsTheDaemonsOwnGitConfig(t *testing.T) {
	for _, key := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_DIR"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	if err := gitenv.UseUmaskSharing(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("GIT_CONFIG_COUNT") == "" {
		t.Fatal("the daemon's own GIT_CONFIG_COUNT is not set")
	}
	if err := recoveryEnvironment(); err != nil {
		t.Fatalf("the daemon's own Git config refused: %v", err)
	}
	if err := recoveryEnvironmentOf(append(os.Environ(), "GIT_DIR=/elsewhere/.git")); err == nil || !strings.Contains(err.Error(), "GIT_DIR") {
		t.Fatalf("inherited GIT_DIR admitted: %v", err)
	}
	if err := recoveryEnvironmentOf(gitenv.AppendConfig(os.Environ(), "core.worktree", "/elsewhere")); err == nil || !strings.Contains(err.Error(), "core.worktree") {
		t.Fatalf("foreign config entry admitted: %v", err)
	}
}
