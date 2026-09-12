package run

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: validateRunRepositoryIdentity must admit a session's unchanged owned
// lane after a history reset without weakening owner/runtime/branch provenance
// or the stricter workspace-transition gate. Real temporary Git plus a snapshot
// is the narrowest layer that proves startup chooses the execution policy.
func TestRunRepositoryIdentityAfterHistoryReset(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(path string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	source, lane := filepath.Join(root, "source"), filepath.Join(root, "lane")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	git(source, "init", "-b", "dev")
	git(source, "config", "user.name", "Fixture")
	git(source, "config", "user.email", "fixture@example.invalid")
	git(source, "commit", "--allow-empty", "-m", "initial")
	initial := git(source, "rev-parse", "HEAD")
	git(source, "commit", "--allow-empty", "-m", "allocation base")
	base := git(source, "rev-parse", "HEAD")
	git(source, "worktree", "add", "-b", "agent/owned", lane)
	git(lane, "reset", "--soft", initial)
	if err := os.WriteFile(filepath.Join(lane, "recovery.txt"), []byte("keep recovery\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(lane, "add", "recovery.txt")
	before := git(lane, "diff", "--cached")
	session := pebblestore.SessionSnapshot{
		ID: "fixture-session", WorktreeEnabled: true, WorktreeRootPath: lane, WorktreeBranch: "agent/owned",
		Metadata: map[string]any{
			"swarm_v3_worktree_owner_session_id": "fixture-session",
			"swarm_v3_source_workspace_path":     source,
			"swarm_v3_runtime_workspace_path":    lane,
			"swarm_v3_worktree_base_commit":      base,
		},
	}
	service := &Service{}
	if err := service.validateRunRepositoryIdentity(session, identity.Principal{}); err != nil {
		t.Fatalf("session cannot continue after history reset: %v", err)
	}
	if err := validateSessionRepositoryIdentity(session); err == nil {
		t.Fatal("workspace transition accepted divergent history")
	}
	for key, invalid := range map[string]string{
		"swarm_v3_worktree_owner_session_id": "foreign-session",
		"swarm_v3_runtime_workspace_path":    source,
		"swarm_v3_source_workspace_path":     lane,
		"swarm_v3_worktree_base_commit":      strings.Repeat("f", 40),
	} {
		old := session.Metadata[key]
		session.Metadata[key] = invalid
		if err := service.validateRunRepositoryIdentity(session, identity.Principal{}); err == nil {
			t.Errorf("accepted invalid %s", key)
		}
		session.Metadata[key] = old
	}
	session.WorktreeBranch = "wrong"
	if err := service.validateRunRepositoryIdentity(session, identity.Principal{}); err == nil {
		t.Fatal("accepted wrong branch")
	}
	if git(lane, "rev-parse", "HEAD") != initial || git(lane, "diff", "--cached") != before || git(source, "rev-parse", "HEAD") != base || session.Metadata["swarm_v3_worktree_base_commit"] != base {
		t.Fatal("validation rewrote history, staged work, or recorded provenance")
	}
}
