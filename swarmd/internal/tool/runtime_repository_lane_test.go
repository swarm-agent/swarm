package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: Git status and promotion must select the same durable secondary
// repository lane, not the conversation's primary repository. Real Git and
// Pebble are necessary to prove exact bytes, intervening dev history, and
// rejection without modifying either repository.
func TestRepositoryLaneStatusAndPromotion(t *testing.T) {
	f := newRecoveryFixture(t)
	if _, err := f.runtime.manageWorktreeIntegrate(f.scope, map[string]any{"session_ids": []string{"recovery-good"}}); err != nil {
		t.Fatal(err)
	}
	primaryHead := recoveryGit(t, f.primary, "rev-parse", "HEAD")
	laneHead := recoveryGit(t, f.lane, "rev-parse", "HEAD")
	branch := recoveryGit(t, f.lane, "branch", "--show-current")
	if err := os.WriteFile(filepath.Join(f.source, "intervening.txt"), []byte("preserve dev"), 0600); err != nil {
		t.Fatal(err)
	}
	recoveryGit(t, f.source, "add", "intervening.txt")
	recoveryGit(t, f.source, "commit", "-m", "intervening dev change")
	devHead := recoveryGit(t, f.source, "rev-parse", "HEAD")
	for _, selector := range []string{f.source, f.lane} {
		out, err := f.runtime.manageSessionsGit(context.Background(), f.scope, map[string]any{"session_id": "recovery-parent", "workspace_path": selector})
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Items []struct {
				Head   string `json:"head_oid"`
				Path   string `json:"worktree_path"`
				Branch string `json:"branch"`
				Base   string `json:"base_commit"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Items) != 1 || response.Items[0].Head != laneHead || response.Items[0].Path != f.lane || response.Items[0].Branch != branch || response.Items[0].Base != f.base {
			t.Fatalf("wrong selected repository: %s", out)
		}
	}
	_, err := f.runtime.manageWorktreePromote(f.scope, map[string]any{"source_session_id": "recovery-parent", "source_branch": branch, "source_head": laneHead, "target_workspace_path": f.source, "target_branch": "dev", "target_head": devHead})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"change.txt": "recovery-good", "intervening.txt": "preserve dev"} {
		got, err := os.ReadFile(filepath.Join(f.source, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if recoveryGit(t, f.primary, "rev-parse", "HEAD") != primaryHead || recoveryGit(t, f.lane, "rev-parse", "HEAD") != laneHead || recoveryGit(t, f.source, "status", "--porcelain") != "" {
		t.Fatal("promotion changed source lane/primary or left dirty target")
	}
	recoveryGit(t, f.source, "merge-base", "--is-ancestor", devHead, "HEAD")
}

// Purpose: an explicit selector cannot grant access, redirect promotion, or
// weaken stale/dirty guards. Assert all relevant Git state remains unchanged.
func TestRepositoryLaneRejectsUnsafePromotion(t *testing.T) {
	for _, name := range []string{"unknown-source", "wrong-branch", "cross-account", "cross-user", "revoked-source", "stale-head", "stale-target", "dirty-source", "dirty-target", "symlink-lane", "foreign-lane"} {
		t.Run(name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			branch := recoveryGit(t, f.lane, "branch", "--show-current")
			args := map[string]any{"source_session_id": "recovery-parent", "source_branch": branch, "source_head": f.base, "target_workspace_path": f.source, "target_branch": "dev", "target_head": f.base}
			switch name {
			case "unknown-source":
				args["target_workspace_path"] = f.primarySource
			case "wrong-branch":
				args["source_branch"] = "agent/not-owned"
			case "cross-account":
				f.scope.Principal.AccountScopeID = "foreign"
			case "cross-user":
				f.scope.Principal.UserID = "foreign"
			case "revoked-source":
				if _, err := f.workspace.DeleteForPrincipal(f.scope.Principal, f.source); err != nil {
					t.Fatal(err)
				}
			case "stale-head":
				args["source_head"] = strings.Repeat("a", 40)
			case "stale-target":
				args["target_head"] = strings.Repeat("a", 40)
			case "dirty-source", "dirty-target":
				path := f.lane
				if name == "dirty-target" {
					path = f.source
				}
				if err := os.WriteFile(filepath.Join(path, "pending.txt"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-lane":
				moved := filepath.Join(t.TempDir(), "moved")
				if err := os.Rename(f.lane, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, f.lane); err != nil {
					t.Fatal(err)
				}
			case "foreign-lane":
				args["target_workspace_path"] = f.goodPath
			}
			paths := []string{f.primary, f.source, f.lane, f.goodPath, f.dirtyPath}
			before := map[string]string{}
			for _, path := range paths {
				before[path] = recoveryGit(t, path, "rev-parse", "HEAD") + recoveryGit(t, path, "status", "--porcelain")
			}
			if _, err := f.runtime.manageWorktreePromote(f.scope, args); err == nil {
				t.Fatal("unsafe promotion accepted")
			}
			for _, path := range paths {
				if got := recoveryGit(t, path, "rev-parse", "HEAD") + recoveryGit(t, path, "status", "--porcelain"); got != before[path] {
					t.Fatal("rejected promotion mutated Git state")
				}
			}
		})
	}
}

func TestRepositoryLaneGitStatusRejectsUnknownSelector(t *testing.T) {
	f := newRecoveryFixture(t)
	for _, selector := range []string{f.goodPath, filepath.Join(f.source, "subdir"), "relative"} {
		if _, err := f.runtime.manageSessionsGit(context.Background(), f.scope, map[string]any{"session_id": "recovery-parent", "workspace_path": selector}); err == nil {
			t.Fatalf("ignored invalid selector %q", selector)
		}
	}
	if _, err := f.workspace.DeleteForPrincipal(f.scope.Principal, f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.manageSessionsGit(context.Background(), f.scope, map[string]any{"session_id": "recovery-parent", "workspace_path": f.source}); err == nil {
		t.Fatal("revoked source accepted")
	}
}
