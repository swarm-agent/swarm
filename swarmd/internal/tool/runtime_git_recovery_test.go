package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
)

func recoveryTestGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func recoveryTestRepo(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "absent"))
	repo := t.TempDir()
	recoveryTestGit(t, repo, "init", "-b", "dev")
	recoveryTestGit(t, repo, "config", "user.name", "Recovery Test")
	recoveryTestGit(t, repo, "config", "user.email", "recovery@example.invalid")
	for _, name := range []string{"selected", "unrelated"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	recoveryTestGit(t, repo, "add", "--", "selected", "unrelated")
	recoveryTestGit(t, repo, "commit", "-m", "base")
	return repo, recoveryTestGit(t, repo, "rev-parse", "HEAD")
}

type recoveryCatalog struct {
	gitManageWorkspaceService
}

func (s *recoveryCatalog) ScopeForPathForPrincipal(p identity.Principal, path string) (workspaceruntime.Scope, error) {
	return workspaceruntime.Scope{Matched: p.AccountScopeID == "owner" && p.UserID == "user" && s.owned[path], ResolvedPath: path}, nil
}

func recoveryTestRuntime(paths ...string) (*Runtime, WorkspaceScope) {
	owned := map[string]bool{}
	for _, path := range paths {
		owned[path] = true
	}
	return &Runtime{workspace: &recoveryCatalog{gitManageWorkspaceService{owned: owned}}}, WorkspaceScope{Principal: identity.Principal{AccountScopeID: "owner", UserID: "user"}}
}

// Purpose: recoveryCommit must publish only reviewed files without consuming
// unrelated staging or requiring any session/plan store. Real Git is the
// narrowest layer proving index preservation and durable exact-request retries.
func TestGitRecoveryCommitPreservesIndexAndRetries(t *testing.T) {
	repo, head := recoveryTestRepo(t)
	for _, name := range []string{"selected", "unrelated", "untracked"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("changed\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	recoveryTestGit(t, repo, "add", "--", "unrelated")
	before, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	r, scope := recoveryTestRuntime(repo) // no ambient checkout or session service
	args := map[string]any{"workspace_path": repo, "expected_branch": "dev", "expected_head": head, "request_id": "retry-key", "message": "recover", "files": []string{"selected"}}
	raw, _ := json.Marshal(args)
	out, err := r.executeOne(context.Background(), scope, Call{Name: "git_commit", Arguments: string(raw)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(repo, "untracked")); err != nil || string(contents) != "changed\n" {
		t.Fatal("unrelated untracked file changed")
	}
	newHead := recoveryTestGit(t, repo, "rev-parse", "HEAD")
	if newHead == head || !strings.Contains(out, newHead) || recoveryTestGit(t, repo, "show", "HEAD:unrelated") != "base" || recoveryTestGit(t, repo, "show", "HEAD:selected") != "changed" {
		t.Fatalf("incorrect recovery result: %s", out)
	}
	after, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("original index changed")
	}
	// A new Runtime models lost in-memory/bookkeeping state after Git success.
	r, scope = recoveryTestRuntime(repo)
	out, err = r.executeOne(context.Background(), scope, Call{Name: "git_commit", Arguments: string(raw)}, nil)
	if err != nil || !strings.Contains(out, `"replayed":true`) || recoveryTestGit(t, repo, "rev-parse", "HEAD") != newHead {
		t.Fatalf("retry duplicated/lost success: %s %v", out, err)
	}
	args["message"] = "different"
	if _, err := recoveryCommit(context.Background(), repo, args, "owner/user"); err == nil {
		t.Fatal("conflicting retry accepted")
	}
	if recoveryTestGit(t, repo, "rev-parse", "HEAD") != newHead {
		t.Fatal("conflicting retry changed HEAD")
	}
}

// Purpose: recoveryRepository and recoveryCommit must reject forged account
// authority, stale guards, pathspecs and symlink traversal without changing HEAD.
// Real temporary repositories plus a principal-aware catalog stub isolate this
// boundary without relying on session attribution or ambient filesystem grants.
func TestGitRecoveryRejectsUnauthorizedAndUnsafeSelections(t *testing.T) {
	repo, head := recoveryTestRepo(t)
	r, scope := recoveryTestRuntime(repo)
	wrong := scope
	wrong.Principal.AccountScopeID = "other"
	if _, err := r.recoveryRepository(wrong, repo); err == nil {
		t.Fatal("cross-account repository accepted")
	}
	if _, err := r.recoveryRepository(scope, t.TempDir()); err == nil {
		t.Fatal("uncataloged repository accepted")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../escape", ":(glob)*", "selected*", "escape/file", ".git/config", "."} {
		args := map[string]any{"expected_branch": "dev", "expected_head": head, "message": "bad", "request_id": path, "files": []string{path}}
		if _, err := recoveryCommit(context.Background(), repo, args, "owner/user"); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	if err := recoveryGuard(context.Background(), repo, "other", head); err == nil {
		t.Fatal("stale branch accepted")
	}
	if err := recoveryGuard(context.Background(), repo, "dev", strings.Repeat("a", 40)); err == nil {
		t.Fatal("stale head accepted")
	}
	if recoveryTestGit(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("rejection mutated HEAD")
	}
}

// Purpose: recoveryIntegrate preserves original commit identity/source work and
// never reports empty or divergent work as integrated. Real linked worktrees
// prove destination postconditions and retry inspection without bookkeeping.
func TestGitRecoveryIntegrationAndBashWithoutCheckout(t *testing.T) {
	target, base := recoveryTestRepo(t)
	source := filepath.Join(t.TempDir(), "source")
	recoveryTestGit(t, target, "worktree", "add", "-b", "feature", source)
	if err := os.WriteFile(filepath.Join(source, "selected"), []byte("feature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	recoveryTestGit(t, source, "commit", "-am", "feature")
	head := recoveryTestGit(t, source, "rev-parse", "HEAD")
	r, scope := recoveryTestRuntime(source, target)
	args := map[string]any{"workspace_path": source, "source_branch": "feature", "source_head": head, "target_workspace_path": target, "target_branch": "dev", "target_head": base, "commits": []string{head}}
	for i := 0; i < 2; i++ {
		out, err := r.recoveryIntegrate(scope, args)
		if err != nil || !strings.Contains(out, `"integrated":true`) || recoveryTestGit(t, target, "rev-parse", "HEAD") != head {
			t.Fatalf("integration: %s %v", out, err)
		}
	}
	args["target_head"] = head
	if _, err := r.recoveryIntegrate(scope, args); err == nil {
		t.Fatal("zero new commits reported as integrated")
	}
	if err := os.WriteFile(filepath.Join(target, "selected"), []byte("destination\n"), 0600); err != nil {
		t.Fatal(err)
	}
	recoveryTestGit(t, target, "commit", "-am", "diverge")
	diverged := recoveryTestGit(t, target, "rev-parse", "HEAD")
	args["target_head"] = diverged
	if _, err := r.recoveryIntegrate(scope, args); err == nil {
		t.Fatal("divergence accepted")
	}
	if recoveryTestGit(t, source, "rev-parse", "HEAD") != head || recoveryTestGit(t, target, "rev-parse", "HEAD") != diverged {
		t.Fatal("divergence rejection modified source/destination")
	}
	bash, _ := json.Marshal(map[string]any{"workspace_path": source, "command": "git rev-parse HEAD", "explanation": []string{"Inspect recovery source."}, "category": "read", "critical": false})
	out, err := r.executeOne(context.Background(), scope, Call{Name: "bash", Arguments: string(bash)}, nil)
	if err != nil || !strings.Contains(out, head) {
		t.Fatalf("explicit Bash cwd failed: %s %v", out, err)
	}
}
