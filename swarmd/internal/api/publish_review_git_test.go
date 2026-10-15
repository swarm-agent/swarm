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
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: prepareLandWorktree must preserve approval only across an unchanged
// real Git patch (including pipeline range-diff verification), and abort conflicts without landing. A temporary repository
// exercises Git's actual merge-base, patch-id and rebase behavior at the queue
// preparation boundary, with no daemon, credentials or external repository.
// Exact approval must supersede stale retry evidence when upstream absorbs a
// commit, while equivalent BLOCK still prevents landing at that new base.
func TestLandReviewRealGitRebase(t *testing.T) {
	for _, mode := range []string{"same_patch", "conflict", "restart", "block_rebased", "stale_base_block", "retry", "retry_block", "absorbed", "absorbed_block"} {
		conflict := mode == "conflict"
		t.Run(mode, func(t *testing.T) {
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
			originalBase := git("rev-parse", "HEAD")
			git("checkout", "-b", "agent/job")
			put("file", "job\n")
			git("commit", "-am", "job")
			firstJob := git("rev-parse", "HEAD")
			absorbed := mode == "absorbed" || mode == "absorbed_block"
			if absorbed {
				put("second", "second job commit\n")
				git("add", ".")
				git("commit", "-m", "second job commit")
			}
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
			if absorbed {
				base = originalBase
				git("branch", "-f", "dev", base)
			}
			blockedHead := ""
			if mode == "stale_base_block" {
				git("checkout", "-b", "blocked", base)
				git("cherry-pick", reviewed)
				blockedHead = git("rev-parse", "HEAD")
				git("checkout", "agent/job")
				git("update-ref", "refs/swarm/publish/dev", base)
				git("branch", "-f", "dev", originalBase)
			}
			dbPath := filepath.Join(t.TempDir(), "queue")
			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			q := publish.NewQueue(db)
			_, err = q.Enqueue(publish.Entry{Account: "account", Request: publish.Request{JobID: "job", Folder: dir, Branch: "agent/job", Head: reviewed, BaseBranch: "dev"}})
			if err != nil {
				t.Fatal(err)
			}
			land := 0
			interrupted := false
			hooks := publish.QueueHooks{Prepare: func(ctx context.Context, e *publish.Entry) error {
				e.ValidateReview = func(head, patch, landingBase string) error {
					if landingBase != base {
						t.Fatal("mutable or incorrect comparison base")
					}
					if blockedHead != "" {
						return rejectBlockingPatch(head, patch, []store.JobReview{{Head: blockedHead, Verdict: "block"}}, func(h string) (string, error) {
							cmd := exec.CommandContext(ctx, "bash", "-o", "pipefail", "-c", `git diff --binary "$1...$2" | git patch-id --stable`, "patch-id", landingBase, h)
							cmd.Dir = dir
							cmd.Env = env
							out, err := cmd.Output()
							if err != nil {
								return "", err
							}
							return strings.Fields(string(out))[0], nil
						})
					}
					e.Review.ReviewID = "review-id"
					if (mode == "block_rebased" || (mode == "retry_block" && interrupted)) && head != reviewed {
						return reviewRequired(head)
					}
					return nil
				}
				return prepareLandWorktree(ctx, publish.Rule{Mode: "land", RequireReview: true, Pipeline: mode == "same_patch" || mode == "conflict"}, e, dir, nil, 1, func(ctx context.Context, r sandbox.ExecRequest) (*provider.ExecResult, error) {
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
				if mode == "restart" && !interrupted {
					interrupted = true
					return publish.LandResult{}, context.Canceled
				}
				if (mode == "retry" || mode == "retry_block" || absorbed) && !interrupted {
					interrupted = true
					return publish.LandResult{}, errors.New("transient landing failure")
				}
				land++
				git("checkout", "dev")
				git("merge", "--ff-only", e.Request.Head)
				return publish.LandResult{Commit: e.Request.Head}, nil
			}, SendBack: func(context.Context, publish.Entry) error { return nil }}
			err = q.Process(ctx, "account", dir, hooks)
			if mode == "retry" || mode == "retry_block" || absorbed {
				saved, _, getErr := q.Get("account", "job")
				if getErr != nil || saved.State != "land_failed" || saved.Review == nil {
					t.Fatalf("missing failed receipt: %+v %v", saved, getErr)
				}
				if absorbed {
					// Upstream absorbs the first commit without changing the approved
					// job head. The remaining aggregate patch differs from the receipt.
					if git("rev-parse", "HEAD") != reviewed {
						t.Fatal("approval head moved")
					}
					base = firstJob
					git("branch", "-f", "dev", base)
					if mode == "absorbed_block" {
						blockedHead = reviewed
					}
				}
				queued, _, enqueueErr := q.EnqueueDelivery(publish.Entry{Account: "account", Request: saved.Request}, true)
				if enqueueErr != nil || queued.Review == nil {
					t.Fatalf("lost retry evidence: %+v %v", queued, enqueueErr)
				}
				err = q.Process(ctx, "account", dir, hooks)
			}
			if mode == "restart" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected interruption: %v", err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				db, err = store.Open(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				q = publish.NewQueue(db)
				saved, _, err := q.Get("account", "job")
				if err != nil || saved.Review == nil || saved.Review.ReviewID != "review-id" || saved.Review.RebasedHead == reviewed {
					t.Fatalf("missing durable evidence: %+v %v", saved, err)
				}
				err = q.Process(ctx, "account", dir, hooks)
			}
			if err != nil {
				t.Fatal(err)
			}
			entry, _, err := q.Get("account", "job")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "block_rebased" || mode == "stale_base_block" || mode == "retry_block" || mode == "absorbed_block" {
				if land != 0 || entry.State != "land_failed" || !strings.Contains(entry.Reason, "needs an OK review") {
					t.Fatalf("BLOCK ignored: %+v", entry)
				}
				return
			}
			if conflict {
				if land != 0 || entry.State != "sent_back" || !strings.Contains(entry.Reason, "new OK review") {
					t.Fatalf("conflict landed or lacks remediation: %+v", entry)
				}
				if git("rev-parse", "HEAD") != reviewed || git("rev-parse", "dev") != base || git("status", "--porcelain") != "" {
					t.Fatal("conflict not cleanly aborted")
				}
			} else {
				if land != 1 || entry.State != "landed" || (!absorbed && entry.Commit == reviewed) || git("rev-parse", "dev") != entry.Commit {
					t.Fatalf("rebased patch not landed: %+v", entry)
				}
			}
		})
	}
}
