package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
	out, err := r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{Name: "git_commit", Arguments: string(raw)})
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
	out, err = r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{Name: "git_commit", Arguments: string(raw)})
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
	args := map[string]any{"workspace_path": source, "source_branch": "feature", "source_head": head, "target_workspace_path": target, "target_branch": "dev", "target_head": base, "commits": []string{head}, "request_id": "integrate-key"}
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
	scope.RejectScopeExpansion = true
	scope.ExplicitRepositoryRecovery = true
	bash, _ := json.Marshal(map[string]any{"workspace_path": source, "command": "git rev-parse HEAD", "explanation": []string{"Inspect recovery source."}, "category": "read", "critical": false})
	out, err := r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{Name: "bash", Arguments: string(bash)})
	if err != nil || !strings.Contains(out, head) {
		t.Fatalf("explicit Bash cwd failed: %s %v", out, err)
	}
}

// Purpose: the public runtime manage-sessions dispatch must work with missing
// attribution and retain one Git commit across concurrent retries and failed
// bookkeeping. Real Git plus unavailable session storage isolates that boundary.
func TestGitRecoveryManagedCommitConcurrentRetryAndBookkeepingFailure(t *testing.T) {
	repo, head := recoveryTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "selected"), []byte("recover\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, scope := recoveryTestRuntime(repo)
	scope.RejectScopeExpansion, scope.ExplicitRepositoryRecovery = true, true
	args := map[string]any{"action": "commit", "recovery": true, "workspace_path": repo, "expected_branch": "dev", "expected_head": head, "request_id": "concurrent", "message": "recover", "files": []string{"selected"}, "session_id": "missing-attribution"}
	raw, _ := json.Marshal(args)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			out, err := r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{Name: "manage-sessions", Arguments: string(raw)})
			if err == nil && (!strings.Contains(out, `"git_success":true`) || !strings.Contains(out, `"reconciliation":"pending"`) || !strings.Contains(out, `"git_status":`)) {
				err = fmt.Errorf("missing Git success or pending bookkeeping: %s", out)
			}
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := recoveryTestGit(t, repo, "rev-list", "--count", head+"..HEAD"); got != "1" {
		t.Fatalf("duplicate commits: %s", got)
	}
	// A restricted Coder cannot obtain the project recovery exception from args.
	restricted := scope
	restricted.ExplicitRepositoryRecovery = false
	out, err := r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), restricted, Call{Name: "manage-sessions", Arguments: string(raw)})
	if err == nil {
		t.Fatalf("restricted recovery accepted: %s", out)
	}
	if got := recoveryTestGit(t, repo, "rev-list", "--count", head+"..HEAD"); got != "1" {
		t.Fatal("rejected call mutated Git")
	}
}

// Purpose: recoveryRepository rejects inherited Git target redirection before
// invoking Git, preventing an authorized cwd from mutating another repository.
func TestGitRecoveryRejectsInheritedGitTarget(t *testing.T) {
	repo, head := recoveryTestRepo(t)
	r, scope := recoveryTestRuntime(repo)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "foreign"))
	if _, err := r.recoveryRepository(scope, repo); err == nil {
		t.Fatal("inherited Git target accepted")
	}
	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	if recoveryTestGit(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("rejected target changed HEAD")
	}
}

type recoveryBookkeeping struct {
	manageSessionService
	session pebblestore.SessionSnapshot
	fail    bool
	writes  int
}

func (s *recoveryBookkeeping) GetSession(id string) (pebblestore.SessionSnapshot, bool, error) {
	return s.session, id == s.session.ID, nil
}
func (s *recoveryBookkeeping) ListSessionEventsBefore(string, uint64, int) ([]pebblestore.V3SessionEvent, error) {
	return []pebblestore.V3SessionEvent{{Seq: 1}}, nil
}
func (s *recoveryBookkeeping) ApplySessionMutation(in pebblestore.V3SessionMutationInput) (pebblestore.V3SessionMutationResult, error) {
	s.writes++
	if s.fail {
		return pebblestore.V3SessionMutationResult{}, fmt.Errorf("injected bookkeeping failure")
	}
	if in.ExpectedLastEventSeq == nil || *in.ExpectedLastEventSeq != 1 || in.Kind != pebblestore.V3SessionMutationUpdateMetadata {
		return pebblestore.V3SessionMutationResult{}, fmt.Errorf("missing canonical evidence fence")
	}
	s.session = *in.Session
	return pebblestore.V3SessionMutationResult{}, nil
}

// Purpose: recoveryEvidence must never reinterpret a post-commit bookkeeping
// failure as Git failure. A fault-injected canonical mutation proves retry repairs
// evidence without duplicate commits, lifecycle completion, or cross-account writes.
func TestGitRecoveryRepairsBookkeepingAfterGitSuccess(t *testing.T) {
	repo, head := recoveryTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "selected"), []byte("recover\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, scope := recoveryTestRuntime(repo)
	service := &recoveryBookkeeping{fail: true, session: pebblestore.SessionSnapshot{ID: "stranded", AccountScopeID: "owner", UserID: "user", WorkspacePath: repo, Lifecycle: &pebblestore.SessionLifecycleSnapshot{Phase: "needs_review"}}}
	r.sessions = service
	args := map[string]any{"action": "commit", "recovery": true, "workspace_path": repo, "expected_branch": "dev", "expected_head": head, "files": []string{"selected"}, "message": "recovery", "request_id": "receipt", "session_id": "stranded"}
	out, err := r.executeManageSessions(context.Background(), scope, args)
	if err != nil || !strings.Contains(out, `"git_success":true`) || !strings.Contains(out, `"reconciliation":"pending"`) || service.writes != 1 {
		t.Fatalf("failure hid success: %s %v", out, err)
	}
	committed := recoveryTestGit(t, repo, "rev-parse", "HEAD")
	service.fail = false
	out, err = r.executeManageSessions(context.Background(), scope, args)
	if err != nil || !strings.Contains(out, `"reconciliation":"recorded"`) || recoveryTestGit(t, repo, "rev-parse", "HEAD") != committed {
		t.Fatalf("retry: %s %v", out, err)
	}
	if service.session.Metadata["git_recovery_evidence"] == nil || service.session.Lifecycle.Phase != "needs_review" {
		t.Fatal("evidence absent or lifecycle falsely completed")
	}
	service.session.AccountScopeID = "foreign"
	writes := service.writes
	out, err = r.executeManageSessions(context.Background(), scope, args)
	if err != nil || !strings.Contains(out, `"reconciliation":"pending"`) || service.writes != writes {
		t.Fatalf("foreign bookkeeping mutated: %s %v", out, err)
	}
}

// Purpose: recoveryIntegrate must refuse true divergent/conflicting histories,
// stale destinations and dirty target files without losing either side. Real
// linked worktrees are the narrowest proof of source/destination preservation.
func TestGitRecoveryIntegrationRejectsConflictingAndDirtyDestination(t *testing.T) {
	target, base := recoveryTestRepo(t)
	source := filepath.Join(t.TempDir(), "source")
	recoveryTestGit(t, target, "worktree", "add", "-b", "feature", source)
	if err := os.WriteFile(filepath.Join(source, "selected"), []byte("source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	recoveryTestGit(t, source, "commit", "-am", "source")
	head := recoveryTestGit(t, source, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "untracked"), []byte("preserve\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, scope := recoveryTestRuntime(source, target)
	args := map[string]any{"workspace_path": source, "source_branch": "feature", "source_head": head, "target_workspace_path": target, "target_branch": "dev", "target_head": base, "commits": []string{head}, "request_id": "integration"}
	if err := os.WriteFile(filepath.Join(target, "selected"), []byte("target\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.recoveryIntegrate(scope, args); err == nil {
		t.Fatal("dirty target accepted")
	}
	if recoveryTestGit(t, target, "rev-parse", "HEAD") != base {
		t.Fatal("dirty rejection advanced target")
	}
	recoveryTestGit(t, target, "commit", "-am", "conflicting target")
	diverged := recoveryTestGit(t, target, "rev-parse", "HEAD")
	if _, err := r.recoveryIntegrate(scope, args); err == nil {
		t.Fatal("stale target accepted")
	}
	args["target_head"] = diverged
	if _, err := r.recoveryIntegrate(scope, args); err == nil {
		t.Fatal("true divergence accepted")
	}
	if recoveryTestGit(t, target, "rev-parse", "HEAD") != diverged || recoveryTestGit(t, source, "rev-parse", "HEAD") != head {
		t.Fatal("conflict altered history")
	}
	if got, err := os.ReadFile(filepath.Join(source, "untracked")); err != nil || string(got) != "preserve\n" {
		t.Fatal("source dirty work lost")
	}
	if got, err := os.ReadFile(filepath.Join(target, "selected")); err != nil || string(got) != "target\n" {
		t.Fatal("target conflicting content lost")
	}
}
