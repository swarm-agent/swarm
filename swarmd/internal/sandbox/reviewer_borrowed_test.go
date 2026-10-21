package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: a reviewer that reads another session's worktree in place
// (RegisterBorrowedReviewCheckout) gets that worktree for its own session
// only, mounted read-only, while the owner's own commands in it are never
// routed into the review sandbox; releasing it leaves nothing that refuses
// the owner or the next reviewer, and an earlier reviewer session cannot run
// in a later reviewer's registration.
// Threat: a reviewer able to write the coder's worktree, a second reviewer
// sharing one review sandbox, a coder locked out of its own worktree by a
// reviewer's registration or closing tombstone, or a finished reviewer
// session running again in the next review.
// Owners: Manager.RegisterBorrowedReviewCheckout, reviewScope,
// enforceReviewIdentity, scopeRunArgs, CaptureReviewIdentity /
// RestoreReviewIdentity, EndReview / ForgetReview over real Git worktrees;
// the container engine is the fake runner.
func TestBorrowedReviewCheckout(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	project, worktrees := resolveExisting(t.TempDir()), resolveExisting(t.TempDir())
	git(project, "init", "-q", "-b", "dev")
	git(project, "commit", "-q", "--allow-empty", "-m", "base")
	lane := filepath.Join(worktrees, "demo", "agent-task-coder")
	git(project, "worktree", "add", "-q", "-b", "agent/task-coder", lane)
	disposable := filepath.Join(worktrees, "demo", "review-other")
	git(project, "worktree", "add", "-q", "--detach", disposable)

	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{StateDir: t.TempDir()})
	m.SetLayout(Layout{
		Projects:      func() ([]string, error) { return []string{project}, nil },
		WorktreesRoot: worktrees,
		Bucket:        func(string) (string, error) { return "demo", nil },
	})
	ctx := context.Background()

	if err := m.RegisterBorrowedReviewCheckout(lane, "rev-borrow-1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := m.RegisterBorrowedReviewCheckout(lane, "rev-borrow-2"); err == nil {
		t.Fatal("a second reviewer session borrowed a worktree under review")
	}
	if err := m.BindReviewSession("rev-borrow-1", lane); err != nil {
		t.Fatal(err)
	}
	// The owner's own commands in its worktree are not the reviewer's.
	owner, ok, err := m.ScopeFor(lane)
	if err != nil || !ok || owner.ReviewCheckout != "" {
		t.Fatalf("owner scope = %+v %v %v, want no review checkout", owner, ok, err)
	}
	// The reviewer's commands are, by its session identity.
	reviewCtx, err := WithReviewSession(ctx, "rev-borrow-1", lane)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := m.enforceReviewIdentity(reviewCtx, owner, lane)
	if err != nil || reviewer.ReviewCheckout != lane {
		t.Fatalf("reviewer scope = %+v %v", reviewer, err)
	}
	args := strings.Join(m.scopeRunArgs("review", project, "spec", []string{project}, SecretSetup{}, EnvironmentImage{Image: m.cfg.Image}, lane), " ")
	if !strings.Contains(args, "type=bind,src="+lane+",dst="+lane+",readonly") {
		t.Fatalf("borrowed worktree is not mounted read-only: %s", args)
	}
	state, err := m.CaptureReviewIdentity("rev-borrow-1", lane)
	if err != nil || !state.Borrowed {
		t.Fatalf("captured identity = %+v %v, want borrowed", state, err)
	}

	// Releasing it: the closing tombstone and registration are gone.
	if err := m.EndReview(ctx, lane); err != nil {
		t.Fatal(err)
	}
	m.ForgetReview(lane)
	m.mu.Lock()
	_, registered := m.reviews[lane]
	closing := m.reviewClosing[lane]
	m.mu.Unlock()
	if registered || closing || borrowedReviewSession(lane) != "" {
		t.Fatalf("after release: registered %v closing %v borrowed %q", registered, closing, borrowedReviewSession(lane))
	}
	// The next round's reviewer borrows it; the finished one cannot run in it.
	if err := m.RegisterBorrowedReviewCheckout(lane, "rev-borrow-2"); err != nil {
		t.Fatalf("next reviewer: %v", err)
	}
	if _, err := m.enforceReviewIdentity(reviewCtx, owner, lane); err == nil {
		t.Fatal("the finished reviewer session ran in the next review")
	}
	// A restored durable identity of the finished reviewer cannot take it back.
	if err := m.RestoreReviewIdentity(state); err == nil {
		t.Fatal("restored the finished reviewer over the next one")
	}

	// A disposable review checkout is never re-registered as borrowed.
	if err := m.RegisterReviewCheckout(disposable); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterBorrowedReviewCheckout(disposable, "rev-borrow-3"); err == nil {
		t.Fatal("a disposable review checkout was borrowed")
	}
}
