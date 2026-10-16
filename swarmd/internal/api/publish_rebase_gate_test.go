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

// Purpose: the preparation/queue boundary must carry review across patch-identical
// rebases, defer changed patches to review without a coder round, and reject
// unreviewed source. Real temporary Git repositories exercise patch/context changes;
// the landing hook asserts that no unapproved head crosses the boundary.
func TestLandingRebaseReviewGate(t *testing.T) {
	for _, mode := range []string{"identical", "changed", "unreviewed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			dir := t.TempDir()
			env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = dir
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			put := func(s string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "file"), []byte(s), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-b", "dev")
			put("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")
			git("add", ".")
			git("commit", "-m", "base")
			git("checkout", "-b", "agent/job")
			put("one\ntwo\nJOB\nfour\nfive\nsix\nseven\neight\n")
			git("commit", "-am", "job")
			reviewed := git("rev-parse", "HEAD")
			git("checkout", "dev")
			// A context-only change affects range-diff but must not replace patch-id authority.
			put("one\ntwo\nthree\nfour\nfive\nsix\nseven\nUPSTREAM\n")
			git("commit", "-am", "upstream")
			base := git("rev-parse", "HEAD")
			git("checkout", "agent/job")
			q := publish.NewQueue(&landMemoryStore{})
			_, err := q.Enqueue(publish.Entry{Account: "account", Request: publish.Request{JobID: "job", Folder: dir, Branch: "agent/job", Head: reviewed, BaseBranch: "dev"}})
			if err != nil {
				t.Fatal(err)
			}
			landed := false
			err = q.Process(ctx, "account", dir, publish.QueueHooks{Prepare: func(ctx context.Context, e *publish.Entry) error {
				return prepareLandWorktree(ctx, publish.Rule{Mode: "land", RequireReview: true, Pipeline: true}, e, dir, nil, 1, func(ctx context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
					cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
					cmd.Dir = dir
					cmd.Env = env
					out, err := cmd.Output()
					res := &provider.ExecResult{Stdout: string(out)}
					var exit *exec.ExitError
					if errors.As(err, &exit) {
						res.ExitCode = exit.ExitCode()
						return res, nil
					}
					if err == nil && len(r.Command) > 1 && r.Command[0] == "git" && r.Command[1] == "rebase" && mode == "identical" {
						git("commit", "--amend", "-m", "job (rebased)")
					}
					if err == nil && len(r.Command) > 1 && r.Command[0] == "git" && r.Command[1] == "rebase" && mode == "changed" {
						put("one\ntwo\nCHANGED\nfour\nfive\nsix\nseven\nUPSTREAM\n")
						git("commit", "-am", "resolution")
					}
					return res, err
				}, func(context.Context, publish.Rule, string) (publish.Base, error) {
					return publish.Base{Commit: base, Branch: "dev"}, nil
				}, func(head string) error {
					if mode == "unreviewed" || head != reviewed {
						return reviewRequired(head)
					}
					return nil
				})
			}, Land: func(_ context.Context, e publish.Entry) (publish.LandResult, error) {
				landed = true
				git("checkout", "dev")
				git("merge", "--ff-only", e.Request.Head)
				return publish.LandResult{Commit: e.Request.Head}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			e, _, err := q.Get("account", "job")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "identical":
				if !landed || e.State != "landed" || e.Review == nil || e.Review.ReviewedHead != reviewed || e.Review.RebasedHead != e.Commit {
					t.Fatalf("identical patch rejected: %+v", e)
				}
			case "changed":
				if landed || e.State != "review_pending" || e.SendBacks != 0 || e.Review != nil || e.Request.Head != git("rev-parse", "HEAD") || !strings.Contains(e.Reason, "review only the conflict resolution") || !strings.Contains(e.Reason, reviewed) || git("rev-parse", "dev") != base {
					t.Fatalf("changed patch not deferred safely: %+v", e)
				}
			case "unreviewed":
				if landed || e.State != "land_failed" || git("rev-parse", "HEAD") != reviewed {
					t.Fatalf("unreviewed source accepted: %+v", e)
				}
			}
		})
	}
}
