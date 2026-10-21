package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: EffectiveTaskBase may move a job's base forward only to a commit
// the folder's own target branch contains. The job clone belongs to the
// coder: its refs (refs/remotes/origin/<target>, refs/replace/*) and its
// grafts file are writable to agent Bash, so nothing the clone claims about
// the target may move the base.
// Threat (review finding): a coder runs `git update-ref
// refs/remotes/origin/dev HEAD~1` (or HEAD), or grafts its head onto dev, and
// swarm_get_changes, the job result's base_commit, the automation reviewer
// brief and the reviewer's own range shrink to its last commit or to nothing,
// while delivery still lands every commit.
// Owner: EffectiveTaskBase over real local Git (a folder and a clone of it).
// The narrowest layer: the callers (api, run) only pass its answer on.
func TestEffectiveTaskBaseIgnoresRefsTheCoderMoves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	folder := t.TempDir()
	git(folder, "init", "-q", "-b", "dev")
	git(folder, "commit", "-q", "--allow-empty", "-m", "base")
	recorded := git(folder, "rev-parse", "HEAD")
	clone := filepath.Join(t.TempDir(), "job")
	git(filepath.Dir(clone), "clone", "-q", folder, clone)
	git(clone, "checkout", "-q", "-b", "agent/job")
	for _, subject := range []string{"one", "two", "three"} {
		git(clone, "commit", "-q", "--allow-empty", "-m", subject)
	}
	head := git(clone, "rev-parse", "HEAD")
	// dev moves on; the job is not rebased, but the coder has fetched it.
	git(folder, "commit", "-q", "--allow-empty", "-m", "landed elsewhere")
	landed := git(folder, "rev-parse", "HEAD")
	git(clone, "fetch", "-q", "origin")
	ctx := context.Background()
	base := func() string { return EffectiveTaskBase(ctx, clone, folder, "dev", recorded, head) }

	if got := base(); got != recorded {
		t.Fatalf("job that was not rebased: base %s, want recorded %s", got, recorded)
	}
	for _, moved := range []string{head + "~1", head + "~2", head} {
		git(clone, "update-ref", "refs/remotes/origin/dev", moved)
		if got := base(); got != recorded {
			t.Fatalf("clone origin/dev moved to %s: base %s, want recorded %s", moved, got, recorded)
		}
	}
	git(clone, "update-ref", "refs/remotes/origin/dev", landed)
	// A replace ref grafting the head straight onto the landed dev.
	git(clone, "replace", "--graft", head, landed)
	if got := base(); got != recorded {
		t.Fatalf("head grafted onto dev by a replace ref: base %s, want recorded %s", got, recorded)
	}
	git(clone, "replace", "-d", head)
	if git(clone, "rev-parse", "HEAD") != head || git(folder, "rev-parse", "refs/heads/dev") != landed {
		t.Fatal("EffectiveTaskBase changed the clone or the folder")
	}

	// A real rebase onto the folder's dev still moves the base.
	git(clone, "rebase", "-q", "origin/dev")
	if got := EffectiveTaskBase(ctx, clone, folder, "dev", recorded, git(clone, "rev-parse", "HEAD")); got != landed {
		t.Fatalf("rebased job: base %s, want the folder's dev %s", got, landed)
	}
}

// Purpose: a job whose head the folder's target branch already contains
// (it was delivered by a fast-forward or a merge) keeps its recorded base,
// so its commits still show after it lands.
// Threat (review finding): merge-base(head, tip) is head itself once the
// target contains head, so the base became head: swarm_get_changes listed no
// commits and no files, swarm_get_job reported base_commit = head, a reviewer
// started after landing got an empty diff and a review by hand failed with
// "review job must have commits".
// Owner: EffectiveTaskBase over real local Git (a folder and a clone of it).
func TestEffectiveTaskBaseKeepsTheRecordedBaseOnceTheTargetContainsTheHead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, delivery := range []string{"fast-forward", "merge"} {
		t.Run(delivery, func(t *testing.T) {
			folder := t.TempDir()
			git(folder, "init", "-q", "-b", "dev")
			git(folder, "commit", "-q", "--allow-empty", "-m", "base")
			recorded := git(folder, "rev-parse", "HEAD")
			clone := filepath.Join(t.TempDir(), "job")
			git(filepath.Dir(clone), "clone", "-q", folder, clone)
			git(clone, "checkout", "-q", "-b", "agent/job")
			git(clone, "commit", "-q", "--allow-empty", "-m", "one")
			git(clone, "commit", "-q", "--allow-empty", "-m", "two")
			head := git(clone, "rev-parse", "HEAD")
			if delivery == "merge" {
				git(folder, "commit", "-q", "--allow-empty", "-m", "landed elsewhere")
			}
			// Deliver: the folder takes the job's head onto dev.
			git(folder, "fetch", "-q", clone, "agent/job")
			if delivery == "fast-forward" {
				git(folder, "merge", "-q", "--ff-only", "FETCH_HEAD")
			} else {
				git(folder, "merge", "-q", "--no-ff", "-m", "deliver job", "FETCH_HEAD")
			}
			git(clone, "fetch", "-q", "origin")
			if got := EffectiveTaskBase(context.Background(), clone, folder, "dev", recorded, head); got != recorded {
				t.Fatalf("job delivered by %s: base %s, want recorded %s (head %s)", delivery, got, recorded, head)
			}
		})
	}
}
