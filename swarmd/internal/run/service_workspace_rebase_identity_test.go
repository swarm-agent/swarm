package run

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: runtime scope admission must use execution-only rebase identity, not
// strand the session or rewrite ownership/history to match detached HEAD. Real
// Git plus a session snapshot is the narrowest run-layer proof; foreign owner,
// stale runtime/history must fail without mutation.
// Worktree-layer tests separately exercise both Git backends and all state bytes.
func TestRunRepositoryIdentityInterruptedRebase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(path string, fail bool, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if (err != nil) != fail || ctx.Err() != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, lane := filepath.Join(root, "source"), filepath.Join(root, "lane")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	git(source, false, "init", "-b", "dev")
	git(source, false, "config", "user.name", "Fixture")
	git(source, false, "config", "user.email", "fixture@example.invalid")
	write(filepath.Join(source, "conflict.txt"), "base\n")
	git(source, false, "add", "conflict.txt")
	git(source, false, "commit", "-m", "base")
	base := git(source, false, "rev-parse", "HEAD")
	git(source, false, "worktree", "add", "-b", "agent/owned", lane)
	write(filepath.Join(lane, "conflict.txt"), "owned\n")
	git(lane, false, "commit", "-am", "owned")
	write(filepath.Join(source, "conflict.txt"), "upstream\n")
	git(source, false, "commit", "-am", "upstream")
	git(lane, true, "rebase", "--merge", "dev")
	if git(lane, false, "ls-files", "--unmerged") == "" {
		t.Fatal("fixture must have unresolved conflicts")
	}
	admin := git(lane, false, "rev-parse", "--absolute-git-dir")
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	state := func() []string {
		return []string{read(filepath.Join(admin, "HEAD")), read(filepath.Join(admin, "index")), read(filepath.Join(lane, "conflict.txt")), read(filepath.Join(admin, "rebase-merge", "head-name")), read(filepath.Join(admin, "rebase-merge", "orig-head")), git(source, false, "show-ref")}
	}
	before := state()
	session := pebblestore.SessionSnapshot{
		ID: "fixture-session", UserID: "fixture-user", AccountScopeID: "fixture-account", WorkspacePath: source,
		WorktreeEnabled: true, WorktreeRootPath: lane, WorktreeBranch: "agent/owned",
		Metadata: map[string]any{
			"swarm_v3_worktree_owner_session_id": "fixture-session",
			"swarm_v3_source_workspace_path":     source,
			"swarm_v3_runtime_workspace_path":    lane,
			"swarm_v3_worktree_base_commit":      base,
			"base_commit":                        base,
		},
	}
	service := &Service{}
	principal := identity.Principal{UserID: session.UserID, AccountScopeID: session.AccountScopeID}
	check := func(wantOK bool) {
		t.Helper()
		beforeSession, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := service.ResolveRuntimeWorkspaceScope(session, principal)
		if (err == nil) != wantOK {
			t.Fatalf("runtime admission wantOK=%v: %v", wantOK, err)
		}
		if wantOK && (scope.PrimaryPath != lane || scope.WorktreeBranch != "agent/owned" || scope.WorktreeBaseCommit != base) {
			t.Fatalf("runtime rebound the recorded lane: %+v", scope)
		}
		if err := validateSessionRepositoryIdentity(session); err == nil {
			t.Fatal("strict transition accepted detached rebase")
		}
		afterSession, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		if string(beforeSession) != string(afterSession) || !reflect.DeepEqual(before, state()) {
			t.Fatal("validation changed session provenance, index, conflict or rebase state")
		}
	}
	check(true)
	for key, value := range map[string]any{
		"swarm_v3_worktree_owner_session_id": "foreign-session",
		"swarm_v3_runtime_workspace_path":    source,
		"swarm_v3_source_workspace_path":     lane,
		"swarm_v3_worktree_base_commit":      strings.Repeat("f", 40),
		"swarm_v3_worktree_history":          []any{map[string]any{"path": lane, "owner_session_id": "foreign-session"}},
	} {
		old, existed := session.Metadata[key]
		session.Metadata[key] = value
		check(false)
		if existed {
			session.Metadata[key] = old
		} else {
			delete(session.Metadata, key)
		}
	}
	session.WorktreeBranch = "wrong"
	check(false)
	session.WorktreeBranch = "agent/owned"
	check(true)
}
