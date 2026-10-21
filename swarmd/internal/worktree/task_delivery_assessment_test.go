package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: AssessTaskDelivery reads a worker job's worktree of the folder
// (commits are in the folder's repository as they are made) and refuses a lane
// from another repository. Real local Git at the inspection boundary is the
// narrowest layer proving candidate counts, empty, missing and foreign lanes
// without changing the source repository.
func TestAssessDeliveryWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, mode := range []string{"candidate", "empty", "missing", "linked", "advanced", "foreign", "wrong_branch"} {
		t.Run(mode, func(t *testing.T) {
			git := func(path string, args ...string) string {
				t.Helper()
				out, err := runGit(path, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			repo := t.TempDir()
			git(repo, "init", "-b", "dev")
			commit := func(path, message string) {
				git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-am", message)
			}
			commit(repo, "base")
			base := git(repo, "rev-parse", "HEAD")
			a, err := (&Service{}).allocateSessionWorkspaceWithBranchMode(repo, true, "", "agent/job", "worker-execution-assessment", true)
			if err != nil {
				t.Fatal(err)
			}
			lane := a.WorkspacePath
			if mode == "linked" {
				lane = filepath.Join(t.TempDir(), "lane")
				git(repo, "worktree", "add", "-b", "agent/linked", lane)
				a.BranchName = "agent/linked"
			}
			if mode != "empty" && mode != "missing" {
				for _, name := range []string{"one", "two"} {
					if err := os.WriteFile(filepath.Join(lane, name), []byte(name), 0600); err != nil {
						t.Fatal(err)
					}
					git(lane, "add", name)
					commit(lane, name)
				}
			}
			head := git(lane, "rev-parse", "HEAD")
			if mode == "advanced" {
				commit(repo, "target advanced")
			}
			target := git(repo, "rev-parse", "HEAD")
			if mode == "foreign" {
				// The same branch and commits, in another repository.
				foreign := filepath.Join(t.TempDir(), "foreign")
				git(repo, "clone", "-q", "--no-local", "--branch", a.BranchName, repo, foreign)
				lane = foreign
			}
			if mode == "wrong_branch" {
				git(lane, "checkout", "-b", "other")
			}
			if mode == "missing" {
				if err := os.RemoveAll(lane); err != nil {
					t.Fatal(err)
				}
			}
			got := AssessTaskDelivery(context.Background(), TaskDeliveryInput{SourcePath: lane, TargetPath: repo, Identity: pebblestore.TaskDeliveryAssessment{BaseOID: base, SourceBranch: a.BranchName, TargetBranch: "dev"}})
			switch mode {
			case "foreign", "wrong_branch":
				if got.State != "unavailable" || len(got.AllowedActions) != 0 {
					t.Fatalf("invalid lane: %+v", got)
				}
			case "missing":
				if got.State != "unavailable" || got.ReasonCode != "checkout_missing" || len(got.AllowedActions) != 0 {
					t.Fatalf("missing: %+v", got)
				}
			case "empty":
				if got.State != "empty" || got.CandidateCommits != 0 {
					t.Fatalf("empty: %+v", got)
				}
			default:
				if got.State != "candidate_work" || got.CandidateCommits != 2 || got.SourceOID != head || got.TargetOID != target || len(got.Files) != 2 || len(got.AllowedActions) != 1 {
					t.Fatalf("candidate: %+v", got)
				}
			}
			if git(repo, "rev-parse", "HEAD") != target {
				t.Fatal("assessment changed target")
			}
		})
	}
}
