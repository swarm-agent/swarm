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

// Purpose: prepareLandWorktree must preserve approval only across an unchanged
// real Git patch, and abort conflicts without landing. A temporary repository
// exercises Git's actual merge-base, patch-id and rebase behavior at the queue
// preparation boundary, with no daemon, credentials or external repository.
func TestLandReviewRealGitRebase(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_patch", true: "conflict"}[conflict], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			dir := t.TempDir()
			env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid", "GIT_TERMINAL_PROMPT=0")
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = dir
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			put := func(name, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-b", "dev")
			put("file", "base\n")
			git("add", ".")
			git("commit", "-m", "base")
			git("checkout", "-b", "agent/job")
			put("file", "job\n")
			git("commit", "-am", "job")
			reviewed := git("rev-parse", "HEAD")
			git("checkout", "dev")
			if conflict {
				put("file", "upstream\n")
			} else {
				put("other", "upstream\n")
			}
			git("add", ".")
			git("commit", "-m", "upstream")
			base := git("rev-parse", "HEAD")
			git("checkout", "agent/job")
			q := publish.NewQueue(&landMemoryStore{})
			_, err := q.Enqueue(publish.Entry{Account: "account", Request: publish.Request{JobID: "job", Folder: dir, Branch: "agent/job", Head: reviewed, BaseBranch: "dev"}})
			if err != nil {
				t.Fatal(err)
			}
			land := 0
			err = q.Process(ctx, "account", dir, publish.QueueHooks{Prepare: func(ctx context.Context, e *publish.Entry) error {
				return prepareLandWorktree(ctx, publish.Rule{Mode: "land", RequireReview: true}, e, dir, nil, 1, func(ctx context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
					cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
					cmd.Dir = r.WorkingDir
					cmd.Env = env
					out, err := cmd.Output()
					res := &provider.ExecResult{Stdout: string(out)}
					var exit *exec.ExitError
					if errors.As(err, &exit) {
						res.ExitCode = exit.ExitCode()
						return res, nil
					}
					return res, err
				}, func(context.Context, publish.Rule, string) (publish.Base, error) {
					return publish.Base{Commit: base, Branch: "dev"}, nil
				}, func(head string) error {
					if head != reviewed {
						return reviewRequired(head)
					}
					return nil
				})
			}, Land: func(_ context.Context, e publish.Entry) (publish.LandResult, error) {
				land++
				git("checkout", "dev")
				git("merge", "--ff-only", e.Request.Head)
				return publish.LandResult{Commit: e.Request.Head}, nil
			}, SendBack: func(context.Context, publish.Entry) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			entry, _, err := q.Get("account", "job")
			if err != nil {
				t.Fatal(err)
			}
			if conflict {
				if land != 0 || entry.State != "sent_back" || !strings.Contains(entry.Reason, "new OK review") {
					t.Fatalf("conflict landed or lacks remediation: %+v", entry)
				}
				if git("rev-parse", "HEAD") != reviewed || git("rev-parse", "dev") != base || git("status", "--porcelain") != "" {
					t.Fatal("conflict not cleanly aborted")
				}
			} else {
				if land != 1 || entry.State != "landed" || entry.Commit == reviewed || git("rev-parse", "dev") != entry.Commit {
					t.Fatalf("rebased patch not landed: %+v", entry)
				}
			}
		})
	}
}
