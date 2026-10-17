package api

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
)

// Purpose: prepareLandWorktree must transfer exact-head approval across an
// unrelated merge-train rebase, including a previous candidate as StackBase.
// Real Git at the executor boundary exercises patch-ID streams and ranges;
// mocked Git output cannot detect errors in the shell identity computation.
// A real conflicting rebase resolved at the executor boundary must instead
// invalidate approval and return a diagnosable rebase-scoped review.
func TestLandRebaseIdentity(t *testing.T) {
	for _, name := range []string{"dev", "stacked", "conflict_resolution"} {
		stacked := name == "stacked"
		conflict := name == "conflict_resolution"
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			dir := t.TempDir()
			run := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			put := func(name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			run("init", "-b", "dev")
			run("config", "user.name", "Landing Test")
			run("config", "user.email", "landing@example.invalid")
			put("a", "base\n")
			put("b", "base\n")
			run("add", ".")
			run("commit", "-m", "base")
			base := run("rev-parse", "HEAD")
			run("checkout", "-b", "job")
			put("a", "first\n")
			run("commit", "-am", "first job change")
			put("a", "second\n")
			run("commit", "-am", "second job change")
			approved := run("rev-parse", "HEAD")
			run("checkout", "dev")
			put("b", "first\n")
			run("commit", "-am", "first dev change")
			put("b", "second\n")
			put("CHANGELOG", "dev changes\n")
			if conflict {
				put("a", "upstream\n")
			}
			run("add", ".")
			run("commit", "-m", "second dev change")
			target := run("rev-parse", "HEAD")
			if stacked {
				run("checkout", "-b", "candidate")
				put("b", "previous candidate\n")
				run("commit", "-am", "previous candidate")
				target = run("rev-parse", "HEAD")
			}
			run("checkout", "job")
			entry := &publish.Entry{DeferChecks: true, Request: publish.Request{Branch: "job", Head: approved, BaseCommit: base, BaseBranch: "dev"}}
			if stacked {
				entry.StackBase = &publish.Request{Head: target, BaseBranch: "dev"}
			}
			executor := func(ctx context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
				cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
				cmd.Dir = r.WorkingDir
				out, err := cmd.Output()
				// Exercise actual conflict resolution, not synthetic patch-ID output.
				if conflict && r.Command[0] == "git" && r.Command[1] == "rebase" && err != nil {
					for i := 0; i < 2; i++ {
						if run("diff", "--name-only", "--diff-filter=U") != "a" {
							t.Fatal("expected a real conflict in a")
						}
						put("a", "resolved change "+strings.Repeat("x", i+1)+"\n")
						run("add", "a")
						cmd = exec.CommandContext(ctx, "git", "-c", "core.editor=true", "rebase", "--continue")
						cmd.Dir = dir
						out, err = cmd.Output()
						if err == nil {
							break
						}
					}
				}
				result := &provider.ExecResult{Stdout: string(out)}
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					result.ExitCode = exit.ExitCode()
					return result, nil
				}
				return result, err
			}
			err := prepareLandWorktree(ctx, publish.Rule{Folder: dir, Mode: "land", RequireReview: true, Pipeline: true}, entry, dir, nil, 1, executor,
				func(context.Context, publish.Rule, string) (publish.Base, error) {
					return publish.Base{Commit: target, Branch: "dev"}, nil
				},
				func(head string) error {
					if head != approved {
						return reviewRequired(head)
					}
					return nil
				})
			if conflict {
				var review *publish.RebaseReviewError
				if !errors.As(err, &review) || !strings.Contains(review.Reason, "reason: commit patch IDs differ: before=2 commits, after=2 commits") {
					t.Fatalf("expected reasoned rebase review after resolution, got %v", err)
				}
				if entry.Review != nil || entry.Request.Head != run("rev-parse", "HEAD") || entry.Request.BaseCommit != target {
					t.Fatalf("resolved patch retained approval or lost candidate: %+v", entry)
				}
				return
			}
			if err != nil {
				t.Fatalf("unrelated rebase lost approval: %v", err)
			}
			if entry.Review == nil || !entry.Review.PatchIdentical || entry.Review.ReviewedHead != approved || entry.Review.RebasedHead != run("rev-parse", "HEAD") || entry.Request.BaseCommit != target {
				t.Fatalf("approval not transferred: %+v", entry)
			}
		})
	}
}
