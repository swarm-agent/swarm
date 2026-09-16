package automation

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Requirement: V3Runtime must allocate an isolated occurrence lane from the saved
// repository's HEAD even when it is detached (as in exact-commit testbenches).
// Threat: implicit current-branch resolution rejects before session creation,
// leaving accepted occurrences pending forever. The runtime allocation boundary
// with real temporary Git/store authorities is the narrowest observable proof;
// foreign ownership and name collisions must still reject without source changes.
func TestAutomationDetachedHEADAllocation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(path string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-b", "dev")
	git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	head := git(repo, "rev-parse", "HEAD")
	git(repo, "checkout", "--detach", head)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := workspace.NewService(store.NewWorkspaceStore(db))
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "owner", AccountScopeID: "account"}
	if _, err := ws.AddForPrincipal(p, repo, "fixture", "", false); err != nil {
		t.Fatal(err)
	}
	v := &V3Runtime{worktrees: worktree.NewService(store.NewWorktreeStore(db), ws, nil)}
	foreign := p
	foreign.AccountScopeID = "foreign"
	before := git(repo, "worktree", "list", "--porcelain")
	if _, err := v.allocateWorkspace(foreign, repo, "foreign-run", "0123456789abcdef"); err == nil {
		t.Fatal("foreign workspace accepted")
	}
	if got := git(repo, "worktree", "list", "--porcelain"); got != before {
		t.Fatal("denial allocated a lane")
	}
	allocation, err := v.allocateWorkspace(p, repo, "execution", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if allocation.WorkspacePath == repo || allocation.BaseCommit != head || git(allocation.WorkspacePath, "rev-parse", "HEAD") != head {
		t.Fatal("incorrect isolated base", allocation)
	}
	after := git(repo, "worktree", "list", "--porcelain")
	if _, err := v.allocateWorkspace(p, repo, "execution", "0123456789abcdef"); err == nil {
		t.Fatal("existing lane overwritten")
	}
	if got := git(repo, "worktree", "list", "--porcelain"); got != after {
		t.Fatal("collision changed lanes")
	}
	if git(repo, "rev-parse", "HEAD") != head || git(repo, "branch", "--show-current") != "" || git(repo, "status", "--porcelain") != "" {
		t.Fatal("source checkout changed")
	}
}
