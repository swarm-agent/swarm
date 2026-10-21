package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type recordingSandboxEnder struct {
	reviews, jobs []string
	deferJobs     bool
}

func (r *recordingSandboxEnder) EndTerminalReview(_ context.Context, _, checkout string) error {
	r.reviews = append(r.reviews, checkout)
	return nil
}

func (r *recordingSandboxEnder) EndJob(_ context.Context, checkout string, terminal func() (bool, error)) error {
	r.jobs = append(r.jobs, checkout)
	if done, err := terminal(); err != nil || !done {
		return sandbox.ErrCleanupDeferred
	}
	if r.deferJobs {
		return sandbox.ErrCleanupDeferred
	}
	return nil
}

// Purpose: the durable terminal sweep ends each terminal run's sandbox
// container under the name it was started with. A read-only reviewer runs in
// a registered review scope (EndTerminalReview). A reviewer an automation step
// allowed to commit security fixes (input review_fixes "security") runs in an
// unregistered job-checkout scope, so it is ended with EndJob, like a coder.
// A deferred EndJob stays pending, so a later sweep retries it.
// Threat: after a deferred first removal or a restart, the fixing reviewer's
// job container is never removed, because the sweep only ever asks to remove
// a review container name it never had.
// Owners: cleanupTerminalSandboxes and terminalSandboxIsReview, over a real
// temp Pebble worker and session store. Only the sandbox engine
// (terminalSandboxEnder) is a recording fake; removing real containers needs a
// real sandbox runtime and is not proven here.
func TestTerminalSandboxSweepEndsAFixingReviewerLikeACoder(t *testing.T) {
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "sweep.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const account, user = "acct-1", "user-1"
	ws := pebblestore.NewWorkerStore(db)
	ss := pebblestore.NewSessionStore(db)
	worker := func(role, key string) string {
		t.Helper()
		w, err := ws.CreateWorker(account, user, pebblestore.CreateWorkerRequest{Role: role, Name: key, IdempotencyKey: key}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	reviewer, coder := worker(pebblestore.WorkerRoleReviewer, "sweep-reviewer"), worker(pebblestore.WorkerRoleCoder, "sweep-coder")
	workspace := t.TempDir()
	checkouts := map[string]string{}
	run := func(workerID, role, id string, input map[string]any) {
		t.Helper()
		checkout := filepath.Join(workspace, ".worktrees", id)
		checkouts[id] = checkout
		sessionID := "worker-execution-" + id
		if err := ss.CreateSession(pebblestore.SessionSnapshot{ID: sessionID, UserID: user, AccountScopeID: account, WorkspacePath: workspace, Title: id,
			WorktreeEnabled: true, WorktreeRootPath: checkout, WorktreeBranch: "agent/" + id, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := ws.RecordWorkerRun(account, pebblestore.WorkerRunRecord{ID: id, WorkerID: workerID, UserID: user, Role: role, SessionID: sessionID,
			Status: "succeeded", Input: input, CreatedAt: 1, StartedAt: 1, CompletedAt: 2}); err != nil {
			t.Fatal(err)
		}
	}
	run(reviewer, pebblestore.WorkerRoleReviewer, "review-ro", map[string]any{"review_job": "worker/c/r"})
	run(reviewer, pebblestore.WorkerRoleReviewer, "review-fix", map[string]any{"review_job": "worker/c/r", "review_fixes": "security"})
	run(reviewer, pebblestore.WorkerRoleReviewer, "review-odd", map[string]any{"review_job": "worker/c/r", "review_fixes": "all"})
	run(coder, pebblestore.WorkerRoleCoder, "coder", map[string]any{"prompt": "x"})

	if all, err := ws.WorkerSandboxRuns(); err != nil || len(all) != 4 {
		t.Fatalf("seeded sandbox runs = %d %v", len(all), err)
	}
	for id := range checkouts {
		if snap, found, err := ss.GetSession("worker-execution-" + id); err != nil || !found || snap.WorktreeRootPath != checkouts[id] {
			t.Fatalf("seeded session %s = %+v %v %v", id, snap, found, err)
		}
	}
	ender := &recordingSandboxEnder{deferJobs: true}
	cleanupTerminalSandboxes(context.Background(), ender, db)
	if !sameSet(ender.reviews, checkouts["review-ro"], checkouts["review-odd"]) {
		t.Fatalf("review containers ended = %v", ender.reviews)
	}
	if !sameSet(ender.jobs, checkouts["review-fix"], checkouts["coder"]) {
		t.Fatalf("job containers ended = %v", ender.jobs)
	}
	// The deferred job removals stay pending; the review removals are done.
	pending := func() []string {
		t.Helper()
		runs, err := ws.WorkerSandboxRuns()
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range runs {
			ids = append(ids, r.ID)
		}
		return ids
	}
	if got := pending(); !sameSet(got, "review-fix", "coder") {
		t.Fatalf("pending after a deferred sweep = %v", got)
	}
	// The retry sweep ends the fixing reviewer as a job again, never as a review.
	retry := &recordingSandboxEnder{}
	cleanupTerminalSandboxes(context.Background(), retry, db)
	if len(retry.reviews) != 0 || !sameSet(retry.jobs, checkouts["review-fix"], checkouts["coder"]) {
		t.Fatalf("retry sweep: reviews %v jobs %v", retry.reviews, retry.jobs)
	}
	if got := pending(); len(got) != 0 {
		t.Fatalf("pending after the retry sweep = %v", got)
	}
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		if seen[w] == 0 {
			return false
		}
		seen[w]--
	}
	return true
}
