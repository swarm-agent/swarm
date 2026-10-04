package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: manage-sessions recovery must save reviewed leftovers from multiple
// checkpoints even when target-session attribution is missing. Real Git through
// public runtime dispatch is the narrowest layer proving both files land together,
// earlier commits survive, unrelated staging survives, and retries do not repeat.
func TestGitRecoveryMultipleCheckpointLeftovers(t *testing.T) {
	repo, base := recoveryTestRepo(t)
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("earlier", "already committed\n")
	recoveryTestGit(t, repo, "add", "--", "earlier")
	recoveryTestGit(t, repo, "commit", "-m", "earlier checkpoint")
	head := recoveryTestGit(t, repo, "rev-parse", "HEAD")
	write("selected", "first checkpoint leftover\n")
	write("second", "second checkpoint leftover\n")
	write("unrelated", "unrelated staged work\n")
	recoveryTestGit(t, repo, "add", "--", "unrelated")
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	r, scope := recoveryTestRuntime(repo)
	service := &recoveryBookkeeping{}
	r.sessions = service // target session no longer exists; no checkpoint attribution
	args := map[string]any{"action": "commit", "recovery": true, "workspace_path": repo, "expected_branch": "dev", "expected_head": head, "files": []string{"selected", "second"}, "message": "save checkpoint leftovers", "request_id": "leftovers", "session_id": "missing"}
	raw, _ := json.Marshal(args)
	for i := 0; i < 2; i++ {
		out, err := r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{Name: "manage-sessions", Arguments: string(raw)})
		if err != nil || !strings.Contains(out, `"git_success":true`) || !strings.Contains(out, `"reconciliation":"pending"`) {
			t.Fatalf("leftover recovery: %s %v", out, err)
		}
		var result struct {
			Files    []string `json:"files"`
			Replayed bool     `json:"replayed"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Files) != 2 || result.Replayed != (i == 1) {
			t.Fatalf("incorrect receipt: %s %v", out, err)
		}
	}
	if service.writes != 0 {
		t.Fatal("missing session received bookkeeping writes")
	}
	for name, want := range map[string]string{"selected": "first checkpoint leftover", "second": "second checkpoint leftover", "earlier": "already committed", "unrelated": "base"} {
		if got := recoveryTestGit(t, repo, "show", "HEAD:"+name); got != want {
			t.Fatalf("committed %s = %q, want %q", name, got, want)
		}
	}
	if got := recoveryTestGit(t, repo, "rev-list", "--count", base+"..HEAD"); got != "2" {
		t.Fatalf("earlier commit lost or recovery duplicated: %s", got)
	}
	if parent := recoveryTestGit(t, repo, "rev-parse", "HEAD^"); parent != head {
		t.Fatal("recovery rewrote earlier history")
	}
	after, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil || !bytes.Equal(index, after) {
		t.Fatal("unrelated staging changed")
	}
	if got, err := os.ReadFile(filepath.Join(repo, "unrelated")); err != nil || string(got) != "unrelated staged work\n" {
		t.Fatal("unrelated worktree content changed")
	}
}

// Purpose: recoveryCommit must reject a losing writer's stale expected HEAD while
// retaining newer worktree edits and its winning commit. Sequential interleaving
// of real Git requests deterministically exercises the optimistic concurrency
// boundary without sleep-based scheduling or a synthetic workload.
func TestGitRecoveryStaleWriterPreservesNewerEdits(t *testing.T) {
	repo, head := recoveryTestRepo(t)
	write := func(contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "selected"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("winner\n")
	args := map[string]any{"expected_branch": "dev", "expected_head": head, "files": []string{"selected"}, "message": "winner", "request_id": "winner"}
	if _, err := recoveryCommit(context.Background(), repo, args, "owner/user"); err != nil {
		t.Fatal(err)
	}
	winner := recoveryTestGit(t, repo, "rev-parse", "HEAD")
	write("newer uncommitted edit\n")
	before := recoveryTestGit(t, repo, "status", "--porcelain=v1")
	refs := recoveryTestGit(t, repo, "show-ref")
	args["request_id"] = "loser"
	args["message"] = "loser"
	if _, err := recoveryCommit(context.Background(), repo, args, "owner/user"); err == nil || !strings.Contains(err.Error(), "HEAD is stale") {
		t.Fatalf("stale writer not rejected: %v", err)
	}
	if recoveryTestGit(t, repo, "rev-parse", "HEAD") != winner || recoveryTestGit(t, repo, "show-ref") != refs || recoveryTestGit(t, repo, "status", "--porcelain=v1") != before {
		t.Fatal("stale rejection changed Git refs or status")
	}
	if got, err := os.ReadFile(filepath.Join(repo, "selected")); err != nil || string(got) != "newer uncommitted edit\n" {
		t.Fatal("newer worktree edit lost")
	}
	if got := recoveryTestGit(t, repo, "show", "HEAD:selected"); got != "winner" {
		t.Fatal("winning commit changed")
	}
}

// Purpose: manage-worktree recovery must authorize the destination independently
// and bind integration to exactly the reviewed commit range. Runtime dispatch
// against real linked worktrees proves rejection leaves both roots and all refs
// unchanged, rather than merely checking a permission status string.
func TestGitRecoveryIntegrationRejectsUnauthorizedTargetAndWrongRange(t *testing.T) {
	target, base := recoveryTestRepo(t)
	source := filepath.Join(t.TempDir(), "source")
	recoveryTestGit(t, target, "worktree", "add", "-b", "feature", source)
	if err := os.WriteFile(filepath.Join(source, "selected"), []byte("feature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	recoveryTestGit(t, source, "commit", "-am", "feature")
	head := recoveryTestGit(t, source, "rev-parse", "HEAD")
	args := map[string]any{"action": "integrate", "recovery": true, "workspace_path": source, "source_branch": "feature", "source_head": head, "target_workspace_path": target, "target_branch": "dev", "target_head": base, "commits": []string{head}, "request_id": "integrate"}
	refs := recoveryTestGit(t, target, "show-ref")
	for _, scenario := range []string{"unauthorized-target", "wrong-range"} {
		t.Run(scenario, func(t *testing.T) {
			r, scope := recoveryTestRuntime(source)
			if scenario == "wrong-range" {
				r, scope = recoveryTestRuntime(source, target)
				args["commits"] = []string{base}
			}
			raw, _ := json.Marshal(args)
			_, err := r.ExecuteForWorkspaceScopeWithRuntime(context.Background(), scope, Call{Name: "manage-worktree", Arguments: string(raw)})
			want := "account workspace catalog"
			if scenario == "wrong-range" {
				want = "reviewed commits do not exactly match"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("wrong rejection: %v", err)
			}
			if recoveryTestGit(t, target, "show-ref") != refs || recoveryTestGit(t, target, "rev-parse", "HEAD") != base || recoveryTestGit(t, source, "rev-parse", "HEAD") != head {
				t.Fatal("rejected integration mutated refs")
			}
			if recoveryTestGit(t, target, "status", "--porcelain") != "" || recoveryTestGit(t, source, "status", "--porcelain") != "" || recoveryTestGit(t, target, "show", "HEAD:selected") != "base" {
				t.Fatal("rejected integration changed source/destination contents")
			}
		})
	}
}
