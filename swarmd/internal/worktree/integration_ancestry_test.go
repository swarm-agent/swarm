package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: project integration verifies source HEAD ancestry, so the explicit
// PreserveAncestry mode of PrepareTaskIntegration/ApplyTaskIntegration must
// preserve original commits, not report failure after a successful cherry-pick.
// AssessTaskDelivery must admit advanced targets, leaving conflicts to preflight.
// Real isolated Git repositories prove ancestry, bytes, idempotency, and conflict
// rejection without changing the target; no provider or daemon is needed.
func TestTaskIntegrationPreserveAncestry(t *testing.T) {
	for _, scenario := range []string{"fast-forward", "divergent", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			repo := initSparseTaskRepository(t)
			git := func(path string, args ...string) string {
				t.Helper()
				out, err := runGit(path, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			write := func(path, name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			base := git(repo, "rev-parse", "HEAD")
			child := filepath.Join(t.TempDir(), "child")
			git(repo, "worktree", "add", "-b", "agent/source", child, base)
			write(child, "README.md", "source change\n")
			git(child, "add", "README.md")
			git(child, "commit", "-m", "source")
			head := git(child, "rev-parse", "HEAD")
			if scenario != "fast-forward" {
				name := "target.txt"
				if scenario == "conflict" {
					name = "README.md"
				}
				write(repo, name, "target change\n")
				git(repo, "add", name)
				git(repo, "commit", "-m", "target")
			}
			target := git(repo, "rev-parse", "HEAD")
			assessment := AssessTaskDelivery(context.Background(), TaskDeliveryInput{SourcePath: child, TargetPath: repo, Identity: pebblestore.TaskDeliveryAssessment{BaseOID: base, SourceBranch: "agent/source", TargetBranch: "dev"}})
			if assessment.State != "candidate_work" || assessment.CandidateCommits != 1 || len(assessment.AllowedActions) != 1 {
				t.Fatalf("normal candidate not admitted: %+v", assessment)
			}
			svc := &Service{}
			children := []TaskIntegrationChild{{SessionID: "source", BaseCommit: base, HeadCommit: head, PreserveAncestry: true}}
			plan, err := svc.PrepareTaskIntegration(repo, "dev", target, children)
			if scenario == "conflict" {
				if err == nil {
					t.Fatal("conflicting merge accepted")
				}
				if git(repo, "rev-parse", "HEAD") != target || git(repo, "status", "--porcelain") != "" {
					t.Fatal("conflict preflight changed target")
				}
				data, err := os.ReadFile(filepath.Join(repo, "README.md"))
				if err != nil || string(data) != "target change\n" {
					t.Fatal("conflict changed target bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			forged := plan
			forged.MergeHead = base
			if _, err := svc.ApplyTaskIntegration(repo, forged); err == nil {
				t.Fatal("forged merge head accepted")
			}
			if git(repo, "rev-parse", "HEAD") != target || git(repo, "status", "--porcelain") != "" {
				t.Fatal("forged plan changed target")
			}
			result, err := svc.ApplyTaskIntegration(repo, plan)
			if err != nil {
				t.Fatal(err)
			}
			git(repo, "merge-base", "--is-ancestor", head, result.ResultingParentHead)
			git(repo, "merge-base", "--is-ancestor", target, result.ResultingParentHead)
			if git(repo, "status", "--porcelain") != "" || git(child, "rev-parse", "HEAD") != head {
				t.Fatal("integration left dirty target or rewrote source")
			}
			data, err := os.ReadFile(filepath.Join(repo, "README.md"))
			if err != nil || string(data) != "source change\n" {
				t.Fatal("source bytes not integrated")
			}
			if scenario == "divergent" {
				data, err := os.ReadFile(filepath.Join(repo, "target.txt"))
				if err != nil || string(data) != "target change\n" {
					t.Fatal("unrelated target edit lost")
				}
			}
			retry, err := svc.PrepareTaskIntegration(repo, "dev", result.ResultingParentHead, children)
			if err != nil {
				t.Fatal(err)
			}
			again, err := svc.ApplyTaskIntegration(repo, retry)
			if err != nil || again.ResultingParentHead != result.ResultingParentHead {
				t.Fatalf("retry changed integrated target: %+v, %v", again, err)
			}
		})
	}
}
