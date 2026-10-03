package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: manageWorktreePromote must retain the original nonempty source tip
// on a diverged target. Real allocated Git lanes exercise the actual runtime
// wiring (a cherry-pick would change the tip), preflight and ancestry verifier.
// Conflicts and empty work must reject without target effects.
func TestManageWorktreePromoteRealAncestry(t *testing.T) {
	for _, scenario := range []string{"diverged", "conflict", "empty", "rewritten"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			f := newRecoveryFixture(t, true)
			source, _, err := f.sessions.GetSession(f.scope.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			source.Metadata["base_commit"] = f.base
			if _, _, err := f.sessions.UpdateMetadata(source.ID, source.Metadata); err != nil {
				t.Fatal(err)
			}
			commit := func(path, name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				recoveryGit(t, path, "add", name)
				recoveryGit(t, path, "commit", "-m", text)
			}
			if scenario != "empty" {
				commit(f.primary, "feature", "source edit")
			}
			name := "unrelated"
			if scenario == "conflict" {
				name = "feature"
			}
			commit(f.primarySource, name, "target edit")
			if scenario == "rewritten" {
				tree := recoveryGit(t, f.primarySource, "rev-parse", "HEAD^{tree}")
				rewritten := recoveryGit(t, f.primarySource, "commit-tree", tree, "-m", "rewritten target")
				recoveryGit(t, f.primarySource, "update-ref", "refs/heads/dev", rewritten)
			}
			head := recoveryGit(t, f.primary, "rev-parse", "HEAD")
			target := recoveryGit(t, f.primarySource, "rev-parse", "HEAD")
			_, err = f.runtime.manageWorktreePromote(f.scope, map[string]any{"source_session_id": f.scope.SessionID, "target_branch": "dev", "target_head": target})
			if scenario != "diverged" {
				if err == nil || recoveryGit(t, f.primarySource, "rev-parse", "HEAD") != target {
					t.Fatalf("rejected promotion changed target: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				recoveryGit(t, f.primarySource, "merge-base", "--is-ancestor", head, "HEAD")
				recoveryGit(t, f.primarySource, "merge-base", "--is-ancestor", target, "HEAD")
				data, err := os.ReadFile(filepath.Join(f.primarySource, "feature"))
				if err != nil || string(data) != "source edit" {
					t.Fatal("source content missing")
				}
			}
			data, readErr := os.ReadFile(filepath.Join(f.primarySource, name))
			if readErr != nil || string(data) != "target edit" || recoveryGit(t, f.primarySource, "status", "--porcelain") != "" || recoveryGit(t, f.primary, "rev-parse", "HEAD") != head {
				t.Fatal("promotion lost target edits, dirtied target or rewrote source")
			}
		})
	}
}
